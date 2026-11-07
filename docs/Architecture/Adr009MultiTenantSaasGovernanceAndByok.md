# ADR-009: Multi-Tenant SaaS Governance, Dual-Lane BYOK Midtrans, Unified Client Monorepo & Hardware Topology

- **Status**: Accepted & Implemented (Backend Migration `000051`, Client Foundation `MyCaffeClient`)
- **Date**: 2026-11-07
- **Deciders**: Abdul Aziz (Director / Product Owner, AzureDevID)

## 1. Context

Menjelang peluncuran komersial produk **MyCaffe QR Code Table Order & Cloud POS** di bawah entitas bisnis **AzureDevID**, terdapat tujuh celah tata kelola teknis, hukum, dan operasional yang harus ditutup secara menyeluruh:
1. Belum adanya skema database untuk dokumen hukum resmi (ToS, Kebijakan Privasi UU PDP No. 27/2022, DPA Mitra Usaha, Refund Policy), pendaftaran onboarding tenant mandiri, verifikasi bisnis (KYB), serta penutupan akun (*Account Clearance*) yang patuh aturan retensi buku besar pajak 10 tahun (UU KUP Pasal 28).
2. Kebutuhan memisahkan secara tegas rekening **Midtrans milik AzureDevID** (sebagai penyelenggara SaaS) dari **Midtrans milik masing-masing Kafe/Tenant**, agar dana penjualan makanan/minuman pelanggan tidak pernah singgah di rekening AzureDevID (menghindari klasifikasi agregator dana / PJP Bank Indonesia tanpa izin) dan aman saat salah satu akun sedang dalam tahap *Midtrans Production Business Review*.
3. Kebutuhan tenant untuk mengonfigurasi sendiri tema brand storefront (CMS), bahasa (`id-ID` dan `en-US`), kontras aksesibilitas WCAG 2.2 AA, serta kanal pengiriman struk/OTP mereka sendiri (Custom SMTP & WhatsApp).
4. Kebutuhan tenant untuk memelihara sendiri perangkat fisik di kafe (printer thermal ESC/POS LAN `TCP:9100`/USB/Bluetooth, laci uang RJ11, scanner, layar KDS, TV antrean, IoT buzzer) beserta simulasi visual topologi jaringan.
5. Kebutuhan konsol Superadmin bagi pemilik AzureDevID untuk memantau tenant, meninjau KYB/onboarding, mengeksekusi *clearance*, dan memverifikasi kesiapan *Midtrans Production Business Review*.
6. Penentuan struktur repositori aplikasi klien (Web PWA, Android Kotlin Jetpack Compose, iOS Swift/Objective-C) dan skema kompilasi Objective-C dari lingkungan pengembangan Windows 11.
7. Eksekusi bebas konflik (*zero merge conflict*) terhadap agen paralel yang sedang mengerjakan fitur Stamp Card (`000050_stamp_card`).

## 2. Decisions

### 2.1 Hierarki Dual-Brand: "MyCaffe by AzureDevID"
- **AzureDevID** (`Royal Azure #0A4DA2`, `Electric Cyan #00AEEF`) digunakan untuk seluruh dokumen korporat, kop surat resmi, perjanjian hukum, faktur langganan SaaS, dan Konsol Superadmin.
- **MyCaffe** (*Espresso & Crema* `#2C1A12`, `#C68B59`, `#FAF6F0`) menjadi tema bawaan aplikasi F&B yang dapat ditimpa per gerai melalui `store_cms_configs` (termasuk opsi penghapusan lencana *Powered by MyCaffe* pada paket *Enterprise White-Label*).

### 2.2 Isolasi Pembayaran Dua Jalur (Dual-Lane Midtrans Core API)
- **Lane 1 (AzureDevID Platform Billing)**: Dikonfigurasi melalui variabel lingkungan `PLATFORM_MIDTRANS_*` dan tabel `platform_invoices`. Hanya memproses pembayaran langganan bulanan/tahunan SaaS dari pemilik kafe ke AzureDevID dengan komisi transaksi 0% (`commission_bps = 0`). Tetap berjalan di Sandbox selama akun Midtrans Production AzureDevID berada pada status *Business Review*.
- **Lane 2 (Tenant BYOK Midtrans)**: Disimpan pada tabel `store_payment_credentials` yang dilindungi PostgreSQL 18 RLS. `server_key` dienkripsi menggunakan **AES-256-GCM** (`internal/pkg/secretbox`) dengan *Associated Authenticated Data* (AAD) terikat ke UUID gerai (`store:<store_id>:midtrans_server_key`) sehingga ciphertext tidak dapat dipindahkan antar-tenant.
- **Resolusi 2-Fase Tanpa Tahanan Lock DB**: `PaymentService.InitiatePayment` memanggil `GovernanceService.ResolveStoreGatewayClientTx` di dalam Fase 1 (`WithTenantTx`) untuk mendekripsi kunci di memori dan membangun instance `*midtrans.Client` terisolasi, lalu mengeksekusi panggilan HTTP `ChargeCoreAPI` / `CheckTransaction` sepenuhnya di luar transaksi database.
- **Webhook Per-Tenant**: Endpoint `POST /api/v1/payments/midtrans/webhook/:store_slug` memetakan `store_slug` melalui fungsi `resolve_store_for_login` yang sudah diizinkan, memverifikasi tanda tangan `SHA-512` secara *constant-time* memakai kunci BYOK tenant terkait, lalu meneruskan rekonsiliasi ke `ProcessMidtransWebhook`.

