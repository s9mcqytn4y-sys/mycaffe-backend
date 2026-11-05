# Frontend Integration Contract (v0.6.0)

Panduan untuk tim frontend (Next.js PWA, Android/iOS) sebelum mulai integrasi. Sumber kebenaran
kontrak adalah [OpenApi.yaml](../Api/OpenApi.yaml); format error dan CRUD ada di
[ApiConventions.md](ApiConventions.md). Dokumen ini memetakan **endpoint ke aplikasi**, **FSM yang
boleh dirender**, dan **celah yang harus ditutup backend**.

## 1. Aturan dasar: backend adalah sumber kebenaran

1. Frontend **tidak menghitung** total, pajak, service, diskon, atau stok. Tampilkan hasil
   `POST /pos/orders/quote` (POS) atau `POST /public/stores/:slug/orders/quote` (tamu). Kirim nilai `quote_fingerprint` dari quote sebagai
   `expected_quote_fingerprint` saat membuat pesanan. Respons `409 QUOTE_CHANGED` berarti harga berubah, jadi
   tampilkan quote baru.
2. Frontend **tidak memutuskan transisi status**. Tombol ditampilkan berdasarkan status dari server
   (tabel FSM di bawah). Server tetap menolak transisi ilegal dengan `409`.
3. **Redirect atau layar sukses bukan bukti pembayaran.** Status `paid` hanya datang dari server, lewat
   WebSocket atau polling pesanan.
4. Setiap POST yang membuat pesanan, ronde, atau pembayaran wajib membawa `idempotency_key` (UUID dari
   klien, disimpan sampai respons diterima). Retry memakai key yang sama.
5. Izin UI diambil dari `GET /auth/staff/me` → `permissions`. Jangan hardcode peran.
6. Uang: integer rupiah. Jangan memakai float. Format tampilan memakai `Intl.NumberFormat('id-ID')`.

## 2. Autentikasi dan realtime

| Langkah | Endpoint | Catatan |
|---------|----------|---------|
| Login staf | `POST /api/v1/auth/staff/login` | `store_slug + email + PIN 6 digit` → access token 15 menit dan refresh cookie HttpOnly |
| Refresh | `POST /api/v1/auth/staff/refresh` | Rotasi; reuse terdeteksi akan mencabut sesi. Panggil sekali per tab (gunakan lock agar tidak paralel). |
| Profil + izin | `GET /api/v1/auth/staff/me` | |
| Logout | `POST /api/v1/auth/staff/logout` | |
| Sesi tamu | `POST /api/v1/public/qr/:token/session` | Dari QR meja; cookie `__Secure-mc_guest` (HttpOnly, path `/api/v1/public`) untuk `/public/guest/*` |
| Tiket WS | `POST /api/v1/pos/ws-ticket` (staf), `POST /api/v1/public/guest/ws-ticket` (tamu) | Tiket sekali pakai, lalu `GET /ws?ticket=…` |
| WebSocket | `GET /ws` | Kanal: `pos`, `kds.bar`, `kds.kitchen`, `owner` (izin `approval.decide`), `guest.<session>`. At-least-once: simpan `seq` tertinggi, abaikan event dengan `seq` ≤ nilai itu, kirim `last_seq` saat reconnect, dan muat ulang lewat REST saat menerima `resync_required` (ApiConventions §6). Batas koneksi menghasilkan `429 WS_CONNECTION_LIMIT`. |

## 3. Endpoint per aplikasi

### POS kasir

- **Daftar dan detail pesanan:** `GET /pos/orders` (filter `status`, `payment_status`, `billing_mode`, `from`/`to`, `shift_id`, `table_id`, `q`; paginasi `limit` + `after`) dan `GET /pos/orders/:id`. Tombol aksi mengikuti `allowed_actions`.

