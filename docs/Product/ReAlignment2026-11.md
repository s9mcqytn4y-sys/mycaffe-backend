# Re-Alignment Cassy by AzureDevID (Draft, 2026-11-09)

Revisi Director 2026-11-09: domain wajib `abbdullaziz.online`, landing page dan jualan ditunda, fokus stabilitas demo cloud dan client. Bagian analisis yang menyarankan domain baru tetap sebagai catatan, keputusan yang berlaku ada di bagian 11.

Status: **Draft untuk keputusan Director**. Belum ada keputusan di sini yang berlaku sampai dicatat di `docs/Architecture/DecisionRegister.md`.

Label keyakinan: **[V]** diverifikasi langsung (repo, file, halaman resmi, HTTP). **[S]** sumber sekunder atau bertentangan. **[A]** asumsi atau pengetahuan umum yang belum dicek.

## 1. Ringkasan eksekutif

Blokir terbesar bukan teknologi. `abbdullaziz.online` masih halaman parkir Hostinger, jadi aktivasi Midtrans, toko aplikasi, SEO, dan penjualan semuanya tertahan di hal yang sama. Backend sudah v0.12.1, tapi belum ada satu pun jalur publik yang bisa dilihat calon pelanggan atau reviewer.

Rekomendasi urutan: (1) website publik dan perbaikan data Midtrans, (2) rapikan sumber kebenaran dokumen dan identitas, (3) deploy v0.12.1 ke demo, (4) lint, i18n, a11y sebagai gerbang CI, (5) layar client untuk fitur v0.11 dan v0.12, (6) pilot berbayar 3 sampai 5 kafe, (7) toko aplikasi, (8) ojol lewat Klikit hanya setelah ada permintaan berbayar.

Lima koreksi terhadap asumsi Anda:

| Asumsi | Fakta | Dampak |
|---|---|---|
| Ada 3 repo terpisah | `C:\Projects\CassyWorktree` adalah worktree repo backend (branch `feat/cassy-rebrand`), tidak punya kerja unik. Bridge WhatsApp ada di `cmd/wa-bridge` di repo backend [V] | Hanya 2 repo: backend dan client |
| AWS bisa tetap $0 | Free plan berakhir pada 6 bulan atau saat $200 habis, lalu akun ditutup, data dihapus setelah 90 hari [S, konsisten dengan pengumuman AWS] | Jangan taruh apa pun yang harus hidup lama di akun free plan |
| Harus bayar Klikit dulu | Sandbox Partner API gratis. Biaya baru ditetapkan saat persetujuan produksi [V] | Integrasi bisa dibangun dengan $0 |
| Bayar Klikit, langsung terintegrasi penuh | Paket di bawah 51 toko hanya webhook. API penuh (stok, ketersediaan) mulai paket Scale dan ada biaya build khusus [V, harga "indikatif"] | Sinkron menu dan stok tidak tersedia di tahap awal |
| Layanan BYOK jarang ditemukan | Belum terbukti. majoo dan InterActive punya pembayaran terintegrasi sendiri, tapi saya tidak menemukan data apakah mereka menawarkan akun gateway milik merchant [A] | Jangan klaim "satu-satunya" di materi publik |

## 2. Midtrans

### 2.1 Isi kedua email (akun G357841497)

- Semua 14 metode butuh koreksi **Website** dan **Foto Selfie**. ShopeePay dan ShopeePay QRIS juga butuh **Payment Flow**.
- Kredivo baru diproses setelah akun lolos validasi.
- Midtrans menanyakan `geaniproduction.com/fotoflow` sebagai URL. Halaman itu hidup dengan judul "Fotoflow, Langganan dan akun". Tidak ada rujukannya di vault maupun docs repo [V]. **Reviewer harus diberi URL yang cocok dengan bidang usaha di NIB.**
- Saya tidak mengisi form dan tidak membalas email. Keduanya menunggu Anda.

### 2.2 Kritik

