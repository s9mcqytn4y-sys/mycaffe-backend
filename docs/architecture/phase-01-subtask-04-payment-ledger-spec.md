# Spesifikasi & Rencana Arsitektur Phase 01 Sub-task 04: Transactional Payment Initiation & Durable Midtrans Webhook Reconciliation

Dokumen ini mendefinisikan spesifikasi teknis, arsitektur database, aturan idempotensi, penanganan *race condition* pembayaran, serta peta jalan pengembangan paralel setelah **PR #4** (*Phase 01 Sub-task 03: Transactional OrderService & Server-Side Pricing Engine*) selesai diverifikasi.

---

## 1. Masalah Inti yang Diselesaikan pada Phase 01 Sub-task 04

Saat ini pada repositori `mycaffe-backend`:
1. Pembuatan pesanan transaksional (`OrderService.CreateOrder`), reservasi stok (`stock_status = 'reserved'`), pelepasan stok saat kedaluwarsa (`CancelOrExpireUnpaidOrder`), dan komit stok saat lunas (`ConfirmOrderPaymentAndCommitStock`) telah **Implemented & Tested**.
2. Klien HTTP Midtrans Core API (`internal/pkg/midtrans/client.go`) dan verifikasi tanda tangan SHA-512 (`VerifySignature`, `ParseGrossAmountIDR`, `MapMidtransStatus`) telah **Implemented & Tested**.
3. Namun, endpoint `POST /api/v1/payments/midtrans/webhook` masih berstatus **Guarded / Fail-Closed** (`503 Service Unavailable` dengan header `Retry-After: 60`) karena lapisan repositori dan servis transaksi untuk tabel `payments`, `payment_events`, dan rekonsiliasi `payment_splits` -> `orders` belum dihubungkan.

**Tujuan Phase 01 Sub-task 04:**
Membangun `PaymentRepository` dan `PaymentService` yang menyimpan setiap percobaan pembayaran (`payments`), menginisiasi transaksi ke Midtrans Core API (`/v2/charge`) atau memproses pembayaran kasir (`cash`, `static_qris`, `edc`), serta memproses webhook Midtrans secara durabel, idempoten, dan tahan terhadap *out-of-order notifications* di PostgreSQL 18.

---

## 2. Tantangan Arsitektur: Routing Webhook Publik di Bawah PostgreSQL 18 RLS

Semua tabel operasional termasuk `orders`, `payment_splits`, `payments`, dan `payment_events` menerapkan `FORCE ROW LEVEL SECURITY` dengan kebijakan:
```sql
USING (store_id = NULLIF(current_setting('app.current_store_id', true), '')::uuid)
```
Ketika Midtrans mengirim HTTP POST ke `/api/v1/payments/midtrans/webhook`, payload JSON Midtrans **hanya** membawa `order_id` (yaitu `provider_order_id` berformat `{ORDER_NUMBER}-S{SPLIT}-A{ATTEMPT}`), `transaction_id`, `status_code`, `gross_amount`, dan `signature_key`. Payload Midtrans **tidak** membawa `store_id` UUID secara langsung.

### Solusi Desain: Lookup Function `SECURITY DEFINER` Terbatas atau Tabel Routing Minimal
Agar proses rekonsiliasi webhook tetap berjalan di dalam `database.WithTenantTx(ctx, db, resolvedStoreID, ...)`:
1. Pada migrasi aditif Sub-task 04 (`db/migrations/20261031000004_add_payment_webhook_lookup.up.sql`), dibuat fungsi PostgreSQL `resolve_store_id_by_provider_order_id(p_provider_order_id VARCHAR)` bertipe `SECURITY DEFINER` (dimiliki oleh `mycaffe_migrator`, `SET search_path = public`) yang **hanya** mengembalikan `store_id UUID` dari tabel `payments` berdasarkan `provider_order_id` yang tepat:
   ```sql
   CREATE OR REPLACE FUNCTION resolve_store_id_by_provider_order_id(p_provider_order_id VARCHAR)
   RETURNS UUID
   LANGUAGE sql
   STABLE
   SECURITY DEFINER
   SET search_path = public
   AS $$
       SELECT store_id
       FROM payments
       WHERE provider_order_id = p_provider_order_id
       LIMIT 1;
   $$;
   GRANT EXECUTE ON FUNCTION resolve_store_id_by_provider_order_id(VARCHAR) TO mycaffe_app;
   ```
2. Fungsi ini tidak pernah membocorkan isi baris `payments` atau nominal uang; ia hanya memetakan `provider_order_id -> store_id` setelah tanda tangan SHA-512 Midtrans diverifikasi valid.
3. Begitu `store_id` diperoleh, `PaymentService.ProcessMidtransWebhook` membuka transaksi tenant resmi `database.WithTenantTx(ctx, db, storeID, ...)` sehingga seluruh pembacaan dan pembaruan pada `payment_events`, `payments`, `payment_splits`, `orders`, `products`, dan `outbox_events` tetap berada di bawah perlindungan penuh RLS `store_id`.

