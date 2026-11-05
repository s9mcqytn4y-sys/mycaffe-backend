# PRD v0.9: Lantai Kafe

Status: **disetujui untuk dikerjakan** (keputusan W5–W10, wawancara ketiga belas, 2026-11-05, lihat
[DecisionRegister.md](../Architecture/DecisionRegister.md)). Satu PR per baris §3, di-merge ke `staging`
saat CI hijau (V3). Promosi ke `main` dan tag `v0.9.0` tetap menunggu persetujuan owner.

## 1. Masalah dan tujuan

v0.8 membuat uang benar dan kasir cepat. Masalah yang tersisa ada di lantai kafe saat jam sibuk:

1. Tamu pindah atau bergabung meja, dan kasir harus menutup lalu membuka pesanan baru.
2. Kafe dengan batas waktu duduk (mis. 90 menit di akhir pekan) mengandalkan ingatan staf.
3. Tamu takeaway bertanya "pesanan saya mana?" ke kasir.
4. Dapur dan bar hanya punya KDS. Kafe kecil memakai printer, bukan layar, dan butuh stiker label cup.
5. Struk digital hanya berupa tautan. Tamu ingin struk di email atau WhatsApp.

| Ukuran keberhasilan | Target | Cara ukur |
|---------------------|--------|-----------|
| Pindah/gabung meja tanpa void | 100 % kasus lewat endpoint, 0 void "salah meja" | `audit_logs` aksi `table.*` vs void beralasan meja ([AnalyticsPlan.md](AnalyticsPlan.md) §3) |
| Pesanan tercetak di stasiun yang benar | ≥ 99 % `print_jobs` `printed` dalam 10 detik | `print_jobs.printed_at - created_at` |
| Pertanyaan antrean ke kasir | Turun (kualitatif, wawancara owner setelah 2 minggu) | Wawancara |
| Struk digital terkirim | ≥ 95 % `sent` | `email_outbox` / `message_outbox` |

## 2. Benchmark majoo

Sumber: panduan pengguna dan halaman produk majoo, dicek 2026-11-05. Detail paritas fitur ada di
[CompetitorAnalysisMajoo.md](CompetitorAnalysisMajoo.md).

| majoo | MyCaffe v0.9 | Alasan berbeda |
|-------|--------------|----------------|
| Status meja berwarna; kuning = batas waktu hampir habis | Papan meja (sudah ada) + `timer_state` `ok` / `warning` / `expired` | Warna adalah urusan frontend; API memberi status + ikon + label (§5 brand) |
| Pindah meja, gabung meja (min. dua meja aktif yang sudah order), "gabung sisa pesanan" | Pindah = seluruh sesi; gabung = satu sesi bersama (W5); pisah = lepas tautan | Satu sesi bersama menjaga satu tagihan dan split bill yang sudah ada |
| PIN otorisasi opsional untuk gabung/pindah/reset meja | Tidak di v0.9; semua aksi meja tercatat di audit | Aksi meja tidak memindahkan uang. Ditinjau ulang jika data audit menunjukkan penyalahgunaan |
| Order Display menampilkan nama pelanggan, menu, meja, nomor antrean | Layar TV hanya nomor antrean + status (W7) | Layar publik di kafe; nama tamu tidak ditampilkan |
| Notifikasi status pesanan via WhatsApp | Struk via email (live) dan WhatsApp Cloud API (aktif setelah token Meta) | Nomor WA hanya dipakai untuk pesan yang diminta tamu |
| Struk digital via WhatsApp, email, SMS | Sama; SMS hanya ke nomor terverifikasi (V4) | Biaya SMS dan anti-spam |
| KDS butuh paket Prime dan aplikasi native | KDS di PWA (sudah ada), printer lewat agen cetak | Satu VPS, tanpa lisensi per perangkat |

## 3. Lingkup rilis