1. **Website parkir [V].** Akar paling mungkin dari koreksi "Website". Syarat resmi: situs aktif, sesuai kategori, produk jelas, harga Rupiah. Checklist vault Anda sudah menyebut 9 titik, tapi tidak satu pun tampil publik.
2. **14 metode untuk akun yang hanya menagih langganan terlalu banyak.** Lane 1 hanya menjual SaaS Rp 149 ribu sampai Rp 599 ribu. Akulaku, Kredivo, Alfamart, dan kartu menambah beban review tanpa alasan bisnis. Rekomendasi: QRIS dan VA dulu, kartu menyusul. Paylater tidak perlu untuk Lane 1.
3. **Label "Aggregator" perlu ditanyakan.** Tidak ada definisinya di docs yang terbaca [V]. Bisa berarti model akuisisi Midtrans sendiri, bisa juga klasifikasi yang berbenturan dengan ADR-009. Tanyakan tertulis ke Wawa.
4. **Surat Pelimpahan Dana di vault belum saya baca**, PDF-nya tidak bisa diekstrak di mesin ini. Pastikan isinya hanya rekening milik sendiri.
5. **Dokumen vault masih memakai nama lama**: `MyCaffe`, `online.abbdullaziz.mycaffe`, dan `api-demo.abbdullaziz.online`. Reviewer akan melihat tiga nama untuk satu usaha.
6. **Sumber override notifikasi.** Kode memakai `X-Override-Notification` lewat SDK (`internal/pkg/midtrans/midtrans.go:164`), sementara halaman header dan webhook yang terbaca tidak mencantumkannya [V]. Uji di sandbox per tenant, dan jadikan URL dashboard tenant sebagai jalur utama.

### 2.3 Biaya dan risiko per channel (harga publik, belum termasuk negosiasi)

| Channel | Biaya | Refund | Risiko utama |
|---|---|---|---|
| QRIS | 0,7% | GoPay on-us 45 hari, off-us 7 hari. ShopeePay off-us BCA, BNI, BRI tidak bisa | Refund off-us 15 sampai 20 hari, kasir harus diberi tahu |
| GoPay, ShopeePay | 2% | GoPay 45 hari. ShopeePay 365 hari, jam 06.00 sampai 23.50 WIB | Biaya mahal untuk tiket kecil |
| DANA, OVO | 1,5% | DANA 45 hari. OVO penuh saja, 7 hari | Partial refund OVO tidak ada |
| VA | Rp 4.000 + PPN | Tidak bisa | Rp 4.440 sama dengan 3% dari paket Rp 149 ribu. Refund manual |
| Kartu | 2,9% + Rp 2.000 | 6 bulan | Scope PCI kalau data kartu menyentuh client |
| Alfamart | Rp 5.000 | Tidak bisa | Tidak cocok di kafe |
| Akulaku, Kredivo | 1,7%, 2% | Bisa | Tidak relevan untuk transaksi kopi |

- BI memperluas MDR QRIS 0% untuk transaksi sampai Rp 100.000 mulai 1 Oktober 2026, usaha mikro sampai Rp 500.000 [S, BCA dan Antara]. Halaman Midtrans masih 0,7% dan bertanggal 2025 [V]. **Minta konfirmasi tertulis.** Kalau berlaku, ini angka jualan kuat karena hampir semua tiket kafe di bawah Rp 100.000.
- Pencairan: minimal 3 hari kerja setelah settled [V].

### 2.4 Model BYOK

Secara hukum ini model yang tepat: dana tidak lewat AzureDevID. Dua konsekuensinya:

- **Friksi onboarding.** Tiap kafe harus lolos review sendiri. Kabar baiknya, kafe tanpa website bisa daftar dengan media sosial, tapi jalur katalog produk hanya membuka VA, QRIS, dan GoPay [V]. Halaman toko Cassy bisa jadi URL mereka, perlu ditanyakan ke Midtrans.
- **Risiko konsentrasi.** Server key semua tenant dijaga satu `CREDENTIAL_ENCRYPTION_KEY`. Key itu bocor berarti semua tenant terdampak. Perlu key per tenant dan prosedur rotasi sebelum tenant ke-10.
- Akun Partner Midtrans (satu Partner ID, banyak Merchant ID) [V] layak ditanyakan. Contoh di docs adalah grup usaha, jadi kecocokan untuk SaaS belum pasti.

## 3. Infrastruktur dan AWS

