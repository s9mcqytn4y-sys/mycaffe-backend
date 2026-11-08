# Product Requirements Document (PRD): Cassy Mobile POS & Tablet Suite (2025/2026 Benchmark)

**Product Name**: Cassy by AzureDevID  
**Technical Package & Bundle ID**: `com.azuredevid.cassy`  
**Target Hardware**: 10" s/d 14" Android Tablets (Sunmi, iMin, Advan, Samsung Galaxy Tab) & Apple iPad (10.9" s/d 12.9" Pro)  
**Standard Benchmarks**: Toast POS (Quick-Service Restaurant Master-Detail), Square POS (Slide-Over Modifier Sheets), Lightspeed Restaurant (High-Contrast Kitchen Display System)  
**Compliance**: `antislop-ui` Mode 1 (Zero AI Slop, No Em-Dashes, Pure SVG Icons, Tabular Numerics, WCAG 2.2 AA)

---

## 1. Executive Summary & Problem Statement

Sistem POS kasir restoran dan kafe di Indonesia sering mengalami hambatan kritis saat jam sibuk:
1. **Latensi Sentuhan & UI Bergoyang**: Harga dan nomor antrean bergeser saat nominal bertambah karena font tidak memiliki fitur tabular (`font-variant-numeric: tabular-nums`).
2. **Ketergantungan Internet untuk Cetak Struk**: Banyak sistem POS berbasis cloud gagal mencetak struk kasir dan tiket dapur ketika koneksi internet terputus.
3. **Kompleksitas Split Bill**: Pemisahan tagihan (*split bill*) memakan waktu lebih dari 2 menit per meja karena alur kasir yang kaku dan rawan salah hitung pembulatan Rupiah.
4. **Visual Terlalu Ramai / AI Generative Slop**: Penggunaan ilustrasi 3D glossy atau gradien kabur yang menurunkan keterbacaan di bawah lampu kafe yang terang.

**Solusi Cassy**:
Menghadirkan arsitektur tablet POS master-detail berkinerja tinggi, cetak langsung ke printer LAN thermal port 9100 tanpa cloud, pembagian tagihan deterministik dengan alokasi sisa pembulatan Rupiah otomatis, serta visual minimalist line vector 2D dengan kontras tinggi.

---

## 2. Layout Tablet Landscape Ergonomics (Toast Benchmark)

Layout antarmuka tablet kasir dirancang dengan rasio pembagian kolom master-detail:

```
┌──────────┬───────────────────────────────────────────┬─────────────────────────────────┐
│ Nav Rail │           Menu Catalog Grid (55%)         │    Persistent Order Check (35%) │
│   (10%)  │                                           │                                 │
├──────────┼───────────────────────────────────────────┼─────────────────────────────────┤
│ [Coffee] │  ┌──────────────┐   ┌──────────────┐     │  Meja 04 · ORD-0112 · Dine-in   │
│          │  │ Caffe Latte  │   │ Kopi Susu    │     │  ------------------------------ │
│ [Pastry] │  │ Rp 38.000    │   │ Rp 28.000    │     │  2x Caffe Latte       Rp 76.000 │
│          │  └──────────────┘   └──────────────┘     │   - Oat Milk (+Rp 6.000)        │
│ [Non-Cof]│  ┌──────────────┐   ┌──────────────┐     │  1x Croissant Butter  Rp 24.000 │
│          │  │ Croissant    │   │ Manual Brew  │     │  ------------------------------ │
│ [Merch]  │  │ Rp 24.000    │   │ Rp 35.000    │     │  Subtotal            Rp 106.000 │
│          │  └──────────────┘   └──────────────┘     │  PB1 (10%)            Rp 10.600 │
│          │                                           │  Total               Rp 116.600 │
│          │                                           ├─────────────────────────────────┤
│          │                                           │ [Split Bill]    [Bayar / Tender]│
└──────────┴───────────────────────────────────────────┴─────────────────────────────────┘
```

### A. Dimensi & Zona Sentuh Ergonomis
- **Navigation Rail Kiri (80–90px)**: Kategori produk vertikal dengan ikon SVG 24x24px, label 12px, dan jarak antar tombol 8px.
- **Menu Catalog Grid (Tengah)**: Kartu produk responsif 3 s/d 4 kolom. Minimum area kartu 160x120px dengan status stok real-time (`Sisa 3` amber chip atau `Habis` disabled overlay).
- **Persistent Order Check Kanan (360–400px)**: Tiket pesanan aktif selalu terlihat dari awal hingga bayar.
- **Zona Ibu Jari Bawah**: Tombol `Split Bill` (40% lebar) dan `Bayar / Tender` (60% lebar) ditempatkan di sudut kanan bawah dengan tinggi minimum 52px.

