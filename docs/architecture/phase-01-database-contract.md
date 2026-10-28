# Phase 01: Database & Transactional Architecture Contract
**MyCaffe Core POS — Multi-Tenant POS, QR Table Ordering, Kitchen Display System (KDS), and Queue Platform**

---

## 1. Executive Summary & Design Scope

Dokumen ini mendefinisikan arsitektur data dan kontrak transaksional untuk **MyCaffe Core POS** pada Phase 01 (Sub-task 1 & Sub-task 2). Tujuannya adalah menjamin:
- **Tenant Isolation (Defense-in-Depth)**: Isolasi data per store/tenant menggunakan filter eksplisit `store_id` di Go ditambah PostgreSQL 18 Row-Level Security (`ENABLE ROW LEVEL SECURITY`) dengan runtime role `mycaffe_app` (`NOBYPASSRLS`).
- **Payment & Financial Integrity**: Perhitungan harga, opsi modifier, pajak, service charge, dan split bill menggunakan integer Rupiah (`BIGINT` / `int64`) serta Basis Points (`BPS`) tanpa floating-point (`float64`).
- **Full Midtrans Core API Custom UI**: Integrasi langsung ke Midtrans Core API (`qris`, `bank_transfer`, `gopay`, `shopeepay`, `card`, `cstore`) ditambah metode kasir (`cash`, `static_qris`, `edc`) tanpa ketergantungan pada tampilan popup Midtrans Snap.
- **Catalog Modifiers, 1-Level Nested Sub-Addons & Immutable Snapshots**: Dukungan penuh untuk grup opsi kustomisasi produk beserta sub-addon 1 level dan snapshot relasional pada `order_item_options`.
- **Split Bill & Multi-Attempt Payment Lifecycle**: Dukungan tagihan penuh maupun terpisah (`full`, `equal`, `by_item`, `custom_amount`) dengan riwayat percobaan pembayaran yang tidak pernah terhapus (`ON DELETE RESTRICT`).

---

## 2. Multi-Tenant Isolation, Dual Roles & Row-Level Security (RLS)

### 2.1 Dual Database Role Separation
1. **`mycaffe_migrator` (DDL / Migration Role)**: Digunakan khusus oleh CLI `cmd/migrate` (`DATABASE_MIGRATION_URL`) untuk mengeksekusi perubahan skema (`db/migrations/*.up.sql`) dengan penguncian `pg_advisory_lock`.
2. **`mycaffe_app` (DML / Runtime API Role)**: Digunakan oleh server Go API (`DATABASE_URL`) dengan atribut `NOBYPASSRLS` dan hak akses terbatas (`SELECT, INSERT, UPDATE, DELETE`) tanpa izin `DROP` atau `TRUNCATE`.

### 2.2 RLS Context Setting & Policy Design
Setiap transaksi aplikasi yang dijalankan melalui `database.WithTenantTx(ctx, db, storeID, fn)` menyetel session variable lokal:
```sql
SELECT set_config('app.current_store_id', '11111111-1111-4111-8111-111111111111', true);
```

Fungsi helper RLS:
```sql
CREATE OR REPLACE FUNCTION current_store_id() RETURNS UUID AS $$
    SELECT NULLIF(current_setting('app.current_store_id', true), '')::UUID;
$$ LANGUAGE sql STABLE;
```

Seluruh 15 tabel operasional tenant (`categories`, `products`, `modifier_groups`, `modifier_options`, `product_modifier_groups`, `option_modifier_groups`, `users`, `orders`, `order_items`, `order_item_options`, `payment_splits`, `payment_split_items`, `payments`, `store_daily_sequences`, `audit_logs`, `outbox_events`) mengaktifkan policy:
```sql
ALTER TABLE orders ENABLE ROW LEVEL SECURITY;

CREATE POLICY store_isolation_orders ON orders
    FOR ALL
    USING (store_id = current_store_id())
    WITH CHECK (store_id = current_store_id());
```