| Kebutuhan | Rekomendasi | Biaya dev |
|---|---|---|
| Dev | Docker lokal + Postgres 18 | $0 |
| Publik demo | Cloudflare Tunnel, sudah jalan | $0 |
| Media, backup | R2, sudah jalan | Kuota gratis [A] |
| Landing page | Cloudflare Pages | $0 [A] |
| Email transaksional | **Satu-satunya alasan kuat pakai AWS: SES.** Gmail App Password tidak layak produksi | Sangat kecil, perlu keluar dari sandbox SES |
| Produksi | Tetap VPS tunggal (ADR-002) sampai ada 20 tenant berbayar | Berbayar |
| AWS lain (ECS, RDS, Cognito, Lambda) | Tidak perlu sekarang. Biaya tetap tanpa pelanggan | |

Pushback region: Anda memilih `ap-southeast-2` (Sydney). Pelanggan di Indonesia, dan ada region Jakarta `ap-southeast-3`. Sydney menambah latensi dan membuat data lintas negara [A]. Kalau hanya untuk SES, Sydney masih dapat diterima. Untuk database, pakai Jakarta.

Agent Toolkit AWS: skrip memasang AWS CLI (belum terpasang [V]), butuh `aws login` di browser oleh Anda, menulis konfigurasi MCP, dan menyisipkan aturan ke `CLAUDE.md`/`AGENTS.md` [V]. Halaman tidak menyebut sumber daya berbayar, tapi juga tidak mencantumkan harga. Pembuatan akun harus Anda lakukan sendiri. Rekomendasi: pasang setelah keputusan region, tanpa rules di repo, dan aktifkan budget alarm $1 lebih dulu.

## 4. Kompetitor dan harga

| | majoo | InterActive MyProfit | Klikit ID | Cassy (rencana) |
|---|---|---|---|---|
| Harga/bulan per outlet | 249 rb, 499 rb, 999 rb | Gratis, 149 rb, 249 rb, 299 rb | Core 269 rb, + Omni 958 rb | 149 rb, 299 rb, 599 rb |
| KDS | Prime (999 rb) | Add-on | Core | Rencana |
| Self order / QR meja | Prime | Add-on kiosk | Omni | Ada |
| GoFood, GrabFood | Prime | GrabFood | Omni | Belum |
| Offline | Prime, Local Server | Ya | Tidak tercantum | Ada (tunai) |
| Coba gratis | 14 hari | Free for life | Tidak ada | Belum ada |

Harga halaman publik, 2026-11-09 [V]. Harga majoo mungkin sudah atau belum termasuk diskon 50%. MyResto dijual sekali bayar tanpa harga publik.

Bacaan CFO: Cassy tidak akan menang harga karena MyProfit punya paket gratis. Posisi terbaik: **QR meja, KDS, dan offline di paket yang harganya sepertiga majoo Prime, dan uang langsung ke rekening kafe.**

### Unit economics yang wajib dijaga

- **Klikit Starter US$49/toko/bulan**, sekitar Rp 810 ribu pada kurs asumsi Rp 16.500 [A]. Lebih mahal dari paket tertinggi Cassy. Jadi harus add-on minimal sekitar Rp 1 juta/toko, atau tenant berlangganan Klikit sendiri. **Jangan dibundel ke tier.**
- **WhatsApp utility sekitar Rp 356,65 + PPN** [S, sumber bertentangan]. 100 struk/hari sama dengan sekitar Rp 1,19 juta/bulan, 8 kali harga paket terendah. Struk WA wajib memakai akun WA tenant atau kuota berbayar.
- CFO belum bisa menghitung CAC, churn, dan runway karena belum ada data penjualan. Itu tujuan pilot.

## 5. Brand dan audiens

Logo (`CassyLogoLightVector.svg`) [V]:

- Gradien biru, cyan, lalu cokelat melanggar panduan sendiri yang melarang gradien gaya AI, dan terlihat seperti dua brand ditempel.
- Monogram garis 44 unit dengan tiga warna hilang jelas di ukuran ikon kecil.
- Teks masih `<text>` bergantung font, belum dikonversi jadi path. Bisa tampil salah di mesin tanpa Plus Jakarta Sans.

Rekomendasi: satu warna solid untuk logo, satu aksen. "by AzureDevID" dihapus dari logo utama, cukup di footer dan dokumen legal.