| PR | Lingkup | Keputusan | Migrasi |
|----|---------|-----------|---------|
| 9.1 | Pindah, gabung, pisah meja; filter area | W5 | `000033_table_session_links` (**selesai**) |
| 9.2 | Timer meja (peringatan saja) | W6 | `000034_table_timers` (**selesai**) |
| 9.3 | Layar antrean TV dengan token layar per toko | W7 | `000035_display_tokens` (**selesai**) |
| 9.4 | Antrean cetak: printer per stasiun, checker, label TSPL, agen cetak | W8 | `000036_printers_print_jobs` (**selesai**) |
| 9.5 | Struk digital: email, driver WhatsApp Cloud API, tautan SMS | W9 | `000037_receipt_deliveries` |

Di luar lingkup v0.9: pencarian SKU/barcode dan customer display (pindah ke v0.10 bersama perangkat),
PIN otorisasi aksi meja, peta lantai grafis (koordinat meja), reservasi.

### 3.1 PR 9.1: Pindah, gabung, pisah meja

**Cerita:** Sebagai kasir, saya memindahkan rombongan dari meja 4 ke meja 9, atau menggabungkan meja 4
dan 5 menjadi satu tagihan, tanpa void dan tanpa mengetik ulang pesanan.

Area memakai kolom `tables.section` yang sudah ada; tidak ada tabel area baru.

| Endpoint | Izin | Aturan |
|----------|------|--------|
| `GET /pos/tables?section=` | staf | Papan meja difilter per area |
| `POST /pos/table-sessions/:id/move` `{to_table_id}` | `TabManage` | Meja tujuan aktif dan tanpa sesi terbuka. Seluruh sesi pindah: `table_sessions.table_id` diganti, `orders.table_id` sesi ikut diperbarui. QR meja lama berhenti mengarah ke sesi |
| `POST /pos/table-sessions/:id/merge` `{session_id}` | `TabManage` | Kedua sesi terbuka dan beda meja. Sesi kedua ditautkan ke sesi utama (`table_session_links`), pesanannya pindah ke sesi utama. QR kedua meja memesan ke sesi utama |
| `POST /pos/table-sessions/:id/split` `{table_id}` | `TabManage` | Lepas tautan meja itu. Meja kembali kosong (sesi baru dibuka saat dipindai atau didudukkan); pesanan yang sudah ada tetap di sesi utama (dipisah lewat split bill yang sudah ada) |

Skema `table_session_links`: `store_id`, `primary_session_id`, `table_id`, `linked_at`,
`linked_by_user_id`, `unlinked_at`; unik satu tautan aktif per meja; FK komposit `(store_id, id)`; RLS.

Event outbox ke kanal `pos`: `table.moved`, `table.merged`, `table.split`. Setiap aksi menulis
`audit_logs`.

**Kriteria terima:**

- Pindah ke meja yang punya sesi terbuka atau sedang digabung → `409 TABLE_SESSION_OPEN`.
- Gabung atau pindah sesi yang sudah ditutup/digabung → `409 TABLE_SESSION_NOT_OPEN`; pisah meja yang tidak tergabung → `409 TABLE_NOT_MERGED`.
- Menutup sesi utama melepas semua meja yang tergabung.
- Tamu yang memindai QR meja yang digabung melihat pesanan sesi utama.
- Total tagihan setelah gabung = jumlah total kedua sesi sebelum gabung (dihitung ulang server, tes integrasi).
- Dua kasir menggabungkan sesi yang sama bersamaan → satu berhasil, satu `409` (`SELECT … FOR UPDATE`, urutan UUID).

### 3.2 PR 9.2: Timer meja

**Cerita:** Sebagai owner, saya menetapkan batas duduk 90 menit; kasir diberi tahu 10 menit sebelum
habis, tanpa ada pesanan yang ditolak.