---

## 3. Spesifikasi Komponen Phase 01 Sub-task 04

### 3.1 Format `provider_order_id` Deterministik
Setiap baris pada tabel `payments` mewakili satu percobaan pembayaran (*payment attempt*) atas satu `payment_split`:
$$\text{provider\_order\_id} = \texttt{\{OrderNumber\}-S\{SplitNumber\}-A\{AttemptNumber\}}$$
Contoh: `ORD-20261030-0001-S1-A1`.
- Jika pelanggan gagal atau membatalkan pembayaran pertama lalu mencoba kembali pada split yang sama, sistem membuat baris `payments` baru dengan `attempt_number = 2` (`ORD-20261030-0001-S1-A2`) tanpa menimpa histori percobaan pertama.

### 3.2 Alur `PaymentService.InitiatePayment`
1. **Validasi Input & Idempotensi:**
   - Wajib menyertakan `idempotency_key` non-kosong.
   - Kunci baris `orders` (`FOR UPDATE`) dan `payment_splits` (`FOR UPDATE`) di dalam `WithTenantTx`.
   - Jika sudah ada baris `payments` dengan `(store_id, idempotency_key)` yang sama, kembalikan respons pembayaran tersebut secara idempoten.
   - Tolak jika `split.status == 'paid'` atau `order.status == 'cancelled'`.
   - Pastikan `payment_method` termasuk dalam daftar `stores.enabled_payment_methods`.
2. **Jalur A: Metode Kasir Langsung (`cash`, `static_qris`, `edc`):**
   - Wajib memiliki `ActorUserID` yang terverifikasi sebagai staf aktif (`owner` atau `cashier`) melalui `VerifyActiveStaffActor`.
   - Untuk `cash`: validasi `cash_tendered_amount >= split.total_amount`, hitung `change_given_amount = cash_tendered_amount - split.total_amount`.
   - Simpan baris `payments` dengan `status = 'paid'`, `paid_at = now()`, `received_by_user_id = ActorUserID`.
   - Perbarui `payment_splits` (`paid_amount = total_amount`, `status = 'paid'`).
   - Panggil logika komit pesanan (`orders.paid_amount += split.total_amount`; jika seluruh split lunas: `orders.payment_status = 'paid'`, `orders.status = 'queued'`, `orders.stock_status = 'committed'`, `orders.expires_at = NULL`).
   - Tulis `audit_logs` dan `outbox_events` (`payment.succeeded` & `order.paid`) dalam satu transaksi ACID yang sama.
3. **Jalur B: Metode Gateway Midtrans (`qris`, `gopay`, `shopeepay`, `bank_transfer`):**
   - Fase 1 (DB Tx 1): Hitung `attempt_number` berikutnya pada `split_id`, simpan baris `payments` berstatus `pending` dengan `amount = split.total_amount - split.paid_amount`, dan perbarui `orders.payment_status = 'pending'`.
   - Fase 2 (Network Call di luar DB lock): Panggil `midtransClient.ChargeCoreAPI(ChargeRequest{OrderID: providerOrderID, GrossAmount: amount, PaymentMethod: method, Bank: bank})`.
   - Fase 3 (DB Tx 2): Simpan `provider_transaction_id`, `qr_string`, `qr_code_url`, `deeplink_url`, `va_number`, `expires_at`, dan `raw_gateway_response` pada baris `payments`, serta tulis `outbox_events` (`payment.initiated`). Jika panggilan ke Midtrans gagal (4xx/5xx permanen), tandai percobaan `payments` tersebut sebagai `failed` agar pelanggan dapat membuat attempt baru (`A2`).

### 3.3 Alur `PaymentService.ProcessMidtransWebhook` (Rekonsiliasi Durabel & Idempoten)
1. **Verifikasi Kriptografi & Format (Sebelum Menyentuh DB):**
   - Batasi ukuran body `<= 64 KB`.
   - Verifikasi `SHA512(order_id + status_code + gross_amount + server_key)` secara *constant-time* (`subtle.ConstantTimeCompare`).
   - Validasi `currency == "IDR"` dan parse `gross_amount` sebagai `int64` Rupiah bulat (`ParseGrossAmountIDR`).
   - Petakan `(transaction_status, fraud_status)` ke `domain.PaymentStatus` (`MapMidtransStatus`).
