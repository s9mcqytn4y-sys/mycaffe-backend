# Roadmap Lanjutan Fase 2 (Dipisahkan dari Iterasi Stabilisasi Produk Pertama)

Untuk menjaga agar **Produk Pertama MyCaffe (`online.abbdullaziz.mycaffe`)** benar-benar stabil, aman secara hukum, dan siap didemokan secara percaya diri kepada klien tanpa membebani iterasi saat ini, fitur-fitur ekspansi berikut secara resmi dipisahkan ke dalam **Roadmap Fase 2**.

## Batas Iterasi Saat Ini (Fase 1 - Stabilisasi & Kesiapan Demo Klien)
Cakupan yang **sudah aktif dan distabilkan pada Fase 1**:
1. **Go Modular Monolith Backend + PostgreSQL 18 RLS (`mycaffe-backend`)**: Isolasi multi-tenant ketat, katalog kosong (`0` dummy tenant pada `development.sql`), Onboarding ketat wajib verifikasi **6-digit Email OTP**, aktivasi akun tenant *unverified* untuk uji coba Midtrans Sandbox BYOK, penguncian Midtrans Production BYOK pada `kyb_status = 'verified'`, rekonsiliasi webhook Midtrans `200 OK` di kedua jalur (Lane 1 SaaS Billing & Lane 2 Store Orders), serta generator otomasi **MikroTik RouterOS v7 (`.rsc`) + panduan Winbox**.
2. **Responsive Web / Tablet / iPad PWA (`MyCaffeClient/apps/web`)**: Mencakup Landing & Harga IDR transparan, Onboarding + OTP, Guest QR Table Ordering (`/order/[token]`), Kasir POS, Layar KDS, Layar Antrean TV, Tata Kelola Gerai Mandiri (BYOK Midtrans SDK resmi + kesiapan dompet digital Apple Pay / Google Pay sesuai regulasi, Notifikasi SMTP/WA, RBAC, Jaringan MikroTik, Penyimpanan R2 + IndexedDB, Penutupan Akun Pajak-Aman), serta Konsol Superadmin AzureDevID.
3. **Aplikasi Native Android (`MyCaffeClient/apps/android` - `online.abbdullaziz.mycaffe`)**: Aplikasi Jetpack Compose native untuk ponsel & tablet kasir Android beserta cetak thermal Bluetooth/LAN dan pengelolaan kredensial gerai.

---

## Cakupan Roadmap Lanjutan (Fase 2 - Pasca Akuisisi Klien Pertama & VPS Produksi)

| Modul Fase 2 | Target Platform / Teknologi | Alasan Pemisahan dari Fase 1 | Prasyarat Eksekusi |
| :--- | :--- | :--- | :--- |
| **1. Aplikasi Desktop Kasir Native (Windows / macOS / Linux)** | `Tauri v2` (Rust Core + WebView2/WKWebView) dengan akses USB HID/Raw ESC/POS langsung tanpa browser bridge | PWA pada tablet/iPad dan PC sudah memenuhi seluruh alur kasir & cetak LAN `print-agent` untuk demo dan produksi awal. | Sertifikat *Code Signing* Windows/Apple agar installer tidak memicu peringatan SmartScreen. |
| **2. Rilis Binary iOS App Store Penuh (`ipa`)** | SwiftUI + Objective-C Hardware Bridge (`apps/ios`, `online.abbdullaziz.mycaffe`) | Kode sumber bridge Objective-C sudah disiapkan di repo, namun kompilasi `.ipa` dan distribusi TestFlight membutuhkan mesin macOS/Xcode Runner dan akun Apple Developer Program aktif (\$99/tahun). Pada iPad saat ini, PWA Safari layar penuh sudah berjalan optimal. | Akun Apple Developer Organization/Individual aktif & sertifikat Apple Pay Merchant ID resmi. |
| **3. Apple Pay & Google Pay Produksi Langsung (Direct Tokenization)** | W3C `PaymentRequest` + Midtrans Card/Wallet Acquiring | Di Indonesia, pemrosesan langsung Apple Pay / Google Pay bergantung pada persetujuan *acquiring bank* kartu internasional pada akun Midtrans Production yang saat ini masih dalam tahap *Business Review*. Deteksi kesiapan perangkat sudah aktif di klien. | Persetujuan final Midtrans Production Business Review + aktivasi kanal Card/Google Pay oleh Midtrans. |
| **4. Multi-Cabang Terpusat (Central Kitchen & Inter-Store Transfer)** | Modul Gudang Pusat & Konsolidasi Laporan Grup | Klien perdana fokus pada stabilitas gerai tunggal/mandiri terlebih dahulu sebelum manajemen stok antar-cabang. | Minimal 1 klien multi-outlet aktif di produksi. |
| **5. Integrasi Akuntansi Eksternal (Jurnal.id / Accurate Online)** | Webhook & Penjadwalan Jurnal Harian Z-Report Otomatis | Buku besar internal dan ekspor CSV sudah mencukupi kebutuhan pelaporan pajak awal UMKM/Kafe. | Kredensial sandbox partner API dari vendor akuntansi terkait. |
| **6. Driver IoT Lanjutan (Timbangan Digital Serial & Pager Meja RF)** | SerialPort / MQTT Local Broker pada Edge Node | Perangkat keras utama kafe (Printer ESC/POS, Cash Drawer RJ11, Barcode Scanner HID, Layar KDS/TV) sudah terdukung penuh. | Sampel unit fisik timbangan/pager dari klien spesifik. |
