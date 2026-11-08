# Competitor Deep Research: InterActive Technologies (`interactive.co.id`)

Tanggal evaluasi: 2026-11-08 (Diverifikasi langsung via Playwright MCP pada 5 URL publik interactive.co.id)
Penyusun: Principal Systems Architect & Product Strategy (AzureDevID)
Benchmark pembanding sebelumnya: [CompetitorAnalysisMajoo.md](CompetitorAnalysisMajoo.md)

## 1. Executive Summary

InterActive Technologies Corp (**PT Interaktif Internasional**, berbasis di Surabaya sejak akhir 1990-an/2000-an) adalah pemain POS & hardware F&B/ritel mapan di Indonesia yang mengandalkan strategi **bundling perangkat keras + paket freemium pembuka + lisensi berbayar bertingkat per cabang/per perangkat**. Kekuatan utama mereka terletak pada kematangan integrasi perangkat kasir fisik (printer thermal, laci kasir RJ11, Android terminal dual-screen, kiosk mandiri) dan kredibilitas korporasi (PSE Komdigi, ISO, jaringan cabang kota besar).

Berdasarkan ekstraksi langsung Playwright hari ini pada seluruh halaman publik MyProfit:
1. **Model Lisensi Resmi InterActive MyProfit**:
   - **Free For Life (`Rp 0`)**: Cloud server, unlimited transaksi, unlimited cashier print, laporan harian, 2 slot user, QRIS dinamis, closing kasir, paperless receipt email.
   - **Basic**: Rp 149.000/bln (atau Rp 1.188.000/thn setara Rp 3.300/hari per lokasi). Fitur: Simple inventory, multi branch, multi warehouse, EDC integration, BI report.
   - **Pro**: Rp 249.000/bln (atau Rp 2.388.000/thn setara Rp 6.650/hari per lokasi). Fitur: Customer self-order QR multiple, dine-in, eat first / pay first, reservasi, Grab Food integration, multi floor.
   - **Enterprise**: Rp 299.000/bln (atau Rp 2.988.000/thn setara Rp 8.200/hari per lokasi). Fitur: Bill of Material (BOM), Purchasing, Table Management System.
   - **Add-On License (Per Perangkat / Per Bulan)**: Slot User tambahan (Rp 69k/bln), Kitchen Print Out (Rp 69k/bln), Kitchen Display System / KDS (Rp 69k/bln), Order Queue Monitor / TV (Rp 149k/bln), Complete Inventory (Rp 199k/bln), MyOrder Tablet (Rp 69k/bln), MyOrder Kiosk (Rp 199k/bln).
2. **Keterbatasan Utama Kompetitor**:
   - **Android-Centric**: Banyak fitur operasional hanya aktif di ekosistem Android ("berlaku pada aplikasi android", "Pay When Done Eat First on Android"), membatasi fleksibilitas kafe yang memakai iPad / laptop browser.
   - **Biaya Tersembunyi pada Add-on Device**: Fitur krusial seperti KDS dapur, antrean TV, dan printer dapur dikenakan biaya sewa bulanan tambahan per unit perangkat.
   - **White-Label Tidak Transparan**: Halaman white-label hanya berupa formulir prospek sales, tanpa transparansi harga dan arsitektur teknis.

---

## 2. Bedah Lini Produk & Software (`interactive.co.id/product/`)

| Lini Produk InterActive | Fungsi Utama | Analisis Arsitektur & Posisi MyCaffe (`mycaffe-backend` + `myCaffe-client`) |
|-------------------------|--------------|-----------------------------------------------------------------------------|
| **InterActive MyProfit** | POS Kasir Cloud & Ritel/F&B, manajemen stok, laporan penjualan, multi-cabang | Di MyCaffe sudah **Implemented & Tested** melalui modul POS Kasir (`internal/service/order_*.go`), Shift & Blind Cash Count (`shift_service.go`), Resep/Bahan Baku & HPP Moving-Average (`inventory_service.go`), Pembelian PO & Hutang Dagang (`payables`), serta Laporan Laba Kotor & Z-Report. |
| **InterActive MyResto** | Manajemen meja lantai restoran (pindah/gabung/pisah meja), open tab, kitchen routing, split bill | Di MyCaffe sudah **Implemented & Tested** pada v0.9 Cafe Floor (`table_service.go`, `tab_service.go`, timer meja, split bill per item & nominal parsial, serta KDS multi-stasiun real-time). |
| **InterActive QR Menu** | Scan QR meja oleh tamu untuk melihat menu dan mengirim pesanan ke kasir/dapur | Fokus rilis utama **MyCaffe QR Table Order v1.0**: token QR kriptografis per meja yang dapat dirotasi (`resolve_table_qr`), sesi tamu terbatas per perangkat, kuotasi harga/pajak/promo di sisi server, pembayaran QRIS Dinamis Core API tanpa redirect Snap, serta tracker pesanan publik via WebSocket. |
| **InterActive Self-Service Kiosk** | Layar sentuh berdiri (standing kiosk) untuk pesan & bayar mandiri di antrean depan | Dapat dijalankan langsung oleh aplikasi tablet/kiosk mode `myCaffe-client` menggunakan kontrak POS/Guest API yang sama dan antrean TV (`display_tokens`). |
| **InterActive BeOne / Backoffice** | Portal pemilik bisnis untuk memantau omzet cabang dan akuntansi dasar | Di MyCaffe ditingkatkan melalui **Tenant Self-Service Governance (`000051`)**: pemilik kafe mengatur sendiri CMS tema brand, BYOK Midtrans, SMTP/WhatsApp sendiri, dan topologi IP perangkat LAN tanpa harus menelepon teknisi vendor. |