2. **Resolusi Tenant & Transaksi ACID (`WithTenantTx`):**
   - Panggil `resolve_store_id_by_provider_order_id(notif.OrderID)`. Jika tidak ditemukan, kembalikan `404 Not Found` (atau log peringatan).
   - Di dalam `WithTenantTx(ctx, db, storeID, ...)`:
     1. Kunci baris `payments` (`FOR UPDATE`), `payment_splits` (`FOR UPDATE`), dan `orders` (`FOR UPDATE`).
     2. **Verifikasi Kecocokan Nominal:** Pastikan `grossAmountIDR == payment.Amount`. Jika tidak cocok, catat anomali keamanan di `payment_events` dan tolak pembaruan status (`400 Bad Request`).
     3. **Idempotent Event Log (`payment_events`):**
        Hitung `idempotency_key` webhook: `SHA256(provider_order_id + ":" + transaction_id + ":" + transaction_status + ":" + status_code + ":" + gross_amount)`.
        Masukkan ke `payment_events` (`ON CONFLICT (store_id, idempotency_key) DO NOTHING`). Jika event yang persis sama sudah pernah diproses dan status `payments` sudah sesuai, kembalikan `200 OK` secara idempoten.
     4. **Aturan Monotonic State Machine (Anti Out-of-Order Downgrade):**
        - Jika `payment.Status == 'paid'`, abaikan notifikasi `pending`, `expire`, atau `cancel` yang datang terlambat (*out-of-order*), tetapi tetap izinkan transisi pasca-settlement yang sah (`refunded`, `partially_refunded`, `chargeback`, `partial_chargeback`).
        - Jika `mappedStatus == 'paid'`:
          - Perbarui `payments.status = 'paid'`, `paid_at = now()`.
          - Perbarui `payment_splits` (`paid_amount = total_amount`, `status = 'paid'`).
          - Jalankan rekonsiliasi pesanan (`ConfirmOrderPaymentAndCommitStock`): jika pesanan sebelumnya sempat `expired` (`stock_status = 'released'`) dan pembayaran terlambat masuk, lakukan pemotongan ulang stok secara atomik; apabila stok ternyata sudah habis dibeli pelanggan lain (`ErrLatePaymentStockExhausted`), tandai pesanan dan tulis `audit_logs` + `outbox_events` (`payment.late_settlement_stock_exhausted`) untuk pengembalian dana kasir.
        - Jika `mappedStatus == 'expired'` atau `'cancelled'` atau `'failed'`:
          - Perbarui `payments.status = mappedStatus`.
          - Jika tidak ada attempt lain yang `paid` dan batas waktu pesanan habis, panggil `CancelOrExpireUnpaidOrder` agar stok yang `reserved` dikembalikan tepat satu kali (`stock_status = 'released'`).

---

## 4. Peta Jalan Eksekusi & Pembagian Workstream Paralel Berikutnya

Setelah **PR #4** direview dan di-merge ke `staging`, pengembangan dilanjutkan dengan urutan cabang dan kualitas gate berikut:

| Tahap | Branch Target | Cakupan Deliverable | Kriteria Selesai (Acceptance Gate) |
| :--- | :--- | :--- | :--- |
| **Phase 01 Sub-task 04** | `feature/phase-01-subtask-04-payment-ledger` | Migrasi `000004` (`resolve_store_id_by_provider_order_id`), `PaymentRepository`, `PaymentService` (`InitiatePayment` + `ProcessMidtransWebhook`), aktivasi HTTP `200 OK` pada `MidtransHandler.HandleWebhook` dengan penyimpanan durabel, dan integration test terhadap PostgreSQL 18. | Webhook Midtrans tersimpan ke `payment_events` & `payments`, mengubah `orders` ke `paid` + `queued` + `committed`, melepaskan stok saat `expire`, dan lulus 8 skenario uji konkurensi/idempotensi di CI. |
| **Phase 02 Sub-task 01** | `feature/phase-02-subtask-01-auth-and-table-qr` | Middleware autentikasi JWT staf (`owner`, `cashier`, `barista`, `kitchen`), penandatanganan & verifikasi HMAC token QR meja (`X-Table-QR-Token`), dan pembukaan endpoint publik `GET /api/v1/public/stores/:slug/catalog` serta `POST /api/v1/orders`. | Endpoint `POST /api/v1/orders` di `staging`/`production` tidak lagi mengembalikan `503`, melainkan memvalidasi identitas `store_id` dari token terverifikasi dan memanggil `OrderService.CreateOrder`. |
| **Phase 02 Sub-task 02** | `feature/phase-02-subtask-02-kds-and-outbox-relay` | Outbox relay worker (`outbox_events -> realtime.Hub`), endpoint transisi KDS (`queued -> preparing -> ready -> served`), dan tiket sesi WebSocket berumur pendek. | Layar KDS Barista/Dapur, TV Queue Display, dan halaman pelacakan pelanggan menerima pembaruan status pesanan secara real-time per `store_id`. |
| **Phase 03 Sub-task 01** | `feature/phase-03-subtask-01-shifts-and-analytics` | Manajemen shift (`single_auto` & `multi_shift`), pencatatan kas laci, void/refund dengan `audit_logs`, dan endpoint laporan analitik pemilik usaha. | Rekonsiliasi kas harian, laporan MDR QRIS, dan laporan pajak PB1/service charge tervalidasi dengan angka Rupiah bulat. |