### 2.3 Kepatuhan Hukum, KYB, dan Tax-Safe Account Clearance
- Dokumen hukum disimpan berversi beserta sidik jari `SHA-256` pada `legal_documents`, dan bukti persetujuan pemilik gerai disimpan secara *append-only* pada `tenant_legal_acceptances`.
- Nomor sensitif (`NIB`, `NPWP`) disamarkan (`secretbox.MaskSecret`) sebelum disimpan di `tenant_onboarding_applications` maupun `store_kyb_profiles`.
- Prosedur penutupan akun (`tenant_clearance_requests`) menjalankan pemeriksaan pra-syarat ketat (menolak jika masih ada sesi shift kasir terbuka atau pesanan aktif/pending), menetapkan masa tenggang 7 hari (`cooling_off`), menonaktifkan kunci BYOK seketika, dan saat dieksekusi (`pii_anonymized_ledger_retained`) menimpa ciphertext kredensial serta membersihkan token SMTP/WhatsApp sambil mempertahankan tabel keuangan (`orders`, `payments`, `cash_movements`) selama 10 tahun sesuai UU KUP Pasal 28.

### 2.4 Self-Maintain Hardware Nodes & Visual Network Simulator
- Tabel `store_network_nodes` menyimpan inventaris perangkat lokal gerai (`escpos_printer`, `cash_drawer`, `pos_terminal`, `kds_screen`, `queue_tv`, `barcode_scanner`, `iot_buzzer`, `router_gateway`, `local_edge_server`) lengkap dengan IP lokal, port (`9100`), alamat MAC, lebar kertas (`58`/`80` mm), dan status pemicu laci kasir RJ11 (`drawer_kick_enabled`).
- Endpoint `POST /api/v1/store/network/simulate` melakukan pengukuran latensi database riil dan menyimulasikan lima skenario operasional (`healthy`, `wan_offline`, `printer_paper_out`, `lan_ip_conflict`, `midtrans_review`) yang terhubung langsung dengan simulator visual interaktif di `MyCaffeClient/apps/web/network-topology-simulator.html`.

### 2.5 Topologi Repositori Klien & Strategi Multiplatform
- Sesuai aturan batas repositori `AGENTS.md`, seluruh kode antarmuka pengguna ditempatkan pada monorepo terpisah **`C:\Projects\MyCaffeClient`** dengan pembagian ruang kerja:
  - `packages/design-tokens`: Token warna, tipografi, kontras WCAG 2.2 AA terverifikasi otomatis, dan kamus lokalisasi `id-ID` / `en-US` bebas *em-dash*.
  - `apps/web`: Aplikasi Web PWA (QR Table Order tamu, KDS, Antrean TV, Backoffice Owner, Simulator Topologi Jaringan).
  - `apps/android`: Aplikasi kasir native **Kotlin + Coroutines + Jetpack Compose** dengan jembatan cetak termal `EscPosHardwareBridge.kt` (`TCP:9100` + kick laci `ESC p`).
  - `apps/ios`: Modul native iOS owner/staff yang direncanakan menggunakan **SwiftUI** modern (Keputusan D2). Jembatan Objective-C warisan (`MyCaffeHardwareBridge.h/.m`) diarsipkan dan tidak lagi dijadikan target utama kompilasi. Pembuatan bundel resmi `.ipa` dan penandatanganan kode Apple akan dieksekusi pada runner GitHub Actions **`macos-15` (Xcode 16.2)** yang di-pin dengan SHA komit setelah akun Apple Developer resmi tersedia.

### 2.6 Strategi Isolasi Git terhadap Agen Paralel
- Seluruh perubahan backend dibangun di *git worktree* terpisah (`C:\Projects\mycaffe-wt-platform`, cabang `feat/platform-saas-governance`) menggunakan nomor migrasi `20261107000051_platform_saas_governance` dan tabel audit tata kelola tersendiri (`governance_audit_logs`) sehingga tidak menyentuh `chk_audit_logs_action` maupun kelima file layanan kasir yang sedang dimodifikasi oleh agen paralel pada `000050_stamp_card`.

## 3. Consequences

- **Positif**:
  - AzureDevID terlindungi dari risiko hukum penampungan dana pihak ketiga (BI PJP) dan siap menghadapi audit *Midtrans Production Business Review* maupun kepatuhan UU PDP No. 27/2022.
  - Tenant mendapatkan otonomi penuh (*self-service*) atas pembayaran, branding, notifikasi, dan pemeliharaan perangkat jaringan lokal kafe.
  - Tidak ada penambahan fungsi `SECURITY DEFINER` baru di luar daftar putih `AGENTS.md`.
- **Trade-off & Mitigasi**:
  - Kunci `CREDENTIAL_ENCRYPTION_KEY` pada environment produksi wajib dicadangkan di pengelola rahasia pemilik; kehilangan kunci tersebut mengharuskan tenant memasukkan ulang Server Key Midtrans mereka (data pesanan historis tetap utuh).
  - Untuk tenant yang memilih *Starter UMKM* (tanpa BYOK Midtrans Dinamis), pembayaran meja QR dan kasir otomatis diarahkan menggunakan QRIS Statis Gerai, Tunai, atau EDC dengan verifikasi kasir.

