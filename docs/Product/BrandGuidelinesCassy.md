# Official Brand Guidelines: Cassy by AzureDevID

**Brand Hierarchy**: Endorsed Dual-Brand (`Cassy by AzureDevID`)  
**Publisher & Legal Entity**: `AzureDevID` (NIB KBLI 62019 & 63122)  
**Technical Package & Bundle ID**: `com.azuredevid.cassy`  
**Brand Vault**: `C:\Users\Acer\OneDrive\Documents\AzureDevId`  
**Quality Compliance**: `antislop` Enforcement (No generic AI slop, no em-dashes, WCAG 2.2 AA, Tabular Numerics)

---

## 1. Executive Summary & Brand Architecture

Identitas visual dan produk Cassy dirancang untuk platform Point of Sale (POS), Kitchen Display System (KDS), pemesanan meja QR, dan operasional restoran F&B modern. Struktur merek menganut prinsip *Endorsed Dual-Brand*:

```text
┌──────────────────────────────────────────────────────────────┐
│                    AzureDevID (Corporate)                    │
│     Badan Usaha Legal, SaaS Platform, Penagihan B2B          │
│     KBLI 62019 / 63122 · Midtrans Corporate Account          │
└──────────────────────────────┬───────────────────────────────┘
                               │
                               ▼
┌──────────────────────────────────────────────────────────────┐
│                  Cassy (Product Brand)                       │
│     Perangkat Lunak F&B POS, KDS, & QR Table Suite           │
│     Bundle ID: com.azuredevid.cassy                          │
└──────────────────────────────┬───────────────────────────────┘
                               │
                               ▼
┌──────────────────────────────────────────────────────────────┐
│               Tenant Override (White-Label)                  │
│     Nama Gerai/Kafe, Logo Kustom, Warna Primer/Sekunder      │
│     Struk Pembelian, Banner Promosi                          │
└──────────────────────────────────────────────────────────────┘
```

---

## 2. Palet Token Warna Resmi (WCAG 2.2 AA)

Setiap kombinasi warna teks dan latar dirancang untuk menjaga keterbacaan pada layar tablet kasir maupun dokumen cetak resmi.

### A. Palet Korporat AzureDevID (Superadmin, Legal, dan Invoice B2B)

Palet ini dipakai khusus untuk portal pengelola pusat, invoice langganan B2B, serta dokumen korespondensi resmi perusahaan:

- `azure-primary`: `#0A4DA2` sebagai warna Deep Royal Azure utama yang mencapai tingkat kontras AAA saat ditampilkan di atas latar putih.
- `azure-cyan`: `#00AEEF` sebagai aksen Electric Cyan pada monogram logo.
- `azure-ink`: `#0B192C` sebagai Executive Midnight Ink untuk teks legal, tabel rincian biaya, serta header faktur.
- `azure-obsidian`: `#07111F` sebagai latar belakang utama Superadmin Console agar nyaman dipantau lama.
- `azure-surface`: `#F8FAFC` sebagai warna dasar kertas faktur serta kartu ringkasan bersih.

### B. Palet Produk Default Cassy (PRD v4 - Pandan dan Kunyit)

- `cassy-pandan` (primary): `#1F5F52` (Aksi primer, header, CTA kasir; kontras tinggi di atas putih tulang)
- `cassy-kunyit` (accent): `#E9A23B` (Sorotan aktif, lencana promo, indikator status hangat)
- `cassy-canvas` (background): `#F7F5F0` (Putih tulang hangat ramah mata kasir & tamu)
- `cassy-tinta` (ink): `#17211E` (Teks utama tajam tanpa nuansa cokelat)
- `cassy-card` (surface): `#FFFFFF` (Permukaan kartu elevasi dengan border tipis `1px solid #E2DDD4`)

### C. Status Operasional Semantik (POS, KDS, dan Pelacak Pesanan)

- `status-paid` / `status-ready`: `#1F5F52` teks di atas `#E6F2EF` (Hijau Pandan)
- `status-pending` / `status-preparing`: `#6E4409` teks di atas `#FEF6E6` (Kunyit Hangat)
- `status-failed` / `status-void`: `#991B1B` teks di atas `#FEE2E2` (Crimson Alert)

---

## 3. Tipografi dan Penanganan Nilai Finansial

Gaya tipografi dan format numerik diseragamkan agar label nominal serta nomor antrean tetap rapi di seluruh perangkat:

- **Brand Display**: `Bricolage Grotesque` (Bobot 600, 700, 800) dengan `letter-spacing: -0.02em` untuk judul identitas merek.
- **Antarmuka Produk, POS & Struk**: `Plus Jakarta Sans` (Bobot 400, 500, 600, 700) agar mudah dibaca cepat saat jam sibuk.
- **Aturan Tabular Numerics**: Seluruh nominal Rupiah (`Rp 28.000`), nomor antrean (`A-014`), timer KDS (`04:12`), dan jumlah stok wajib menggunakan `font-variant-numeric: tabular-nums` supaya lebar karakter sejajar presisi dan antarmuka kasir tidak bergetar (*no layout shift*) ketika nilai berubah.
- **Log Jaringan & IP Hardware**: `JetBrains Mono` dengan bobot `500` untuk alamat IP printer maupun kode diagnostik.

---

## 4. Penegakan Kualitas Anti-Slop (antislop)

1. **Dilarang keras memakai tanda hubung panjang (*em-dash*, U+2014)** pada seluruh antarmuka, pesan error API, struk kasir, maupun buku panduan. Gunakan titik dua, tanda kurung, koma, atau kalimat ringkas.
2. **Dilarang menggunakan gradien ungu-biru AI atau glassmorphism buram** yang menurunkan keterbacaan di layar tablet kasir di bawah pencahayaan toko.
3. **Ukuran Target Sentuh Minimum**: Tombol angka PIN kasir, tombol tambah item `+` / `-`, dan tombol cetak wajib berukuran minimal **48x48 px** untuk mencegah salah tekan saat antrean sibuk.
4. **Pemisah Uang Rupiah**: Format angka rupiah baku adalah `Rp 150.000` dengan titik pemisah ribuan dan tanpa sen desimal `,00`.

---

## 5. Inventaris Aset Brand Vault

Seluruh aset logo resmi dan dokumen legal tersimpan pada direktori `C:\Users\Acer\OneDrive\Documents\AzureDevId`:

- **Aset Vektor Resolusi Tinggi**:
  - `BrandAssetsLogo/AzureDevIdMonogramVector.svg`
  - `BrandAssetsLogo/AzureDevIdLogoLightVector.svg`
  - `BrandAssetsLogo/AzureDevIdLogoDarkVector.svg`
- **Raster Transparan**:
  - `BrandAssetsLogo/AzureDevIdLogoLightTransparent.png` (Kop surat & invoice)
  - `BrandAssetsLogo/AzureDevIdLogoDarkTransparent.png` (Latar gelap)
  - `BrandAssetsLogo/AzureDevIdMonogramTransparent.png` (Monogram simbol)
  - `BrandAssetsLogo/AzureDevIdAppIconDarkSquircle.png` (Ikon aplikasi launcher mobile)
- **Dokumen Legal & Kop Surat**:
  - `BrandAssetsLogo/KopSurat/KopSuratResmiAzureDevId.docx`
  - `LegalAndBrandGuidelines/ChecklistMidtransBusinessReviewProd.md`
  - `LegalAndBrandGuidelines/TermsOfServiceDanDpaMyCaffe.md`
