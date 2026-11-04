# Evaluasi Operasional POS End-to-End & Backlog

Disusun 2026-10-31 setelah wawancara owner (keputusan E1–E5, T1–T2, C1–C3, H1, R1, N1, S1 di
[DecisionRegister.md](../Architecture/DecisionRegister.md)). Status memakai empat kategori `AGENTS.md`.

## 1. Review sistem autentikasi

| Area | Kondisi sekarang | Status |
| ------ | ------------------ | -------- |
| Login staf | `store_slug + email + PIN 6 digit`, argon2id, pesan gagal generik | Implemented & Tested |
| PIN | **Tepat 6 digit angka**; ditolak bila semua sama, berurutan (`123456`, `654321`), pola tahun (`198519`), atau ada di denylist | Implemented & Tested |
| Email staf | Di-trim, huruf kecil, alamat RFC 5322 tanpa nama tampilan, ≤255 karakter, unik per toko tanpa peduli huruf besar/kecil | Implemented & Tested |
| Password | **Tidak dipakai** (keputusan E1). Kompensasinya lockout 5×/15 menit, limit per-IP, limit Nginx, dan email ke owner saat akun terkunci | Implemented & Tested |
| Aktivasi & reset PIN | Undangan email (48 jam), lupa PIN (30 menit), PIN sementara dari owner yang wajib diganti, link sekali pakai, hash-only | Implemented & Tested |
| Sesi | Access 15 menit, refresh berotasi, maksimal 3 sesi, idle 2 jam, reuse → sesi dicabut, race antar-tab → 409 | Implemented & Tested |
| RBAC | ✅ 02.01-C: matriks izin `internal/pkg/rbac`, `RequirePermission`, `/me.permissions`, identitas dari token di rute POS, `store_settings` (ambang O1/O4/C1, toggle C3). PIN owner untuk ambang O1/O4 diterapkan saat fitur void/refund/diskon dibuat; dan pencabutan `store_id`/`actor_user_id` dari body | **Planned (02.01-C)** |
| Risiko tersisa | PIN 6 digit = 10⁶ kombinasi. Penyerang yang tahu email + kode toko bisa sengaja mengunci akun (DoS terarah); mitigasinya unlock oleh owner dan notifikasi email | Accepted risk |

## 2. Meja, kunjungan meja, group order, split bill

**Kondisi sekarang:** `orders.table_number` hanya teks; belum ada tabel `tables`, kunjungan meja, maupun
pengelompokan order per meja. Status order (`pending_payment → queued → preparing → ready → served →
completed`) terpisah dari status pembayaran, dan pemisahan ini sudah benar.

**Desain yang disetujui (T1, T2):**

```text
tables (store_id, id, label, area, seats, qr_version, qr_ordering_enabled, is_active)
table_sessions (store_id, id, table_id, status, opened_at, opened_by, closed_at, closed_by, close_reason)
  status: occupied → awaiting_payment → needs_cleaning → closed
orders.table_session_id (nullable FK, komposit store_id)
guest_sessions (store_id, id, table_session_id, device_token_hash, expires_at)   -- 1 per perangkat, 3 jam
```

| Sinyal | Efek pada meja |
| -------- | ---------------- |
| Scan QR pertama / kasir buat order dine-in untuk meja | buka `table_session` (occupied) |
| Ada order dine-in belum lunas (open tab) | `awaiting_payment` |
| Semua order lunas **dan** `served`/`completed` | mulai timer idle (default 30 menit, per toko) |
| Kasir tekan "Kosongkan meja" / timer habis | `needs_cleaning` → `closed`; meja kosong di denah |
| Scan QR di meja yang `closed` | kunjungan baru (tamu berikutnya) |

- **Group order:** semua perangkat tamu di meja tergabung ke `table_session` yang sama, sehingga KDS dan kasir
  melihatnya sebagai satu grup. Setiap tamu hanya melihat ordernya sendiri (keputusan Q6).
- **Split bill:** untuk QR, setiap perangkat membayar sendiri (pay-first), jadi pembagian terjadi secara
  alami. Open tab hanya bisa dibuka kasir. Pembagian rata atau per item memakai tabel yang sudah ada,
  `payment_splits` dan `payment_split_items`. **Blocker:** `commitPaidOrderTx` saat ini melunasi *semua*
  split sekaligus, jadi perlu dikerjakan ulang sebelum open tab dan split diaktifkan.
- **Metrik:** perputaran meja dan durasi kunjungan diambil dari `table_sessions` (Module 3 analytics).

## 3. Port publik & form tamu

- **Port publik:** produksi hanya 80/443 (Nginx). Demo tidak membuka port masuk sama sekali (Quick Tunnel).
  Dev hanya `127.0.0.1`. API (8080) dan PostgreSQL tidak pernah diekspos.