---

## 3. Store Lifecycle & Financial Retention Contract

1. **Soft Delete pada Store (`stores.deleted_at`) & User (`users.deleted_at`)**: Penutupan store atau penonaktifan staf hanya mengisi kolom `deleted_at` dan menonaktifkan `is_active`.
2. **Strict `ON DELETE RESTRICT` pada Histori Finansial**:
   - Relasi `orders.store_id -> stores.id` menggunakan `ON DELETE RESTRICT`.
   - Relasi `payments.store_id -> stores.id` dan `payments.order_id -> orders.id` menggunakan `ON DELETE RESTRICT`.
   - Menghapus baris `stores` atau `orders` yang telah memiliki transaksi `payments` akan langsung ditolak oleh PostgreSQL (`restrict_violation` / SQLSTATE `23001`).

---

## 4. Financial Modeling: Integer Rupiah & Basis Points (BPS)

### 4.1 Zero Floating-Point Rule
- Seluruh nominal uang disimpan dalam **integer Rupiah** (`BIGINT` di PostgreSQL / `int64` di Go).
- Seluruh tarif persentase (PB1, Service Charge, MDR QRIS) disimpan dalam **Basis Points (BPS)** (`100 bps = 1%`, `1000 bps = 10%`).
- String `gross_amount` dari webhook Midtrans (misal `"58905.00"`) wajib diverifikasi tanda tangannya (`SHA-512`) menggunakan string mentah asli, kemudian dikonversi ke `int64` menggunakan parser integer murni (`midtrans.ParseGrossAmountIDR`) yang menolak sen desimal tidak nol.

### 4.2 Formula Kalkulasi Finansial Server-Side (Round-Half-Up)
$$\text{Item Unit Price} = \text{Base Price} + \sum (\text{Option Price Delta} \times \text{Option Qty}) + \sum (\text{Sub-Option Price Delta} \times \text{Sub-Option Qty})$$
$$\text{Service Charge} = \left\lfloor \frac{(\text{Subtotal} - \text{Discount}) \times \text{service\_charge\_bps} + 5000}{10000} \right\rfloor$$
$$\text{Tax Amount} = \left\lfloor \frac{(\text{Subtotal} - \text{Discount} + \text{Service Charge}) \times \text{tax\_rate\_bps} + 5000}{10000} \right\rfloor$$
$$\text{Total Amount} = \text{Subtotal} - \text{Discount} + \text{Service Charge} + \text{Tax Amount}$$

---

## 5. Catalog Modifiers, 1-Level Nested Sub-Addons & Order Snapshots

1. **`modifier_groups`**: Mendefinisikan grup kustomisasi per store (`selection_type IN ('single', 'multiple')`, `is_required`, `min_select`, `max_select`).
2. **`modifier_options`**: Pilihan di dalam grup dengan `price_delta_amount BIGINT` (mendukung harga tambahan `>= 0` maupun potongan `<= 0` sesuai aturan grup), ketersediaan, dan stok.
3. **`product_modifier_groups`**: Menghubungkan produk ke `modifier_groups` beserta override `min_select` / `max_select`.
4. **`option_modifier_groups`**: Menghubungkan `parent_option_id` ke `child_group_id` untuk mendukung **1-level nested sub-addon** (contoh: memilih opsi *Oat Milk* membuka sub-grup *Takaran Foam*).
5. **`order_item_options`**: Menyimpan snapshot relasional yang tidak dapat berubah (`snapshot_group_name`, `snapshot_option_name`, `snapshot_price_delta_amount`, `quantity`, dan `parent_order_item_option_id`) saat pesanan dibuat.

---

## 6. Split Bill, Payment Attempts & KDS State Machine

