# Iteration 4d: POS Void, Refund, and Stock Override Specification

## 1. Executive Summary & Product Scope

Dokumen ini mendefinisikan spesifikasi teknis dan arsitektur untuk **Iteration 4d: POS Void, Refund, dan Stock Override** pada MyCaffe Core POS. Sesuai keputusan Owner **O1** dan **O4** pada [docs/architecture/decision-register.md](decision-register.md), seluruh tindakan operasional yang mengubah status pesanan, membatalkan transaksi keuangan, mengembalikan uang kas, atau menyesuaikan kuantitas stok fisik wajib diaudit ketat secara *append-only* (`audit_logs`) dan dilindungi mekanisme otorisasi PIN kasir vs PIN owner berdasarkan ambang batas (*threshold*).

---

## 2. Ratified Decisions & Security Rules

| Keputusan | Domain | Ketentuan Teknis & Otorisasi |
|---|---|---|
| **O1** | **Void Order** | Pembatalan pesanan yang **belum lunas** (`payment_status = 'unpaid'` atau status pesanan `open` / `pending_payment`).<br>1. Kasir memasukkan PIN miliknya sendiri jika total order $\le$ `void_refund_owner_threshold` (default Rp 100.000).<br>2. Wajib menyertakan PIN Owner (`owner_pin`) jika total order $>$ threshold toko.<br>3. Alasan pembatalan (`reason`) wajib diisi.<br>4. Seluruh stok item yang sebelumnya direservasi otomatis dikembalikan ke inventaris (100% restock).<br>5. Status pesanan berubah menjadi `cancelled`. |
| **O1** | **Refund Payment** | Pengembalian dana untuk transaksi yang **sudah lunas** (`payment_status = 'paid'` atau split bersangkutan berstatus `paid`).<br>1. Otorisasi PIN kasir (jika $\le$ threshold) atau PIN Owner (jika $>$ threshold). Alasan refund wajib diisi.<br>2. **Metode Tunai (Cash)**: Kasir wajib memiliki shift aktif di laci kas (`shifts.status = 'open'`). Dana kas dikeluarkan dari laci dan dicatat sebagai pergerakan kas `cash_movements` bertipe `refund`. Transaksi ditolak jika saldo kas di laci tidak mencukupi (`expected_cash < refund_amount`).<br>3. **Metode Non-Tunai / Gateway**: Dicatat pada ledger pembayaran sebagai pengembalian dana.<br>4. Opsi `restore_stock` (boolean, default `true`): jika `true`, stok item dikembalikan ke inventaris; jika `false`, stok dicatat sebagai *waste/loss*.<br>5. Mendukung refund per pembayaran/split (`split_id`/`payment_id`) atau full order refund. Status payment menjadi `refunded`, status split menjadi `refunded`, dan status pesanan menjadi `refunded` jika seluruh transaksi telah dibatalkan. |
| **O4** | **Stock Override** | Penyesuaian manual kuantitas stok produk (`products.stock_quantity`) di luar alur transaksi penjualan reguler.<br>1. **Setiap** penyesuaian stok manual WAJIB diverifikasi dengan PIN Owner (`owner_pin`), tanpa memandang nilai stok.<br>2. Alasan penyesuaian (`reason`) wajib diisi (misal: *opname fisik mingguan*, *tumpah*, *kadaluarsa*).<br>3. Kuantitas stok baru harus berupa integer non-negatif ($\ge 0$).<br>4. Dicatat ke `audit_logs` dengan aksi `'STOCK_OVERRIDE'` memuat `previous_stock`, `new_stock`, dan `delta`. |

---

## 3. Database & Schema Alignment

### 3.1 Migration `20261101000013_void_refund_stock_override.up.sql`

- Memastikan constraint `chk_audit_logs_action` mendukung:
  `'VOID_ORDER', 'REFUND', 'STOCK_OVERRIDE'` (sudah ada sejak migrasi awal).
- Memastikan constraint `chk_cash_movements_type` mendukung `'refund'` (sudah ada di migrasi 10).
- Memastikan status pesanan mendukung `'cancelled'` dan status pembayaran mendukung `'refunded'` (sudah ada di migrasi 01).
- Indexing pelengkap jika diperlukan untuk pencarian audit log per reference/actor.

---

## 4. API Endpoints Contract

### 4.1 Void Order

- **Route**: `POST /pos/orders/:id/void`
- **RBAC**: `RequirePermission(rbac.OrderVoid)`
- **Request Body**:

  ```json
  {
    "reason": "Pelanggan salah meja dan membatalkan pesanan sebelum dibuat",
    "pin": "123456",
    "owner_pin": "482913"
  }
  ```

- **Responses**:
  - `200 OK`: `{"order": {...}, "message": "Pesanan berhasil dibatalkan"}`
  - `400 BAD_REQUEST`: Reason kosong atau order sudah berstatus lunas / cancelled.
  - `401 UNAUTHORIZED`: PIN Kasir atau PIN Owner salah.
  - `403 FORBIDDEN`: Otorisasi peran tidak mencukupi.
  - `422 UNPROCESSABLE_ENTITY`: Total order melebihi threshold toko namun `owner_pin` tidak disertakan.

### 4.2 Refund Payment / Split

- **Route**: `POST /pos/orders/:id/payments/:payment_id/refund`
- **RBAC**: `RequirePermission(rbac.PaymentRefund)`
- **Request Body**:

  ```json
  {
    "reason": "Kopi tumpah oleh staf kasir, pelanggan meminta refund tunai",
    "pin": "123456",
    "owner_pin": "482913",
    "restore_stock": false
  }
  ```

- **Responses**:
  - `200 OK`: `{"payment": {...}, "order": {...}, "open_drawer": true}`
  - `409 SHIFT_CONFLICT`: Tidak ada shift kasir aktif untuk refund tunai, atau kas di laci tidak mencukupi.
  - `422 UNPROCESSABLE_ENTITY`: Melebihi threshold tanpa `owner_pin`.

### 4.3 Stock Override

- **Route**: `POST /pos/products/:id/stock-override`
- **RBAC**: `RequirePermission(rbac.InventoryStockOverride)`
- **Request Body**:

  ```json
  {
    "new_stock": 25,
    "reason": "Stock opname fisik harian penutupan",
    "owner_pin": "482913"
  }
  ```

- **Responses**:
  - `200 OK`: `{"product": {...}, "previous_stock": 18, "new_stock": 25, "delta": 7}`
  - `401 UNAUTHORIZED`: PIN Owner salah.
  - `422 UNPROCESSABLE_ENTITY`: Kuantitas stok negatif atau reason kosong.

---

## 5. Verification Plan

1. **Unit & Service Tests**:
   - Uji batas threshold toko (`void_refund_owner_threshold`): Kasir PIN valid $\le$ threshold; gagal jika $>$ threshold tanpa Owner PIN.
   - Uji saldo laci kas saat refund tunai: menolak refund jika `counted_cash < refund_amount`.
   - Uji opsi `restore_stock = true` vs `restore_stock = false`.
   - Uji stock override wajib Owner PIN untuk setiap modifikasi stok.
2. **Integration Test Suite**:
   - `internal/handler/void_refund_stock_integration_test.go` diuji langsung pada PostgreSQL 18 container lokal (`dev.ps1 test-int` dengan `-race -count=1`).