- **Rute publik saat ini:** `/health*`, webhook Midtrans (signature SHA-512), `auth/staff/login`, dan
  `pin/forgot|setup` (limit ketat). Rute order publik tetap 503 sampai signed QR + guest session (02.01-D/E).
- **Form email tamu (E4, Planned):** kolom `email` opsional di checkout QR, plus checkbox *terpisah*
  "Saya mau menerima promo dari {toko}" yang default **tidak tercentang**. Struk digital dikirim bila email diisi.

```text
customer_contacts (store_id, id, email_normalized, first_seen_at, last_order_at)
marketing_consents (store_id, id, contact_id, granted bool, text_version, source, ip_hash, created_at)  -- append-only
orders.receipt_email (snapshot, nullable)
```

  Persetujuan dicatat append-only (versi teks, sumber, waktu) dan bisa dicabut lewat link unsubscribe.
  Data ini disimpan per toko; antar tenant tidak saling melihat (UU PDP 27/2022).

## 4. POS end-to-end: kebutuhan & pain point per peran

### Kasir

| Alur | Kebutuhan | Kondisi | Gap |
| ------ | ----------- | --------- | ----- |
| Walk-in tanpa meja | Order takeaway/dine-in langsung di POS, bayar di tempat | Layanan order + pembayaran tunai/EDC/static QRIS sudah ada | Rute HTTP kasir terkunci sampai 02.01-C |
| Dine-in vs takeaway | `order_type` + service charge/pajak takeaway per toko | `order_type` ada | Aturan service charge takeaway per toko belum ada |
| Tunai | Uang diterima, kembalian, laci | `cash_tendered_amount`, `change_given_amount` | Belum terikat shift/laci |
| Void (sebelum lunas) | PIN sendiri + alasan; di atas ambang PIN owner (O1) | Belum ada | Planned |
| Refund (setelah lunas) | Parsial per item, tunai dari laci aktif, non-tunai manual/API (C2) | Webhook menyimpan event refund saja | Planned |
| Pembayaran ganda | Deteksi + event `payment.refund_required` | Implemented (PR #5) | Notifikasi owner + antrean refund di UI |
| Shift & EOD | Buka modal, blind count, selisih > Rp5.000 → alasan + PIN owner, laporan X/Z, paid-out (C1) | Belum ada tabel | Planned |
| Kasir merangkap barista | Toggle per toko: cashier mendapat izin tiket barista (C3) | Belum ada | Planned (RBAC 02.01-C + KDS) |

**Pain point utama:** antrean menumpuk saat jam sibuk, kasir harus pindah layar, salah kembalian, dan
selisih kas akhir hari tanpa jejak. Desain menjawabnya dengan: satu layar POS yang juga menampilkan antrean
bar bila toggle C3 aktif, kalkulasi kembalian di server, blind count, dan audit per aksi.

### Barista & dapur (KDS)

| Kebutuhan | Kondisi | Gap |
| ----------- | --------- | ----- |
| Tiket per stasiun (bar/dapur) | Belum ada atribut stasiun pada produk | Tambah `products.prep_station` (bar/kitchen/none) + routing |
| Order baru muncul tanpa refresh + bunyi | `outbox_events` ditulis, **relay ke WebSocket belum ada** | Outbox relay worker + WS ticket (Phase 03) |
| Modifier kritis menonjol | Snapshot modifier sudah tersimpan | Format tiket KDS |
| Status preparing/ready/served | Status ada di DB | Endpoint transisi + otorisasi peran |
| SLA & aging | — | Threshold per toko (Module 2) |
| Penanda "bayar telat" (Q7) | Event `order.paid` dari late settlement | Flag di tiket |

### Owner

Undang staf, reset PIN, notifikasi akun terkunci (**Implemented**); laporan shift, refund, penjualan, HPP,
dan settlement (Planned, Phase 04–06).

## 5. Perangkat keras (H1)

| Perangkat | Pendekatan | Backend menyediakan |
| ----------- | ------------ | --------------------- |
| Printer thermal 58/80 mm ESC/POS (USB/LAN/Bluetooth) | Chrome: WebUSB/Web Serial untuk USB; aplikasi *print bridge* lokal untuk LAN/BT | `GET /orders/{id}/receipt?format=escpos\|text\|json` dari snapshot |
| Laci kas | Kick lewat port RJ11 printer (perintah ESC/POS `ESC p`) | Disertakan di payload struk tunai |
| Layar pelanggan (dual screen) | Perangkat sama: `BroadcastChannel`; perangkat beda: WebSocket kanal `customer-display` per terminal | Event keranjang, total, QR dinamis, status lunas |
| TV antrean | WebSocket read-only, tanpa PII (nomor antrean + status) | Kanal `queue` per toko |
| KDS tablet | Browser kiosk, auto-reconnect + replay | Outbox relay + WS ticket |

Fallback: bila printer gagal, struk digital (QR/link) selalu tersedia. POS tetap bisa mencatat transaksi
tunai, tetapi pembayaran online tidak bisa diproses saat internet mati.

## 6. Struk digital (R1, Planned)

- Token acak (256-bit, hash-only di DB) dengan masa berlaku 90 hari, alamatnya `/r/{token}`, bukan nomor order.
- Isi struk: nama toko, alamat, NPWP (opsional), nomor order + antrean, meja/takeaway, item + modifier + catatan,
  subtotal, diskon, service charge, PB1, total, metode bayar, uang diterima/kembalian, kasir, waktu WIB,
  status refund. Semua angka diambil dari snapshot.
- Format cetak ESC/POS 32/48 kolom; footer berisi QR yang mengarah ke struk digital.

## 7. Background job, worker, goroutine

| Worker | Interval | Fungsi | Status |
| -------- | ---------- | -------- | -------- |
| Realtime hub | event loop | Fan-out WebSocket per tenant | Implemented |
| Payment reconciliation | 30 detik | Expire order lewat batas waktu, `CheckTransaction` untuk pending > 2 menit | Implemented |
| **Email outbox** | 10 detik | Kirim email + retry/backoff, lease 10 menit, `SKIP LOCKED` | **Implemented (PR ini)** |
| Outbox relay → WebSocket | 1 detik / `LISTEN/NOTIFY` | Kirim `outbox_events` ke KDS/POS/TV; mengisi `processed_at` | Planned (Phase 03) |
| Table session auto-close | 1 menit | Menutup kunjungan idle (T1) | Planned |
| Session & token cleanup | harian | Menghapus sesi/token kedaluwarsa lewat SECURITY DEFINER sempit atau job migrator | Planned |
| End-of-day | per shift/hari | Ringkasan Z + email owner | Planned |

**Prinsip:** semua worker berjalan di dalam proses API (satu VPS, ADR-002), dihentikan lewat `workerCtx` saat
shutdown, menemukan tenant melalui fungsi `SECURITY DEFINER` sempit, lalu bekerja di bawah RLS per toko.
Klaim baris memakai `FOR UPDATE SKIP LOCKED` agar aman bila ada lebih dari satu instance. Tidak memakai Redis.

## 8. Kanal umpan balik

| Kanal | Untuk | Sumber | Status |
| ------- | ------- | -------- | -------- |
| Toast / flash di POS & KDS | Hasil aksi langsung (sukses, gagal, `REFRESH_RACE`) | Respons API (`code` + pesan Bahasa Indonesia) | Kontrak error tersedia |
| Notifikasi in-app real-time + suara | Order baru, siap saji, pembayaran lunas | WebSocket dari outbox relay | Planned |
| Email | Aktivasi, reset, PIN berubah, akun terkunci | `email_outbox` | Implemented |
| Email (berikutnya) | Struk, refund, ringkasan shift, pembayaran ganda | `email_outbox` | Planned |
| Web push | Owner saat aplikasi tertutup | — | Ditunda (N1) |
| Log server | Diagnosa; tanpa PIN, token, atau secret | `log` + Sentry | Implemented |
| Audit log | Akuntabilitas aksi sensitif | `audit_logs` (append-only) | Implemented untuk auth/pembayaran |

## 9. Backlog berurutan

| # | Item | Bergantung pada | Status |
| --- | ------ | ----------------- | -------- |
| 1 | Email outbox + aktivasi/reset PIN + PIN 6 digit | — | **Selesai di PR ini** |
| 2 | ✅ Selesai — 02.01-C RBAC: matriks izin, `/me.permissions`, identitas tepercaya di rute order/pembayaran, PIN owner untuk ambang O1/O4, toggle C3 | 1 | Next |
| 3 | 02.01-D Tabel meja + signed QR + jeda darurat | 2 | Planned |
| 4 | 02.01-E Guest session + `table_sessions` + form email/consent (E4) | 3 | Planned |
| 5 | Outbox relay → WebSocket + WS ticket + replay | 2 | Planned (Phase 03) |
| 6 | KDS: stasiun, transisi status, aging, flag bayar telat | 5 | Planned |
| 7 | Shift & laci kas, paid-out, X/Z, EOD + email owner | 2 | Planned |
| 8 | Void/refund (O1/C2) + rework split di `commitPaidOrderTx` + open tab | 2, 7 | Planned |
| 9 | Struk ESC/POS + struk digital (R1) + email struk | 4, 8 | Planned |
| 10 | Layar pelanggan & TV antrean | 5 | Planned |
| 11 | Backoffice email gagal + ekspor consent marketing | 1, 4 | Planned |
| 12 | Produksi: domain, Hostinger KVM 2, SPF/DKIM/DMARC, Midtrans live | domain | Blocked by Dependency |