- **Katalog:** `GET /pos/catalog` (ETag).
- **Pesanan:** `POST /pos/orders/quote`, `POST /pos/orders`, `POST /pos/orders/:id/payments`.
- **Tab dan meja:**
  - tab: `POST /pos/tabs`, `POST /pos/tabs/:id/rounds`, `GET /pos/tabs/:id`, `POST /pos/tabs/:id/close` (split bill di body), `POST /pos/tabs/:id/reopen`;
  - meja: `GET /pos/tables?section=`, `POST /pos/tables/:id/sessions`, `POST /pos/table-sessions/:id/close`;
  - pindah/gabung/pisah (v0.9, W5): `POST /pos/table-sessions/:id/move` `{to_table_id}`, `/merge` `{session_id}`, `/split` `{table_id}`. Meja yang digabung muncul di papan dengan `merged_into_table_id` dan sesi serta total tagihan bersama; event `table.moved`, `table.merged`, `table.split` di kanal `pos` berarti papan meja perlu dimuat ulang;
  - timer meja (v0.9, W6): papan memberi `timer_state` (`none`, `ok`, `warning`, `expired`) dan `session.ends_at`; `POST /pos/table-sessions/:id/extend` `{minutes}`. Event `table.timer_warning`, `table.timer_expired`, `table.timer_extended` di kanal `pos`. Timer hanya peringatan: pesanan tetap diterima.
- **Layar antrean TV (v0.9, W7):** owner mendaftarkan layar di `POST /store/display-tokens` `{name}` (token tampil sekali), `GET /store/display-tokens`, `POST /store/display-tokens/:id/revoke`. Aplikasi TV mengirim `Authorization: Display <token>` ke `GET /public/queue` (nomor antrean dan status `queued`/`preparing`/`ready` hari ini) dan `POST /public/queue/ws-ticket` (kanal `queue`, event `queue.updated` `{queue_number, status}`; `served`, `completed`, `cancelled` berarti nomor dihapus dari layar). Origin aplikasi TV harus ada di `ALLOWED_ORIGINS`. Aturan tampilan: [BrandAndProductKnowledge.md](../Product/BrandAndProductKnowledge.md) §5.1.
- **Printer dan agen cetak (v0.9, W8):** owner mengatur printer di `GET/POST /store/printers`, `PATCH /store/printers/:id`, `POST /store/printers/:id/rotate-token` (token agen tampil sekali). Kasir mencetak ulang lewat `POST /pos/orders/:id/reprint` `{kind: ticket|checker|label}`. Agen cetak di LAN kafe memanggil `POST /print-agent/jobs/claim` (200 job `payload_base64` atau 204) lalu `POST /print-agent/jobs/:id/ack` `{status: printed|failed, error}` dengan `Authorization: Printer <token>`. Tiket dibuat otomatis saat pesanan lunas atau ronde dikirim, sekali per tiket KDS baru. Event `print_job.printed` dan `print_job.failed` di kanal `pos`.
- **Struk digital (v0.9, W9):** kasir `POST /pos/orders/:id/receipt/send` dan tamu `POST /public/guest/orders/:id/receipt/send` (cookie tamu + CSRF, hanya pesanan sendiri) dengan `{channel: email|whatsapp|sms, to}` untuk pesanan yang sudah dibayar. Hasil 202 berisi alamat yang disamarkan. WhatsApp menjawab `503 WHATSAPP_UNAVAILABLE` sampai token Meta dipasang; SMS hanya ke nomor terverifikasi (`422 PHONE_NOT_VERIFIED`); batas 3 kirim per pesanan dan 10 per penerima per hari (`429 RECEIPT_SEND_LIMIT`). Tampilkan pilihan email lebih dulu.
- **Shift:**
  - shift: `POST /pos/shifts`, `GET /pos/shifts/current`, `POST /pos/shifts/:id/close`, `GET /pos/shifts/:id/x-report`, `GET /pos/shifts/:id/z-report`;
  - kas: `POST /pos/shifts/movements`.
- **Koreksi:** `POST /pos/orders/:id/void`, `POST /pos/orders/:id/payments/:payment_id/refund`, `POST /pos/products/:id/stock-override`.
- **Struk:** `GET /pos/orders/:id/receipt?kind=&format=json|escpos&width=58|80`, `POST /pos/orders/:id/receipt-link`.

### Multi-payment dan pembulatan (v0.8, V11/V12)