Lainnya:

- Palet Espresso mengikat Cassy pada kopi, padahal target F&B luas. `DESIGN.md` client masih "MyCaffe by AzureDevID".
- Domain `abbdullaziz.online` memakai nama pribadi dan salah eja mudah terjadi. Produk perlu domain sendiri. Ketersediaan dan merek DJKI "Cassy" belum dicek.
- "Gen-Z" perlu diperjelas. Pemilik kafe muda (pembeli software) dan pengunjung Gen-Z (pengguna QR) butuh pesan berbeda. Bukti riset yang ditemukan tipis [S]. Target usulan: pemilik muda 1 sampai 3 outlet.
- Nada: bicara seperti rekan pemilik kafe, angka nyata, tanpa jargon "ekosistem", "all-in-one". Contoh: "Pesanan masuk dapur tanpa teriak. Uangnya langsung ke rekening kafe."
- SEO/AEO: halaman dasar harus ada dulu. Lalu halaman use case, perbandingan, panduan harga, FAQ, Google Business Profile, dan video TikTok atau YouTube operasional. Banyak statistik AEO berasal dari vendor [S], jadi ukur sendiri dengan 15 sampai 20 pertanyaan target.

## 6. Regulasi

| Area | Temuan | Aksi |
|---|---|---|
| FDA | Tidak relevan, Cassy bukan alat kesehatan | Dilewati |
| UU PDP | PP 33/2026 sudah terbit, denda sampai 2% pendapatan. Lembaga PDP belum terbentuk [S]. ADR-009 baru mengutip UU 27/2022 | Baca teks PP, terutama transfer luar negeri dan prosesor. Perbarui DPA. Konsultasi hukum sebelum pilot |
| PSE Komdigi | Permenkominfo 5/2020, penegakan aktif 2026 [S]. Status SaaS B2B tidak eksplisit | Daftar, biaya kecil dibanding risiko |
| Android | Verifikasi developer berlaku di Indonesia sejak 30 September 2026, nama paket harus didaftarkan [V] | Daftarkan `com.azuredevid.cassy` setelah Bundle ID final |
| Apple | Perorangan masuk sebagai individu, nama penjual tampil nama pribadi. DBA ditolak. Organisasi butuh badan hukum dan D-U-N-S [S, merujuk halaman Apple] | Putuskan: rilis atas nama pribadi atau bentuk badan hukum |
| Apple IAP | Makanan fisik di luar aplikasi memakai pembayaran non-IAP. Langganan SaaS berpotensi kena aturan digital [S] | Jual langganan di web saja, tanpa tautan beli di aplikasi. Konfirmasi ke teks panduan |
| Google Play | Org butuh D-U-N-S. Aturan tester personal belum diverifikasi | Cek saat daftar |

## 7. Teknologi dan kualitas

Stack tetap: Go, Gin, Postgres RLS, Next.js 15, Compose. Tidak ada alasan menulis ulang.

Celah yang perlu ditutup [V]:

| Celah | Bukti | Tindakan |
|---|---|---|
| Go tanpa golangci-lint | Tidak ada config atau langkah CI | Tambah preset kecil di CI |
| Web tanpa ESLint | Tidak ada di `package.json` | ESLint + `jsx-a11y` |
| i18n | 0 file memakai library i18n, kamus token ada | `next-intl`, mulai dari layar baru |
| a11y | Atribut ada di 9 file saja | axe di Playwright untuk alur kasir dan QR |
| Dokumen basi | `memory/STATUS.md` masih bicara `staging` dan 37 migrasi. Sekarang 56 | Tulis ulang |
| Git docs | Masih model `staging`, branch itu sudah tidak ada. CI masih menyebutnya | Perbarui |
| Bundle ID | `com.azuredevid.cassy` di app, `online.abbdullaziz.mycaffe` di CLAUDE global dan vault | Pilih satu sebelum upload pertama, ini permanen |
| WhatsApp | `whatsmeow` sudah ditolak di produksi oleh `validateWhatsAppConfig` (W12). Pedoman WhatsApp melarang klien tidak resmi | Pertahankan: dev dan demo saja, produksi Cloud API. Kalimat "driver utama" di commit rebrand jangan dibaca sebagai produksi |
| Email | Gmail App Password | SES atau sejenisnya |