---

## 3. Ekosistem Perangkat Keras & Topologi Jaringan Kompetitor

### 3.1 Perangkat Keras yang Dijual InterActive
- **Terminal POS Android All-in-One (`I-POS` Series / Sunmi OEM)**: Layar sentuh kasir + layar kedua menghadap pelanggan (*Customer Display*) + printer thermal internal 58mm/80mm.
- **Printer Dapur & Kasir**: Printer thermal ESC/POS 80mm dengan koneksi **LAN Ethernet (`TCP:9100`)** untuk dapur basah/panas (jarak kabel >15 meter jauh lebih stabil daripada Bluetooth) serta USB/Bluetooth untuk meja kasir.
- **Cash Drawer (Laci Uang)**: Terhubung melalui kabel **RJ11 (6-pin)** ke port `DK` di belakang printer thermal; laci terbuka otomatis melalui pulsa listrik perintah byte ESC/POS `ESC p m t1 t2` (`0x1B 0x70 0x00 0x19 0xFA`), bukan terhubung langsung ke tablet Android/iOS.
- **Barcode Scanner**: USB HID (bekerja sebagai emulasi papan ketik di web/Android) atau Bluetooth HID untuk scan SKU kemasan dan *staff ID badge*.
- **Kitchen Display & Queue TV**: Android TV / Smart TV atau mini-PC HDMI untuk menampilkan nomor antrean pesanan yang siap diambil.

### 3.2 Arsitektur Jaringan Kompetitor vs Strategi MyCaffe
- **Pola InterActive**: Aplikasi kasir lokal menyimpan master menu di penyimpanan perangkat, mencetak langsung ke IP LAN printer dapur (`192.168.1.x:9100`), dan menyinkronkan transaksi ke cloud saat internet tersedia. Kelemahan umumnya adalah **konflik perubahan IP router (DHCP drift)** yang membuat printer dapur tiba-tiba mati saat router ISP di-restart, memaksa pemilik kafe memanggil teknisi.
- **Keunggulan MyCaffe**:
  1. **Print Queue Terkelola + Direct LAN Bridge**: Mendukung agen cetak terautentikasi (`Authorization: Printer <token>`) sekaligus *Direct TCP:9100 Coroutines/NWConnection Adapter* di `myCaffe-client/apps/android` dan `apps/ios`.
  2. **Panel Self-Maintain Jaringan & Simulator Visual (`store_network_nodes`)**: Pemilik kafe dapat mendaftarkan IP statis/MAC perangkat, menguji skenario gangguan (*WAN Offline*, *Printer Paper Out*, *DHCP IP Drift*), dan mengikuti panduan perbaikan mandiri tanpa downtime menunggu teknisi.
  3. **Offline Cash & Static QRIS Sync (ADR-005)**: Tetap melayani kasir saat internet kafe mati dengan verifikasi PIN offline perangkat terdaftar, lalu rekonsiliasi aman saat koneksi kembali.

---

## 4. Branding, Model Harga, dan Legalitas Kompetitor

### 4.1 Branding & Positioning
- **InterActive**: Korporat biru-merah bergaya vendor B2B konvensional. Kuat untuk waralaba konvensional dan manajemen properti, tetapi terasa kaku bagi *specialty coffee shop*, *artisan bakery*, dan resto independen generasi baru yang menginginkan tampilan menu QR sekelas brand mereka sendiri (*white-label*).
- **MyCaffe by AzureDevID**: Mengusung arsitektur **Dual-Brand**:
  - **AzureDevID** (`Royal Azure #0A4DA2` & `Electric Cyan #00AEEF`) sebagai jaminan rekayasa sistem, keamanan data, penagihan SaaS, dan kepatuhan hukum.
  - **MyCaffe** (*Espresso & Crema* `#2C1A12` & `#C68B59`) sebagai identitas produk F&B hangat yang dapat diganti penuh (*custom hex palette, logo, hero banner, custom domain*) oleh masing-masing tenant melalui tabel `store_cms_configs`.

