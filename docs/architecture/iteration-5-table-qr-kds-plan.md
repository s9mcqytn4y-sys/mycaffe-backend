# Iteration 5 Architecture Plan: Table Management, QR Ordering & KDS Relay

## 1. Executive Summary & Architecture Context

Iteration 5 mewujudkan alur inti pemesanan mandiri oleh pelanggan (Customer Self-Ordering via QR Table) dan orkestrasi peracikan di dapur (Kitchen Display System / KDS) untuk MyCaffe Core POS.

### Prinsip Arsitektur Utama

1. **Multi-Tenant Row-Level Security (RLS)**: Tabel baru `tables` dan relasi `orders.table_id` terisolasi penuh per `store_id` menggunakan PostgreSQL 18 RLS (`NOBYPASSRLS` pada role runtime `mycaffe_app`).
2. **Opaque & Regenerable QR Tokens**: Setiap meja memiliki `qr_token` kriptografis (UUIDv4 acak) yang divalidasi di server (`GET /public/tables/:token`). Jika stiker meja dicuri atau disalahgunakan, pemilik toko dapat melakukan *regenerate token* tanpa merusak riwayat transaksi lampau.
3. **Pemisahan Mode Pembayaran Pelanggan (Pay-First vs Pay-After)**:
   - **`pay_first`**: Pesanan QR berstatus `pending_payment` dan langsung menerbitkan Midtrans Dynamic QRIS. Setelah notifikasi webhook Midtrans (SHA-512 valid) terverifikasi, status bertransisi ke `queued`, nomor antrean dibuat, dan event dikirim ke KDS.
   - **`pay_after`**: Pesanan QR langsung bertransisi ke `queued` (atau open tab di kasir), langsung masuk tiket KDS, dan pembayaran diselesaikan belakangan di kasir.
4. **KDS & WebSocket Outbox Relay**: Tiket KDS difilter per stasiun persiapan (`barista` vs `kitchen`). Seluruh event status pesanan ditulis secara transaksional ke `outbox_events` dan disiarkan secara asinkron ke WebSocket subscriber channel (`kitchen` / `barista`) dengan validasi token staf dan isolasi tenant.

---

## 2. Database Schema & Migration (Phase 5a)

File: `db/migrations/20261102000014_tables_qr_and_kds.up.sql`

```sql
-- 1. Create tables table
CREATE TABLE IF NOT EXISTS tables (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    store_id UUID NOT NULL REFERENCES stores(id) ON DELETE CASCADE,
    table_number VARCHAR(50) NOT NULL,
    label VARCHAR(100),
    capacity INT NOT NULL DEFAULT 4,
    location_section VARCHAR(50) NOT NULL DEFAULT 'main',
    status VARCHAR(20) NOT NULL DEFAULT 'available',
    qr_token UUID NOT NULL DEFAULT gen_random_uuid(),
    is_active BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_tables_store_number UNIQUE (store_id, table_number),
    CONSTRAINT uq_tables_store_qr_token UNIQUE (store_id, qr_token),
    CONSTRAINT chk_tables_status CHECK (status IN ('available', 'occupied', 'reserved')),
    CONSTRAINT chk_tables_capacity CHECK (capacity > 0)
);

CREATE INDEX IF NOT EXISTS idx_tables_store_status ON tables(store_id, status);
CREATE INDEX IF NOT EXISTS idx_tables_qr_token ON tables(qr_token);

-- Enable RLS
ALTER TABLE tables ENABLE ROW LEVEL SECURITY;
CREATE POLICY tables_tenant_isolation ON tables
    FOR ALL TO mycaffe_app
    USING (store_id = NULLIF(current_setting('app.current_store_id', true), '')::UUID)
    WITH CHECK (store_id = NULLIF(current_setting('app.current_store_id', true), '')::UUID);

-- 2. Link orders to tables and add daily queue number
ALTER TABLE orders 
    ADD COLUMN IF NOT EXISTS table_id UUID REFERENCES tables(id) ON DELETE SET NULL,
    ADD COLUMN IF NOT EXISTS queue_number VARCHAR(20),
    ADD COLUMN IF NOT EXISTS tracking_token UUID DEFAULT gen_random_uuid();

CREATE INDEX IF NOT EXISTS idx_orders_store_table ON orders(store_id, table_id);
CREATE INDEX IF NOT EXISTS idx_orders_tracking_token ON orders(tracking_token);

-- 3. Function to resolve table by QR token for public ordering (SECURITY DEFINER)
CREATE OR REPLACE FUNCTION resolve_table_by_qr_token(p_token UUID)
RETURNS TABLE (
    table_id UUID,
    store_id UUID,
    store_slug VARCHAR,
    table_number VARCHAR,
    label VARCHAR,
    status VARCHAR,
    is_active BOOLEAN,
    billing_mode VARCHAR
)
SECURITY DEFINER
SET search_path = public
LANGUAGE plpgsql
AS $$
BEGIN
    RETURN QUERY
    SELECT 
        t.id AS table_id,
        t.store_id,
        s.slug AS store_slug,
        t.table_number,
        t.label,
        t.status,
        (t.is_active AND s.is_active) AS is_active,
        COALESCE(ss.billing_mode, 'pay_first')::VARCHAR AS billing_mode
    FROM tables t
    JOIN stores s ON s.id = t.store_id
    LEFT JOIN store_settings ss ON ss.store_id = t.store_id
    WHERE t.qr_token = p_token AND t.is_active = true AND s.is_active = true;
END;
$$;

REVOKE ALL ON FUNCTION resolve_table_by_qr_token(UUID) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION resolve_table_by_qr_token(UUID) TO mycaffe_app;
```

---

## 3. API Contract Specifications

### 3.1 Table Management (Staff POS & Backoffice)

- `GET /api/v1/pos/tables`: Menampilkan seluruh meja toko beserta status okupansi saat ini.
- `POST /api/v1/pos/tables`: Membuat meja baru (Permission: `table.manage`).
- `PUT /api/v1/pos/tables/:id`: Mengubah nomor meja, kapasitas, lokasi seksi, dan status.
- `POST /api/v1/pos/tables/:id/regenerate-qr`: Menerbitkan `qr_token` baru untuk meja tersebut.
- `DELETE /api/v1/pos/tables/:id`: Soft delete meja (`is_active = false`).

### 3.2 Public QR Resolution & Guest Checkout

- `GET /api/v1/public/tables/:token`: Validasi token QR meja.
- `POST /api/v1/public/stores/:slug/orders`: Pelanggan membuat pesanan mandiri.
- `GET /api/v1/public/orders/track/:tracking_token`: Status pelacak pesanan waktu-nyata pelanggan.

### 3.3 Kitchen Display System (KDS)

- `GET /api/v1/pos/kds/tickets`: Mengambil seluruh tiket aktif (`station=all|barista|kitchen`).
- `PATCH /api/v1/pos/kds/tickets/:order_id/status`: Aksi satu sentuhan memajukan status pesanan (`queued` $\to$ `preparing` $\to$ `ready` $\to$ `served`).
- **Outbox Relay**: Menulis event ke `outbox_events` (`KDS_STATUS_UPDATED`) yang di-broadcast via WebSocket `/ws`.

---

## 4. Implementation Phasing

1. **Phase 5a**: Table CRUD & Schema Migration (`tables`, RLS, QR tokens).
2. **Phase 5b**: Public QR Order Flow & Order Tracker.
3. **Phase 5c**: KDS Tickets & Realtime Outbox Relay.
4. **Phase 5d**: End-to-end Integration Test Suite & OpenAPI Specification.