### 6.1 Order, Split Bill & Payment Attempt Hierarchy
- **`orders`**: Memiliki `billing_mode` (`pay_first`, `open_tab`, `split_bill`), `total_amount`, `paid_amount`, `refunded_amount`, `status` (`order_status`), dan `payment_status` (`unpaid`, `pending`, `authorized`, `partially_paid`, `paid`, `failed`, `cancelled`, `expired`, `partially_refunded`, `refunded`, `chargeback`).
- **`payment_splits`**: Membagi tagihan `orders` ke dalam 1 bagian (`full`) atau beberapa bagian (`equal`, `by_item`, `custom_amount`) dengan invarian $\sum \text{split.amount} = \text{orders.total\_amount}$.
- **`payments`**: Mencatat setiap percobaan pembayaran (`attempt_number`) untuk suatu split/order dengan `provider_order_id` berformat `{order_number}-S{split_seq}-A{attempt_number}`. Jika QRIS kedaluwarsa (`expired`) atau gagal (`failed`), pelanggan dapat membuat percobaan baru (`A2`) tanpa menimpa log percobaan `A1`.

### 6.2 Midtrans Core API Status Mapping & Guard
| Midtrans `transaction_status` | `fraud_status` | Internal `PaymentStatus` | Keterangan |
| :--- | :--- | :--- | :--- |
| `authorize` | `accept` | `authorized` | Pre-auth kartu kredit |
| `capture` | `accept` | `paid` | Pembayaran kartu berhasil |
| `capture` | `challenge` | `pending` | Menunggu review FDS |
| `capture` | `deny` | `failed` | Ditolak FDS |
| `settlement` | `accept` / `""` | `paid` | Dana terkonfirmasi lunas |
| `pending` | `*` | `pending` | Menunggu pembayaran pelanggan |
| `deny` / `failure` | `*` | `failed` | Ditolak / gagal (tidak pernah jatuh ke `pending`) |
| `cancel` | `*` | `cancelled` | Dibatalkan |
| `expire` | `*` | `expired` | Melewati batas waktu bayar |
| `partial_refund` | `*` | `partially_refunded` | Refund sebagian |
| `refund` | `*` | `refunded` | Refund penuh |
| `chargeback` / `partial_chargeback` | `*` | `chargeback` | Sengketa / chargeback |

Fungsi `domain.CanTransitionPaymentStatus(from, to)` memastikan webhook yang datang terlambat (misal `pending` tiba setelah `paid`) diabaikan secara idempoten dan tidak pernah menurunkan status pembayaran yang sudah lunas.

### 6.3 Aturan Transisi Antrean Dapur (KDS)
- Untuk pesanan **Pay-First** (`billing_mode = 'pay_first'` atau `'split_bill'` dari meja QR), `orders.status` hanya berpindah dari `pending_payment` ke `queued` (dan memicu event `outbox_events` ke KDS) ketika **`orders.paid_amount == orders.total_amount`** (`orders.payment_status == 'paid'`).
- Untuk pesanan **Open Tab / Kasir POS** (`billing_mode = 'open_tab'`), kasir yang terotentikasi dapat langsung memasukkan pesanan ke `queued` sementara `payment_status` berada pada `unpaid` atau `partially_paid`.

---

## 7. Migration CLI & Verification Lifecycle

Seluruh migrasi database dikelola oleh `cmd/migrate` (`internal/pkg/database/migrator.go`) yang mencatat versi dan checksum SHA-256 pada tabel `schema_migrations`. Pipeline CI menguji siklus penuh pada PostgreSQL 18:
1. `go run ./cmd/migrate -action=up` (Fresh Install)
2. `psql -f scripts/verify/verify_schema_constraints.sql` (Uji FK `RESTRICT`, Check Constraints, Idempotensi Queue, dan Isolasi RLS `mycaffe_app`)
3. `go run ./cmd/migrate -action=down` (Rollback Teardown)
4. `go run ./cmd/migrate -action=up` (Re-apply)
5. `go run ./cmd/migrate -action=seed` dijalankan 2x berturut-turut (Uji Idempotensi Seed Development)
