# Spesifikasi & Rencana Arsitektur Phase 01 Sub-task 04: Transactional Payment Initiation & Durable Midtrans Webhook Reconciliation

Dokumen ini mendefinisikan spesifikasi teknis, arsitektur database, aturan idempotensi, penanganan *race condition* pembayaran, serta peta jalan pengembangan paralel setelah **PR #4** (*Phase 01 Sub-task 03: Transactional OrderService & Server-Side Pricing Engine*) selesai diverifikasi.

---

## 1. Masalah Inti yang Diselesaikan pada Phase 01 Sub-task 04

Status terkini pada repositori `mycaffe-backend` (`feature/phase-01-subtask-04-payment-ledger` / **PR #5**):
1. Pembuatan pesanan transaksional (`OrderService.CreateOrder`), reservasi stok (`stock_status = 'reserved'`), pelepasan stok saat kedaluwarsa (`CancelOrExpireUnpaidOrder`), dan komit stok saat lunas (`ConfirmOrderPaymentAndCommitStock`) telah **Implemented & Tested** (merged ke `staging` via **PR #4**).
2. Klien HTTP Midtrans Core API (`internal/pkg/midtrans/midtrans.go`) dengan `ChargeCoreAPI`, `CheckTransaction`, `SetPaymentIdempotencyKey`, dan verifikasi tanda tangan SHA-512 (`VerifySignature`, `ParseGrossAmountIDR`, `MapMidtransStatus`) telah **Implemented & Tested**.
3. Migrasi `20261031000004_harden_payment_idempotency_and_webhook_inbox.up.sql`, `PaymentRepository`, `PaymentService` (`InitiatePayment`, `ProcessMidtransWebhook`, `ExpireOverdueOrders`, `ReconcileStalePendingPayments`, `StartReconciliationWorker`), serta aktivasi HTTP `200 OK` pada `POST /api/v1/payments/midtrans/webhook` dan endpoint `POST /api/v1/orders/:id/payments/initiate` (fail-closed `503` di `staging`/`production` hingga Phase 02 Auth) telah **Implemented & Tested**.

**Tujuan Phase 01 Sub-task 04:**
Membangun `PaymentRepository` dan `PaymentService` yang menyimpan setiap percobaan pembayaran (`payments`), menginisiasi transaksi ke Midtrans Core API (`/v2/charge`) atau memproses pembayaran kasir (`cash`, `static_qris`, `edc`), serta memproses webhook Midtrans secara durabel, idempoten, dan tahan terhadap *out-of-order notifications* di PostgreSQL 18.

---

## 2. Tantangan Arsitektur: Resolusi Tenant Webhook di Bawah RLS

Schema awal sudah menyediakan tabel `payments`, tabel durable inbox `payment_notification_logs`, dan fungsi `resolve_payment_webhook_context(VARCHAR)`. Jangan membuat fungsi resolver kedua dengan nama atau kontrak yang berbeda.

Fungsi yang sudah ada mengembalikan konteks terbatas:
`store_id`, `order_id`, `payment_id`, `expected_amount`, dan `current_status`.
Fungsi tersebut dibuat sebagai `SECURITY DEFINER`, dengan hak akses yang harus diperketat melalui migration Sub-task 04:

- `REVOKE ALL ON FUNCTION resolve_payment_webhook_context(VARCHAR) FROM PUBLIC`.
- Berikan `EXECUTE` hanya kepada `mycaffe_app`.
- Pastikan pemilik fungsi adalah role migrator yang terkontrol dan schema pada `search_path` tidak dapat ditulis oleh role tidak tepercaya.
- Verifikasi perilaku fungsi dengan login langsung sebagai `mycaffe_app`, termasuk kasus provider order ID tidak ditemukan dan tidak ada kebocoran data tenant.

**Koreksi penting tentang RLS:** migration awal mengaktifkan `ENABLE ROW LEVEL SECURITY`; migration yang saat ini ada tidak menerapkan `FORCE ROW LEVEL SECURITY`. Role aplikasi `mycaffe_app` bukan pemilik tabel dan tidak memiliki `BYPASSRLS`, sehingga query aplikasi tetap tunduk pada policy tenant. Fungsi `SECURITY DEFINER` menggunakan privilege pemiliknya untuk lookup lintas tenant sebelum transaksi tenant dibuka. Dokumentasi dan acceptance test harus mencerminkan konfigurasi aktual ini, bukan mengklaim `FORCE RLS`.

Alur webhook yang benar:
1. Validasi body, signature SHA-512, currency, nominal, dan status sebelum lookup.
2. Panggil `resolve_payment_webhook_context(notif.OrderID)` menggunakan koneksi aplikasi. Fungsi hanya boleh dipanggil setelah signature valid.
3. Jika tidak ditemukan, jangan membuat tenant context dari payload; catat peringatan tanpa membocorkan apakah ID lain valid.
4. Setelah memperoleh `store_id`, jalankan satu `database.WithTenantTx` untuk mengunci payment, split, dan order serta menyimpan inbox event, status ledger, perubahan order, dan outbox secara atomik.

Untuk memastikan resolver tidak ambigu pada satu akun Midtrans yang dipakai banyak tenant, `provider_order_id` harus unik secara global per provider, bukan hanya unik di dalam `store_id`. Gunakan ID payment UUID yang dibuat server sebagai provider order ID. Nomor order yang ramah manusia tetap menjadi identitas tampilan dan tidak digunakan sebagai kunci gateway.

## 3. Spesifikasi Komponen Phase 01 Sub-task 04

### 3.1 Identitas Provider dan Idempotensi

Setiap payment attempt memiliki `payments.id` UUID yang dibuat server. Gunakan UUID tersebut sebagai `provider_order_id` untuk Midtrans. UUID 36 karakter memberikan keunikan lintas tenant yang memakai akun gateway bersama, tidak bergantung pada sequence order per toko, dan tetap stabil untuk retry pada attempt yang sama.

- `payments.idempotency_key` adalah idempotency key dari klien, unik per toko.
- Tambahkan `payments.request_hash` untuk menolak pemakaian ulang idempotency key dengan order, split, nominal, metode, atau bank yang berbeda.
- `provider_order_id = payments.id.String()`; jangan memakai `order_number` sebagai ID gateway.
- Midtrans Go SDK v1.3.8 menyediakan `SetPaymentIdempotencyKey` dengan batas 36 karakter. Gunakan UUID payment yang sama sebagai header `Idempotency-Key` pada setiap retry attempt tersebut.
- Sebelum mengulang charge setelah timeout atau respons ambigu, panggil Get Status API berdasarkan `provider_order_id`. Jangan membuat attempt baru sebelum status attempt lama direkonsiliasi.
- Idempotency provider tidak menggantikan idempotensi aplikasi. Kedua lapisan harus diuji terpisah.

### 3.2 Alur `PaymentService.InitiatePayment`

1. **Validasi dan idempotensi di dalam transaksi tenant**
   - Wajib menerima `idempotency_key` non-kosong dan menghitung canonical `request_hash`.
   - Kunci order dan split dengan `FOR UPDATE`.
   - Jika idempotency key sudah ada dan hash sama, kembalikan attempt yang sama; jika hash berbeda, kembalikan konflik.
   - Tolak split yang sudah lunas, order yang dibatalkan/kedaluwarsa, metode yang tidak diaktifkan toko, atau jumlah yang tidak sama dengan sisa tagihan.
   - Phase 01 hanya mendukung satu split penuh dari `OrderService.CreateOrder`. Split bill parsial tetap nonaktif sampai state machine dan alokasi pembayaran parsial disetujui.

2. **Jalur kasir langsung**
   - `cash`, `static_qris`, dan `edc` wajib memiliki `ActorUserID` yang diverifikasi sebagai staf aktif `owner` atau `cashier`.
   - Untuk `cash`, validasi `cash_tendered_amount >= amount`; simpan `cash_tendered_amount`, `change_given_amount`, dan `verified_by_user_id`.
   - Untuk `static_qris` dan `edc`, jangan menganggap pembayaran lunas dari input pelanggan. Status hanya berubah menjadi paid setelah kasir memverifikasi referensi transaksi dan nominal.
   - Simpan payment, update split/order, audit log, dan outbox dalam satu transaksi ACID.

3. **Jalur gateway Midtrans**
   - DB Tx 1: buat payment attempt berstatus `pending`, UUID provider order ID, request hash, amount, method, split ID, dan attempt number; set status split/order menjadi pending.
   - Network call: panggil Midtrans di luar transaksi database menggunakan idempotency key UUID payment. Gunakan timeout HTTP terbatas. Jangan menahan row lock selama panggilan jaringan.
   - DB Tx 2: simpan transaction ID, QR string, redirect/deeplink, VA number, biller code, payment code, expiry, serta status respons yang sudah divalidasi.
   - Jika panggilan berakhir timeout atau error jaringan ambigu, jangan langsung menandai attempt failed. Lakukan Get Status API dan simpan status sebagai perlu rekonsiliasi bila provider belum dapat dipastikan.
   - Hanya error provider yang pasti permanen yang boleh mengubah attempt menjadi failed. Retry untuk attempt yang sama memakai provider order ID dan idempotency key yang sama.

### 3.3 Alur `PaymentService.ProcessMidtransWebhook`

1. **Validasi sebelum DB**
   - Batasi body hingga 64 KB.
   - Verifikasi `SHA512(order_id + status_code + gross_amount + server_key)` dengan `subtle.ConstantTimeCompare`.
   - Wajib validasi currency `IDR`, nominal integer Rupiah, dan `status_code == "200"` untuk status sukses.
   - Untuk `capture`/ `settlement`, jika `fraud_status` disediakan, hanya `accept` yang boleh dianggap sukses. `challenge` tidak boleh dipetakan menjadi paid.
   - Status yang tidak didukung tidak boleh diam-diam menjadi pending atau paid.

2. **Resolusi dan penguncian tenant**
   - Gunakan fungsi existing `resolve_payment_webhook_context(VARCHAR)` setelah signature valid.
   - Dalam `WithTenantTx`, kunci payment, split, dan order. Pastikan `provider_order_id`, `store_id`, `payment_id`, dan `gross_amount` cocok dengan record tersimpan.

3. **Durable inbox dan idempotensi event**
   - Gunakan tabel existing `payment_notification_logs`, bukan membuat tabel `payment_notification_logs` kedua.
   - Migration menambah `provider_order_id` dan `event_hash`; hash mencakup provider order ID, transaction ID, status code, transaction status, fraud status, dan gross amount.
   - Insert event dengan `ON CONFLICT (store_id, event_hash) DO NOTHING`. Event duplikat yang sudah diproses harus mendapat HTTP 200 tanpa mengulang efek bisnis.
   - Payload yang disimpan harus disanitasi; jangan menyimpan signature key sebagai bagian dari raw payload.

4. **State transition dan rekonsiliasi**
   - Jangan menurunkan status paid akibat notifikasi pending/expire/cancel yang terlambat.
   - Jika notifikasi tampak tidak berurutan atau berpotensi menurunkan status, panggil Midtrans Get Status API dan gunakan status provider terkini sebelum mengambil keputusan.
   - Untuk paid, update payment attempt dan split; karena split bill parsial belum aktif, Phase 01 hanya mengonfirmasi satu split penuh.
   - Update order dan stock dalam transaksi yang sama. Jika late settlement datang setelah stok dilepas dan stok tidak lagi tersedia, payment tetap dicatat paid; rollback hanya bagian komit stok dengan savepoint, tandai order sebagai perlu penanganan/refund, dan tulis outbox event anomali. Jangan rollback bukti penerimaan uang.
   - Untuk failed/cancelled/expired, lepaskan reservasi hanya setelah deadline order benar-benar lewat dan hanya sekali. Kegagalan satu attempt sebelum deadline tidak boleh otomatis membatalkan order atau melepas stok.

5. **Respons webhook**
   - HTTP 200 setelah event valid berhasil disimpan secara durable, termasuk anomali nominal yang sudah dikarantina dan di-alert.
   - HTTP 503 + `Retry-After` untuk kegagalan database/transien sebelum commit.
   - HTTP 400/401 hanya untuk payload/signature yang tidak valid; jangan gunakan 4xx untuk kegagalan internal yang seharusnya di-retry provider.

### 3.4 Pekerjaan yang harus disediakan sebelum production

- Worker terjadwal untuk mengeksekusi `CancelOrExpireUnpaidOrder` pada reservasi yang melewati `orders.expires_at`. Kolom expiry dan method service tanpa worker tidak akan melepaskan stok secara otomatis.
- Worker rekonsiliasi untuk payment attempt yang charge-nya berakhir ambigu, dan untuk notifikasi yang gagal diproses.
- Alert untuk amount mismatch, provider status tidak dikenal, late settlement, dan backlog outbox.
- Refund workflow dan chargeback reconciliation tetap menjadi scope terpisah; jangan menyatakan sudah didukung hanya karena enum status sudah tersedia.

## 4. Peta Jalan Eksekusi & Pembagian Workstream Paralel Berikutnya

Setelah **PR #4** direview dan di-merge ke `staging`, pengembangan dilanjutkan dengan urutan cabang dan kualitas gate berikut:

| Tahap | Branch Target | Cakupan Deliverable | Kriteria Selesai (Acceptance Gate) |
| :--- | :--- | :--- | :--- |
| **Phase 01 Sub-task 04** | `feature/phase-01-subtask-04-payment-ledger` | Migrasi `000004` (`resolve_payment_webhook_context`), `PaymentRepository`, `PaymentService` (`InitiatePayment` + `ProcessMidtransWebhook`), aktivasi HTTP `200 OK` pada `MidtransHandler.HandleWebhook` dengan penyimpanan durabel, dan integration test terhadap PostgreSQL 18. | Webhook Midtrans tersimpan ke `payment_notification_logs` & `payments`, mengubah `orders` ke `paid` + `queued` + `committed`, melepaskan stok saat `expire`, dan lulus 8 skenario uji konkurensi/idempotensi di CI. |
| **Phase 02 Sub-task 01** | `feature/phase-02-subtask-01-auth-and-table-qr` | Middleware autentikasi JWT staf (`owner`, `cashier`, `barista`, `kitchen`), penandatanganan & verifikasi HMAC token QR meja (`X-Table-QR-Token`), dan pembukaan endpoint publik `GET /api/v1/public/stores/:slug/catalog` serta `POST /api/v1/orders`. | Endpoint `POST /api/v1/orders` di `staging`/`production` tidak lagi mengembalikan `503`, melainkan memvalidasi identitas `store_id` dari token terverifikasi dan memanggil `OrderService.CreateOrder`. |
| **Phase 02 Sub-task 02** | `feature/phase-02-subtask-02-kds-and-outbox-relay` | Outbox relay worker (`outbox_events -> realtime.Hub`), endpoint transisi KDS (`queued -> preparing -> ready -> served`), dan tiket sesi WebSocket berumur pendek. | Layar KDS Barista/Dapur, TV Queue Display, dan halaman pelacakan pelanggan menerima pembaruan status pesanan secara real-time per `store_id`. |
| **Phase 03 Sub-task 01** | `feature/phase-03-subtask-01-shifts-and-analytics` | Manajemen shift (`single_auto` & `multi_shift`), pencatatan kas laci, void/refund dengan `audit_logs`, dan endpoint laporan analitik pemilik usaha. | Rekonsiliasi kas harian, laporan MDR QRIS, dan laporan pajak PB1/service charge tervalidasi dengan angka Rupiah bulat. |