### 4.2 Perbandingan Skema Harga

| Aspek Biaya | InterActive MyProfit (Diverifikasi 2026-11-08) | MyCaffe by AzureDevID (Model Free-Core D6) |
|-------------|-----------------------------------------------|--------------------------------------------|
| Biaya Langganan Software | **Paket Free Rp 0** (fitur dasar, 2 user, tanpa KDS/TV)<br>**Basic Rp 149.000/bln** (inventori sederhana)<br>**Pro Rp 249.000/bln** (dine-in, QR multiple)<br>**Enterprise Rp 299.000/bln** (BOM, PO, meja) | **Free Core Selamanya (Rp 0)**: 1 outlet, staf & transaksi tanpa batas, QR tamu, POS, KDS dapur, dan 1 antrean TV sudah termasuk tanpa add-on tersembunyi. Upgrade dipicu pertumbuhan nyata (outlet ke-2+, volume pesan). |
| Biaya Add-On Perangkat | KDS Rp 69k/bln/device, TV Monitor Rp 149k/bln/device, Kitchen Print Rp 69k/bln/device | **Rp 0 / Open Multi-Device**: Bebas buka KDS dapur dan antrean TV di browser smart TV/tablet apa saja tanpa biaya lisensi per layar. |
| Komisi per Transaksi QR Order | Agregator pembayaran pihak ketiga dengan settlement berjangka | **0% Komisi Platform per Transaksi**: Skema BYOK Midtrans mencairkan dana 100% langsung ke rekening bank merchant. |
| Perangkat Keras | Bundling hardware khusus / Sunmi OEM | **Open-Standard Hardware**: Bebas pakai tablet/HP/printer thermal ESC/POS 58mm/80mm merek apa saja (Epson, Xprinter, Sunmi, dll). |
| Uji Coba & Onboarding | Registrasi free online atau sales meeting | **Self-Service Onboarding Instan**: Langsung aktif dengan dokumen hukum UU PDP & ToS terverifikasi SHA-256. |

### 4.3 Kepatuhan Hukum & Regulasi Indonesia
- Kompetitor besar berbadan hukum PT memiliki pendaftaran **PSE Lingkup Privat Komdigi**, syarat & ketentuan tertulis, serta pemisahan peran sebagai penyedia perangkat lunak kasir (bukan penyelenggara jasa pembayaran tanpa izin Bank Indonesia).
- **Langkah Kepatuhan AzureDevID (Implemented & Guarded)**:
  1. **Bebas Risiko Izin PJP Bank Indonesia**: Dengan memisahkan **Lane 1 (Midtrans AzureDevID khusus tagihan langganan SaaS)** dan **Lane 2 (Midtrans BYOK terenkripsi AES-256-GCM milik masing-masing Tenant untuk pesanan makan/minum)**, AzureDevID **tidak pernah menampung, memotong, atau meneruskan dana milik kafe klien**.
  2. **UU PDP No. 27 Tahun 2022 & UU KUP Pasal 28**: Persetujuan elektronik dicatat dengan jejak SHA-256 (`tenant_legal_acceptances`), kredensial dienkripsi (`secretbox`), dan prosedur penutupan akun (`tenant_clearance_requests`) menghapus kredensial/PII sambil mempertahankan buku besar transaksi selama 10 tahun untuk kepatuhan pajak.
  3. **Pendaftaran PSE Komdigi**: Dilengkapi playbook penyelesaian kendala OTP melalui SSO OSS RBA PB-UMKU untuk badan usaha perorangan ber-NIB.

---

## 5. Matriks Keunggulan Kompetitif Utama (Actionable Moat)

1. **Zero Fund Commingling (BYOK Midtrans Core API)**: Pemilik kafe memegang kendali penuh atas arus kas dan MDR resmi mereka sendiri tanpa ketergantungan pada pencairan dompet pihak ketiga.
2. **Keamanan Multi-Tenant Kelas Perbankan**: Isolasi *Row-Level Security* (RLS) PostgreSQL 18 dengan role `mycaffe_app` (`NOBYPASSRLS`) dan tidak adanya akses `DELETE` pada tabel buku besar keuangan.
3. **Self-Maintainable Cafe Infrastructure**: Pemilik atau kepala toko mampu mengganti IP printer LAN, merotasi QR meja yang bocor, menguji jaringan lokal via simulator visual, dan beralih ke mode kasir offline/QRIS Statis dalam hitungan detik saat gangguan ISP terjadi.