SOLID dan DRY: terapkan pada kode baru dan kode yang disentuh, ditegakkan linter. Refactor massal tanpa bug nyata hanya membakar waktu.

Notifikasi: WebSocket untuk dapur dan kasir (sudah ada), push FCM/APNs untuk owner, email untuk invoice, WA template hanya untuk tagihan. SMS dimatikan kecuali diminta karena mahal [A].

MikroTik: tiga VLAN dan dual-WAN cocok untuk outlet ramai, tapi terlalu berat sebagai syarat. Persyaratan minimum usulan: router apa pun, reservasi IP printer, SSID tamu terpisah. MikroTik jadi paket opsional lewat teknisi. Generator `.rsc` tetap, tapi antrian QoS harus menyebut domain produksi, bukan `demo.`.

## 8. Versi, branch, CI/CD

- **Branch**: trunk-based. `main` plus branch pendek `feat/`, `fix/`, `docs/`. Staging sudah hilang, jangan dihidupkan lagi.
- **Lingkungan**: `main` ke demo otomatis, tag ke produksi manual dengan persetujuan Director.
- **Versi**: SemVer per repo. API `v0.x` sampai pilot stabil, `v1.0.0` saat tenant berbayar pertama berjalan 30 hari. Client mengikuti kontrak OpenAPI.
- **PR**: kecil, CI hijau, satu tujuan. Kedua repo publik, jadi branch protection bisa diaktifkan di `main`.
- **Kecepatan**: job integrasi tetap, tambahkan path filter agar perubahan docs tidak menjalankan semuanya.

## 9. Urutan pengembangan baku

| Tahap | Isi | Selesai kalau |
|---|---|---|
| 0 | Landing, harga, legal, kontak. Perbaiki data Midtrans | Halaman publik hidup, form dikirim Director |
| 1 | Keputusan grill, sinkron `*.md`, ID, nama | Tidak ada nama lama di dokumen aktif |
| 2 | Deploy v0.12.1 ke demo, migrasi 54 sampai 56 | Smoke test lulus |
| 3 | Gerbang lint, i18n, a11y | CI wajib hijau |
| 4 | Layar client v0.11 dan v0.12 | E2E alur kasir |
| 5 | Batas kredit, credit note, rotasi key | Test integrasi |
| 6 | Pilot 3 sampai 5 kafe berbayar | Data CAC, churn, aktivasi |
| 7 | Rilis Android, lalu iOS | Lolos review |
| 8 | Klikit, sandbox dulu | Ada toko yang bayar add-on |

## 10. Rename

Rename menunggu keputusan dan semua sesi selesai. Jangan ubah nama role database `mycaffe_app`/`mycaffe_migrator`, nama migrasi, atau bucket R2 karena berisiko tinggi dan tidak terlihat pengguna. `CassyWorktree` bisa dilepas setelah disetujui.

## 11. Keputusan grill

| # | Keputusan | Tanggal |
|---|---|---|
| 1 | Domain resmi wajib `abbdullaziz.online` (direvisi Director) | 2026-11-09 |
| 2 | Fokus saat ini stabilitas demo cloud dan client. Landing page dan jualan ditunda (menggantikan pilihan `cassy.id`) | 2026-11-09 |
| 3 | Tetap usaha perorangan sampai Midtrans disetujui dan pilot berbayar berjalan. Pemicu naik: sebelum tenant ke-10 atau pendapatan menutup biaya pendirian | 2026-11-09 |
| 4 | Akun Midtrans AzureDevID tetap mengajukan 14 metode (menyimpang dari rekomendasi QRIS dan VA). Perlu dokumen Payment Flow ShopeePay | 2026-11-09 |
| 5 | Harga publik mengikuti seed: Starter Rp 149.000, Pro Rp 299.000 (paket utama), Enterprise Rp 599.000, uji coba 14 hari. Penegakan batas paket wajib sebelum pilot berbayar | 2026-11-09 |
| 6 | AWS hanya untuk SES di `ap-southeast-2`, dengan budget alarm $1. Produksi tetap VPS dan Cloudflare sampai sekitar 20 tenant berbayar | 2026-11-09 |
| 7 | Struk digital default memakai link gratis. WhatsApp hanya untuk Pro ke atas dengan akun WhatsApp Business milik tenant. OTP, tagihan langganan, dan pengingat invoice memakai akun Cassy | 2026-11-09 |
| 8 | Logo dirapikan jadi satu warna dengan satu aksen, monogram tetap, teks jadi path, "by AzureDevID" pindah ke footer. Dikerjakan setelah cek PDKI | 2026-11-09 |
| 9 | Target pembeli tahun pertama: pemilik kafe muda 1 sampai 3 outlet. Gen-Z menjadi standar pengalaman QR, bukan sasaran penjualan | 2026-11-09 |
| 10 | Rename ke Cassy mencakup folder, repo GitHub, dan image GHCR serta file `deploy/`, dalam PR terpisah dengan smoke test demo. Role DB, nama migrasi, dan bucket R2 tetap | 2026-11-09 |
| 11 | Rilis trunk-based: branch pendek ke `main`, `main` otomatis ke demo, tag `v*` ke produksi dengan persetujuan Director. `staging` tidak dihidupkan lagi | 2026-11-09 |

