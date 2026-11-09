# PRD: WPA QR Order Meja

Status: disetujui arah (ADR-010), 2026-11-09. Label bukti: **[V]** dicek di kode atau demo, **[S]** sumber sekunder, **[A]** asumsi.

## 1. Masalah dan tujuan

Tamu kafe ingin memesan dan membayar dari meja tanpa memasang aplikasi dan tanpa memanggil staf. Web yang ada sekarang belum bisa memenuhi itu: halaman order hanya menjual varian default, tidak ada addon, dan belum memakai brand gerai [V].

**Tujuan rilis:** tamu menyelesaikan pesanan dari pindai QR sampai tiket masuk dapur tanpa bantuan staf.

**Ukuran:** persentase sesi QR yang berakhir lunas, waktu median pindai sampai bayar, dan pesanan yang butuh bantuan staf. Target angka ditetapkan setelah pilot, karena belum ada data.

**Di luar cakupan:** semua operasional tenant, langganan, onboarding, dan pemasaran.

## 2. Pengguna dan cerita

| Sebagai | Saya ingin | Supaya |
|---|---|---|
| Tamu | Melihat menu dengan foto dan harga akhir | Tahu yang saya bayar |
| Tamu | Memilih varian, addon, dan sub-addon | Pesanan sesuai selera |
| Pemesan | Membagi tagihan rata atau per item | Teman ikut membayar |
| Teman | Membuka tautan dan membayar bagian saya | Tidak perlu mengirim uang ke pemesan |
| Tamu | Melihat status pesanan dan menerima struk | Tahu pesanan diproses |

## 3. Alur ujung ke ujung

1. Pindai QR, `GET /public/qr/:token` [V].
2. Sesi tamu lewat cookie HttpOnly, `POST /public/qr/:token/session`, berlaku 3 jam [V].
3. Katalog dan brand, `/public/stores/:slug/catalog` dan `/cms` [V].
4. Keranjang, total selalu dari `/orders/quote` [V].
5. Buat pesanan dengan `expected_quote_fingerprint` [V].
6. Bayar penuh atau bagi tagihan (rute split baru, ADR-010).
7. QRIS Midtrans dari key tenant, webhook per tenant [V].
8. Setelah lunas, tiket KDS dibuat dan event `order.paid` terkirim [V].
9. Status lewat WebSocket tiket tamu, jatuh ke polling [V rute ada, belum dipakai web].
10. Struk lewat tautan publik `/public/receipts/:token` [V rute ada, belum ada halaman web].

## 4. Evaluasi fitur saat ini

| Area | Kondisi | Tindakan |
|---|---|---|
| Keranjang | Tanpa varian dan modifier, hitungan lokal saat quote gagal [V] | Pemilih opsi, hapus estimasi lokal |
| Validasi | Nama bebas tanpa batas di UI [V] | Label terlihat, batas panjang, error di dekat field |
| Bayar | Hanya QRIS, QR `Svg` dibuat di klien [V] | Tambah instruksi, kedaluwarsa, bayar ulang |
| Split | Tidak ada untuk tamu [V] | ADR-010 |
| Struk | Kirim email tersedia, halaman publik belum [V] | Halaman struk |
| Lifecycle meja | Rotasi QR dan arsip ada di API, tidak ada layar [V] | Android |
| Template QR per tenant | Tidak ada di backend [V] | Android, generator poster |
| Notifikasi tamu | Hanya polling 3 detik [V] | WebSocket `guest:<session>` |
| i18n | Hanya legal yang memakai locale [V] | Kamus `id-ID` dan `en-US` |

## 5. Temuan keamanan dan kegagalan

| # | Temuan | Risiko | Tindakan |
|---|---|---|---|
| 1 | Zona `public_limit` 30 permintaan per menit per IP, sedangkan pelacak polling 20 per menit per tamu [V] | Tamu di Wi-Fi kafe yang sama saling kena 429 | WebSocket dulu, zona terpisah |
| 2 | Tidak ada CSP dan Permissions-Policy di demo [V] | Dampak XSS lebih besar | Tambah di nginx |
| 3 | `postcss` transitif punya 2 advisori high [V] | Build time | `pnpm.overrides` |
| 4 | Warna CMS tenant masuk ke halaman [V rancangan] | Kontras buruk, injeksi CSS | Validasi hex dan kontras |
| 5 | Tautan split publik [V rancangan] | Diteruskan atau ditebak | Token 32 byte, hash, kedaluwarsa |
| 6 | Pembayaran sebagian lalu pesanan batal [A] | Dana tertahan | Refund per bagian, cek batas refund per kanal |
| 7 | Sinyal hilang saat bayar [A] | Pesanan ganda | Idempotency key disimpan per percobaan |
| 8 | `qr_ordering_paused` dan stok habis saat bayar | Tamu bingung | Pesan jelas per kondisi |

Uji penetrasi aktif belum dijalankan. Daftar ini berasal dari pembacaan kode dan header demo.

## 6. Evaluasi Android

Compose, Ktor, Koin, dan CameraX layak dipertahankan. Celahnya [V]:

- Tidak ada navigasi multi-layar yang mencakup meja, menu, KDS, dan setup layar.
- Alamat API di-hardcode ke `api.abbdullaziz.online`, sedangkan demo memakai `demo.abbdullaziz.online`.
- Hanya ada 2 file test. Perilaku sinkron dan login belum teruji.
- `security-crypto` dan `biometric` memakai versi alpha.
- Data contoh terlihat di `PosLandscapeScreen.kt` dan `PaymentChannel.kt`, belum diperiksa rinci.

## 7. Kompetitor

| Produk | Yang relevan | Sumber |
|---|---|---|
| majoo | Self Order hanya di paket Prime Rp 999.000 | majoo.id/harga [V] |
| Klikit | QR ordering di Omni, Core + Omni Rp 958.000 | klikit.io [V] |
| Kasir Pintar | Punya QR Order dan split pesanan | Pusat bantuan, judul saja [S] |

Keluhan pengguna yang terdokumentasi tipis. Yang muncul berulang di sumber sekunder: QR yang memaksa pasang aplikasi atau daftar akun, menu yang sulit dibaca, dan sinyal buruk di dalam gedung [S]. Karena itu WPA tidak meminta akun, foto selalu punya ukuran tetap, dan tamu tetap punya jalur minta bantuan staf.

## 8. Teknologi

Next.js 15 App Router dipertahankan. State keranjang cukup `useReducer`, tanpa pustaka state. Tambahan yang dibenarkan: `next-intl` untuk kamus. Tidak perlu pustaka komponen atau animasi. Target awal: LCP 2,5 detik di 4G, JS 150 KB gzip per rute, WCAG 2.2 AA. Semua target belum diukur.

## 9. Urutan kerja

1. Pangkas WPA (client #27).
2. Dokumen ini.
3. UI tamu dengan brand tenant, i18n, a11y.
4. Backend split tautan, zona rate limit, seed demo dan foto.
5. Simulasi E2E otomatis terhadap demo sandbox.
6. Layar operasional Android.