- `POST /pos/orders/:id/payments` menerima `amount` opsional untuk tunai, EDC, dan QRIS statis. Tanpa `amount`, pembayaran melunasi sisa tagihan. Sisa tagihan adalah `split.total_amount - split.paid_amount` dari respons.
- Selama `payment_status = partially_paid`, tampilkan sisa tagihan dan tombol bayar lagi. Pesanan baru masuk dapur setelah lunas.
- QRIS dinamis selalu melunasi sisa tagihan. Selama QRIS menunggu, pembayaran sebagian ditolak dengan `409 SPLIT_PAYMENT_PENDING`.
- Bila `cash_rounding_step` aktif, tunai penutup harus mencakup nominal yang dibulatkan. Tampilkan `rounding_amount` sebagai "Pembulatan", dan hitung kembalian dari respons, bukan di klien.

### Diskon dan compliment (v0.8, PR 8.6)

- `POST /pos/orders/:id/discount {discount_amount | discount_bps, reason}` sebelum pembayaran apa pun. Total baru ada di respons; jangan hitung di klien.
- `422 OWNER_PIN_REQUIRED` di atas batas kasir: minta approval `discount` dengan `params.amount`, lalu ulangi dengan `approval_id`.
- `compliment: true` berarti pesanan sudah lunas dengan total 0 dan langsung masuk dapur.

### Kas outlet (v0.8, V13)

- `GET /pos/outlet-cash` menampilkan saldo, pengeluaran hari ini, sisa tanpa approval, dan 50 mutasi terakhir.
- `POST /pos/outlet-cash/movements` mencatat `top_up` (owner), `expense` (kategori wajib), `to_drawer`, dan `from_drawer`.
- `409 OUTLET_EXPENSE_APPROVAL_REQUIRED` berarti kirim approval `outlet_expense` dengan `params {amount, category}`, lalu ulangi dengan `approval_id`.
- Buka shift tanpa `opening_float` memakai modal awal default toko. X-report kasir menyembunyikan kas yang diharapkan kecuali `cashier_sees_expected_cash` aktif.

### OTP SMS (v0.8, [Sms.md](../Setup/Sms.md))

- **Reset PIN staf:** `POST /auth/staff/pin/forgot-sms` (selalu 202), lalu `POST /auth/staff/pin/reset-sms`. Owner mengatur nomor staf dengan `POST /staff/:id/phone`.
- **Verifikasi pelanggan di POS:** `POST /pos/customers/verify` → `POST /pos/customers/verify/:id/confirm`.
- `503 SMS_UNAVAILABLE` berarti SMS belum aktif; sembunyikan tombolnya.

### KDS

`GET /kds/tickets`, `POST /kds/tickets/:id/transition` (`expected_status` untuk optimistic
concurrency; `409` berarti layar lain sudah mengubah status), `PATCH /catalog/products/:id/availability`.

### PWA tamu (QR meja)

`GET /public/qr/:token`, `POST /public/qr/:token/session`, `GET /public/guest/session`,
`GET /public/stores/:slug/catalog`, `POST /public/stores/:slug/orders/quote`, `GET|POST /public/guest/orders`,
`POST /public/guest/orders/:id/payments`, `GET /public/orders/track/:token`, `GET /public/receipts/:token`.

### Backoffice owner

- **Katalog CRUD:** `/catalog/*`.
- **Media:** `POST /media/uploads` → PUT ke R2 → `POST /media/uploads/:id/complete`.
- **Laporan:** `GET /reports/sales`, `GET /reports/summary` (JSON/CSV).
- **Pengaturan:** `GET|PATCH /store/settings`.
- **Staf:** `GET|POST /staff`, `POST /staff/:id/pin-reset`, `POST /staff/:id/temporary-pin`.
- **Laci kas:** `/pos/drawers`.
- **Meja dan QR:** `/pos/tables*`, `POST /pos/tables/:id/rotate-qr`.

## 4. FSM yang dirender frontend

Konstanta Go sama 1:1 dengan `CHECK` PostgreSQL. Status di luar tabel ini adalah bug server.