---

## 3. Spesifikasi Fungsional Modul Kritis

### A. Alur Modifiers & 1-Level Nested Sub-Addons (Square Benchmark)
1. Menekan produk dengan modifier wajib (contoh: *Espresso Single/Double*) langsung membuka Slide-Over Inspector Drawer dari sisi kanan (lebar 460px).
2. Opsi bertingkat (1-level nested sub-addons):
   - Memilih `Oat Milk (+Rp 6.000)` otomatis membuka accordion pilihan di bawahnya:
     - `[ ] Whipped Cold Foam (+Rp 3.000)`
     - `[ ] Extra Warm`
3. Seluruh penambahan harga modifier terhitung secara real-time pada tombol CTA `Tambah ke Pesanan (Rp 38.000)`.

### B. Mesin Split Bill Deterministik (Toast Canvas)
Cassy menyediakan 2 metode pemisahan tagihan:
1. **Split by Item / Seat**: Kasir memindahkan item pesanan ke sub-tagihan (*Bill 1*, *Bill 2*, *Bill 3*) via tap-to-move.
2. **Split Equal ($N$ Pembayar)**:
   - Pembagian nominal menggunakan rumus deterministik integer Rupiah:
     $$\text{base} = \lfloor \text{total} / N \rfloor, \quad \text{bill}_1 = \text{base} + (\text{total} - (\text{base} \times N))$$
   - Contoh: Total Rp 100.000 dibagi 3 orang:
     - Bill 1: **Rp 33.334** (menampung sisa pembulatan Rupiah)
     - Bill 2: **Rp 33.333**
     - Bill 3: **Rp 33.333**
   - Tidak ada sen desimal, mencegah selisih pembukuan kasir.

### C. Hardware Abstraction Layer (HAL) & Drawer Kick
1. **Socket TCP 9100 Non-blocking**: Komunikasi printer dapur dan kasir berjalan di background coroutine `Dispatchers.IO` (Android) dan Swift Actor (iOS).
2. **Perintah Pembuka Laci Kasir (*Cash Drawer*)**:
   - Saat transaksi Tunai selesai, perintah ESC/POS mengirimkan sinyal pulse pin 2 RJ-11:
     `0x1B, 0x70, 0x00, 0x19, 0xFA` (`ESC p 0 25 250`).

### D. Kitchen Display System (KDS) & SLA Urgensi (Lightspeed Benchmark)
1. **Header Tiket Kontras Tinggi**:
   - Status Baru (<10 menit): Header hitam arang `#1E1C1A` dengan teks putih.
   - Status Sedang Dibuat (10-20 menit): Border oranye amber `#D97706` dengan timer berkedip.
   - Status Terlambat / SLA Breached (>20 menit): Header merah menyala `#B91C1C` dengan peringatan audio berkala.
2. **Bump Bar Sentuh Cepat**: Tombol `Selesai / Bump` setinggi 60px di dasar setiap tiket dapur, dapat ditekan barista dalam <0.2 detik.

---

## 4. Standar Aset Visual Resmi & Penegakan Anti-Slop

### A. Direktori Aset Vektor Resmi (`BrandAssetsLogo/Cassy`)
- `CassyMonogramVector.svg`: Monogram interlock huruf `C` dan `S` dengan titik aksen Crema `#C68B59`.
- `CassyAppIconDarkSquircle.svg`: Ikon squircle obsidian `#07111F` untuk Play Store, App Store, dan PWA.
- `CassyLogoLightVector.svg`: Wordmark Cassy di atas latar terang (*Warm Alabaster Canvas* `#FAF6F1`).
- `CassyLogoDarkVector.svg`: Wordmark Cassy di atas latar gelap.

### B. Aturan Tipografi & Desain
1. **Wajib Mengaktifkan Tabular Numerics**: Seluruh nominal harga, nomor tiket, timer antrean, dan kuantitas menu wajib menggunakan `font-variant-numeric: tabular-nums`.
2. **Dilarang Menggunakan Ilustrasi 3D Clay / AI Bubbly Art**: Seluruh antarmuka operasional murni menggunakan ikon garis vektor 2D minimalis 24x24px dengan tebal garis 2px.
3. **Dilarang Menggunakan Em-Dash (—)**: Gunakan titik dua, tanda kurung, koma, atau kalimat ringkas.
4. **Target Sentuh Minimum 48x48px**: Sesuai Apple HIG dan WCAG 2.2 AA.