## Sumber

[Harga Midtrans](https://midtrans.com/id/pricing), [Refund per metode](https://docs.midtrans.com/docs/what-payment-method-that-have-refund-feature), [Kriteria website](https://docs.midtrans.com/docs/what-are-the-website-or-application-criterias-for-registering-a-midtrans-account), [Tanpa website](https://docs.midtrans.com/docs/do-i-have-to-have-a-website-to-activate-my-midtrans-account), [Merchant vs Partner](https://docs.midtrans.com/docs/difference-between-merchant-account-and-partner-account), [Biaya QRIS Midtrans](https://docs.midtrans.com/docs/what-is-the-applicable-transaction-fee-for-qris), [Pencairan](https://docs.midtrans.com/docs/when-can-i-withdraw-my-transaction-funds-from-midtrans), [MDR QRIS BCA](https://www.bca.co.id/id/informasi/news-and-features/2026/09/01/18/50/informasi-penyesuaian-merchant-discount-rate-mdr-transaksi-qris), [Antara](https://www.antaranews.com/berita/5697901/bi-perluas-kebijakan-mdr-qris-0-persen-untuk-pelaku-umkm), [AWS experience baru](https://aws.amazon.com/about-aws/whats-new/2026/09/New-AWS-Builder-Experience), [AWS $0 2026](https://builder.aws.com/content/3Kkt4kNiDSUnO3n6db2weSNubzE/the-dollar0-aws-account-in-2026-free-plan-credits-guardrails), [AWS Agent Toolkit](https://raw.githubusercontent.com/aws/agent-toolkit-for-aws/refs/heads/main/setup-instructions/setup.md), [majoo](https://majoo.id/harga), [MyProfit](https://interactive.co.id/product/my-profit-pricing-subscription?lang=id), [MyResto](https://interactive.co.id/product/my-resto?lang=id), [Klikit Indonesia](https://klikit.io/en/pricing/indonesia), [Klikit Partner API](https://klikit.io/en/integrations/partner-api), [Harga WA Meta](https://developers.facebook.com/documentation/business-messaging/whatsapp/pricing), [ChatMaxima WA ID](https://chatmaxima.com/whatsapp-api-pricing/indonesia/), [PP 33/2026](https://siplawfirm.id/resources/pp-33-2026-pelindungan-data-pribadi), [Status Lembaga PDP](https://teknologi.bisnis.com/read/20261028/101/2016478/lembaga-pdp-sudah-harmonisasi-tinggal-tunggu-restu-prabowo), [PSE Komdigi](https://teknologi.bisnis.com/read/20260928/101/2007757/komdigi-beri-tenggat-1-oktober-7-pse-belum-terdaftar-terancam-kena-teguran), [Android verification](https://developer.android.com/developer-verification/guides), [Apple enrollment](https://developer.apple.com/support/enrollment), [Apple D-U-N-S](https://developer.apple.com/support/D-U-N-S/), [Apple Guidelines](https://developer.apple.com/support/downloads/terms/app-review-guidelines/App-Review-Guidelines-English-UK.pdf), [AEO](https://llmrefs.com/answer-engine-optimization).