- `store_settings`: `table_time_limit_minutes` (0 = mati, default 0), `table_timer_warning_minutes` (default 10).
- Kolom di `table_sessions` (relasi 1:1, tanpa tabel terpisah): `ends_at`, `warned_at`, `expired_at`,
  `extended_minutes`. `ends_at` diisi saat sesi dibuka jika batas aktif; mengubah batas hanya berlaku untuk
  sesi baru. Batas 0 (mati) atau 15–480 menit; peringatan 1–60 menit dan lebih pendek dari batas.
- Worker memindai setiap 30 detik (pola worker outbox yang sudah ada) dan menerbitkan
  `table.timer_warning` serta `table.timer_expired` ke kanal `pos`, masing-masing tepat sekali.
- `POST /pos/table-sessions/:id/extend` `{minutes}` (1–240): `ends_at` jadi max(`ends_at`, sekarang) + menit,
  peringatan dan kedaluwarsa aktif lagi, audit. Sesi tanpa timer → `409 TABLE_TIMER_OFF`.
- Worker memakai fungsi discovery `SECURITY DEFINER` `list_stores_with_due_table_timers` (hanya `store_id`).
- Papan meja mengembalikan `timer_state` (`none`, `ok`, `warning`, `expired`) dan `ends_at`.
- Pesanan tetap diterima setelah kedaluwarsa (W6).

**Kriteria terima:** tes dengan jam palsu (`WithClock`) untuk peringatan, kedaluwarsa, perpanjangan, dan
idempotensi worker saat dua instance berjalan.

### 3.3 PR 9.3: Layar antrean TV

**Cerita:** Sebagai tamu takeaway, saya melihat nomor antrean saya pindah dari "Disiapkan" ke "Siap
diambil" di TV.

- Tabel `display_tokens`: `store_id`, `name`, `token_hash` (SHA-256), `created_by_user_id`,
  `revoked_at`, `last_seen_at`. Format token `store.id.secret`, ditampilkan sekali saat dibuat.
- Owner: `POST /store/display-tokens`, `GET /store/display-tokens`, `POST /store/display-tokens/:id/revoke`.
- Publik: `GET /public/queue` dengan header `Authorization: Display <token>` mengembalikan
  `[{queue_number, status, updated_at}]` untuk pesanan hari bisnis ini yang `queued`, `preparing`, `ready`.
- WebSocket: tiket dengan token layar, kanal baru `queue`, hanya event `queue.updated`
  `{queue_number, status}`.
- Payload tanpa nama tamu, nomor HP, item, atau nominal (W7).
- Token memuat `store_id` (pola kartu badge staf), jadi resolusi langsung lewat `WithTenantTx` dan RLS; tidak
  perlu fungsi `SECURITY DEFINER` baru. Maksimal 10 layar aktif per toko; `last_seen_at` dicatat paling
  sering sekali per menit. Event `queue.updated` diturunkan relay dari event pesanan yang ada, tidak disimpan.

**Kriteria terima:** token dicabut → `401` dalam satu permintaan dan koneksi WS ditutup; token toko A tidak
pernah melihat antrean toko B (tes RLS); batas laju per token.

### 3.4 PR 9.4: Antrean cetak

**Cerita:** Sebagai barista di kafe tanpa layar KDS, saya menerima tiket cetak di printer bar; kasir
mencetak stiker label untuk setiap cup.

- Tabel `printers`: `name`, `station` (`bar`, `kitchen`, `checker`, `label`; struk kasir tetap lewat RawBT dan
  `GET /pos/orders/:id/receipt?format=escpos`), `format`
  (`escpos`, `tspl`), `paper_width_mm` (58/80 untuk ESC/POS; lebar label untuk TSPL), `copies` (1–5),
  `footer`, `agent_token_hash`, `is_active`.
- Tabel `print_jobs`: `printer_id`, `order_id`, `kind` (`ticket`, `checker`, `receipt`, `label`),
  `payload` (byte siap cetak, dirender server), `status` (`pending`, `claimed`, `printed`, `failed`,
  `dead`), `attempts`, `claimed_at`, `printed_at`, `last_error`. Retry dan dead-letter mengikuti
  outbox (000025).