### 4.1 `order_status` (persiapan, `internal/domain/order.go`)

| Dari | Ke |
|------|----|
| `pending_payment` | `queued`, `cancelled` |
| `open` (tab) | `queued`, `preparing`, `ready`, `served`, `completed`, `cancelled` |
| `queued` | `preparing`, `cancelled` |
| `preparing` | `ready` |
| `ready` | `served`, `completed` |
| `served` | `completed` |
| `completed`, `cancelled` | terminal |

### 4.2 `payment_status` pesanan (terpisah dari persiapan)

Nilai: `unpaid`, `pending`, `partially_paid`, `paid`, `failed`, `cancelled`, `expired`,
`partially_refunded`, `refunded`, `chargeback`, `partial_chargeback`.

Aturan UI:

- tombol bayar hanya untuk `unpaid`, `failed`, `expired`, dan `partially_paid`;
- tombol refund hanya untuk `paid` dan `partially_refunded`;
- `pending` berarti tunggu event, jangan buat attempt baru.

### 4.3 Payment attempt

`pending`, `authorized` → `paid` | `failed` | `cancelled` | `expired`; `paid` → `partially_refunded` →
`refunded`; `chargeback` dan `partial_chargeback` berasal dari webhook. Event `pending` atau `cancelled`
yang datang terlambat tidak menimpa `paid`.

### 4.4 Lainnya

| Entitas | Status |
|---------|--------|
| Split bill | `unpaid` → `pending` → `partially_paid` → `paid`; `cancelled`, `refunded`. Terkunci setelah pembayaran split pertama. |
| Ronde tab | `queued` → `preparing` → `ready` → `served`; `cancelled` |
| Tiket KDS | `queued` → `preparing` → `ready` → `served`; `cancelled`. Status ronde dan pesanan diturunkan server dari tiket. |
| Shift | `open` → `closed` atau `force_closed` |
| Meja | `available` → `occupied` → `settled` (hitung mundur auto-close) → `available` |
| Stok pesanan | `not_tracked`, `reserved` → `committed` atau `released` |

## 5. Celah backend sebelum integrasi frontend

| # | Celah | Dampak ke frontend | Prioritas |
|---|-------|--------------------|-----------|
| G-1 | ~~Tidak ada daftar dan detail pesanan~~ | **Implemented & Tested (v0.7 PR 2):** `GET /pos/orders` (filter, cursor `after`) dan `GET /pos/orders/:id` (`allowed_actions`) | Selesai |
| G-2 | ~~Tidak ada daftar tab terbuka~~ | **Implemented & Tested:** `GET /pos/orders?billing_mode=open_tab&status=open`; tab yang sudah ditutup dan menunggu bayar memakai `status=pending_payment` | Selesai |
| G-3 | Tidak ada endpoint layar antrean TV | Modul "Queue Display" tidak bisa dibangun | Wajib bila TV ikut demo |
| G-4 | Tidak ada `GET /audit-logs` dan `PATCH /staff/:id` (nonaktif/ganti peran) | Backoffice tidak lengkap | Sebelum go-live |
| G-5 | ~~Remote approval owner (P1-04)~~ | **Implemented & Tested (v0.7 PR 3, [ADR-007](Adr007RemoteOwnerApproval.md)):** kasir `POST /pos/approvals`, owner `GET /approvals` lalu `POST /approvals/:id/approve` dengan PIN, kasir mengirim `approval_id` sebagai pengganti `owner_pin` | Selesai |
| G-6 | Tidak ada perintah buka laci di ESC/POS | Laci dibuka manual | Kecil |
| G-7 | Branding toko (logo, warna, footer struk) belum ada di settings | White-label belum bisa diatur | Sebelum klien kedua |

Untuk mobile native (Android/iOS): kontrak HTTP sama. Refresh token memakai cookie HttpOnly, jadi
klien native memerlukan cookie jar (OkHttp/URLSession). Jika tidak memakai cookie jar, perlu keputusan
kontrak baru (refresh token di body untuk klien native). Itu perubahan auth dan butuh persetujuan.