- Pemicu: job dibuat bersama tiket KDS baru (pembayaran lunas atau ronde dikirim), sehingga webhook ganda
  tidak mencetak dua kali. Satu job per printer aktif yang punya baris: stasiun bar/dapur barisnya sendiri,
  checker seluruh pesanan atau ronde, label satu per cup minuman bar. Salinan (`copies`) sudah ada di payload.
  Pengaturan printer yang tidak bisa dirender dilewati dengan log, tidak pernah menggagalkan pembayaran.
- Agen cetak (aplikasi kecil di LAN kafe, di luar repo ini; RawBT tetap didukung lewat struk):
  `POST /print-agent/jobs/claim` → job berikut, `POST /print-agent/jobs/:id/ack` `{status, error}`.
  Autentikasi `Authorization: Printer <token>`, satu token per printer.
- `POST /pos/orders/:id/reprint` `{kind}` membuat job baru, tercatat di audit.
- Renderer ESC/POS memakai `RenderESCPOS` yang ada (`internal/service/receipt_escpos.go`); renderer TSPL
  baru di sebelahnya (`receipt_tspl.go`).

**Kriteria terima:** satu job per printer per pemicu (idempoten saat webhook ganda); claim memakai
`FOR UPDATE SKIP LOCKED`; snapshot tes byte ESC/POS dan TSPL; job yang tidak di-ack dalam 60 detik
kembali `pending`.

### 3.5 PR 9.5: Struk digital

**Cerita:** Sebagai tamu, saya menerima struk di email atau WhatsApp setelah membayar.

- `POST /pos/orders/:id/receipt/send` `{channel: email|whatsapp|sms, to}` dan versi tamu dari halaman
  pelacak pesanan.
- Email memakai `email_outbox` dan template baru. WhatsApp memakai driver baru di `message_outbox`
  (Cloud API, template pesan disetujui Meta); tanpa token → `503 WHATSAPP_UNAVAILABLE`. SMS hanya ke
  nomor `customers` yang terverifikasi (V4) dan hanya berisi tautan struk.
- Tabel `receipt_deliveries` untuk audit dan batas laju (per pesanan maks. 3 kirim, per nomor/email
  maks. 10 per hari).
- Isi pesan hanya tautan `receipt_tokens` yang sudah ada (90 hari), bukan rincian struk.

**Kriteria terima:** batas laju → `429`; nomor belum terverifikasi untuk SMS → `422`; nomor dan email
disimpan, tidak pernah ditulis ke log.

## 4. Persyaratan non-fungsional

- Setiap tabel baru: `store_id`, FK komposit `(store_id, id)`, `ENABLE ROW LEVEL SECURITY`, policy
  tenant, migrasi `down` menolak jalan bila ada data.
- Tidak ada perubahan pada kalkulasi uang. Gabung meja tidak menghitung ulang harga item; total
  diturunkan dari snapshot pesanan.
- Semua endpoint baru masuk `docs/Api/OpenApi.yaml` dan
  [FrontendIntegrationContract.md](../Architecture/FrontendIntegrationContract.md).
- Event baru mengikuti taksonomi di [AnalyticsPlan.md](AnalyticsPlan.md) §2.
- Teks untuk tamu, kasir, dan TV mengikuti [BrandAndProductKnowledge.md](BrandAndProductKnowledge.md) §5.

## 5. Pertanyaan terbuka

1. Agen cetak: aplikasi Windows kecil, Android (RawBT), atau keduanya? Menentukan siapa yang membangunnya.
2. Template WhatsApp: nama bisnis yang diverifikasi Meta dan teks template perlu disiapkan owner.
3. Apakah timer meja perlu berbeda per area (mis. teras vs dalam)? Default: satu nilai per toko.
