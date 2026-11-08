# Official Brand Guidelines: Cassy by AzureDevID

**Brand Hierarchy**: Endorsed Dual-Brand (`Cassy by AzureDevID`)  
**Publisher & Legal Entity**: `AzureDevID` (NIB KBLI 62019 & 63122)  
**Technical Package & Bundle ID**: `com.azuredevid.cassy`  
**Brand Vault**: `C:\Users\Acer\OneDrive\Documents\AzureDevId`  
**Quality Compliance**: `antislop` Enforcement (No generic AI slop, no em-dashes, WCAG 2.2 AA, Tabular Numerics)

---

## 1. Executive Summary & Brand Architecture
Identitas visual dan produk Cassy dirancang untuk platform Point of Sale (POS), Kitchen Display System (KDS), pemesanan meja QR, dan operasional restoran F&B modern. Struktur merek menganut prinsip *Endorsed Dual-Brand*:

```
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

## 2. Palet Token Warna Resmi (WCAG 2.2 AA Compliant)

### A. Palet Korporat `AzureDevID` (Superadmin, Legal & Invoice B2B)
- `azure-primary`: `#0A4DA2` (Deep Royal Azure, Kontras 8.14:1 AAA di atas putih)
- `azure-cyan`: `#00AEEF` (Electric Cyan Accent, khusus aksen logo monogram dan highlight gelap)
- `azure-ink`: `#0B192C` (Executive Midnight Ink, teks utama dokumen legal dan invoice)
- `azure-obsidian`: `#07111F` (Latar belakang gelap Superadmin Console)
- `azure-surface`: `#F8FAFC` (Permukaan bersih kertas faktur)

### B. Palet Produk Default `Cassy` (*Espresso & Crema*)
- `caffe-espresso`: `#2C1A12` (Aksi primer dan judul, Rasio Kontras 14.2:1 AAA di atas canvas)
- `caffe-roast`: `#5C3A21` (Elemen interaktif sekunder, Rasio Kontras 8.9:1 AAA)
- `caffe-crema`: `#C68B59` (Aksen hangat dan border penanda aktif)
- `caffe-canvas`: `#FAF6F1` (Latar belakang hangat ramah mata kasir di tablet)
- `caffe-card`: `#FFFFFF` (Permukaan kartu elevasi dengan border tipis `1px solid #E6DDD3`)

### C. Status Operasional Semantik (POS, KDS, dan Pelacak Pesanan)
- `status-paid` / `status-ready`: `#166534` teks di atas `#DCFCE7` (Hijau Matcha)
- `status-pending` / `status-preparing`: `#9A3412` teks di atas `#FFEDD5` (Amber Roast)
- `status-failed` / `status-void`: `#991B1B` teks di atas `#FEE2E2` (Crimson Alert)

---

## 3. Tipografi & Penanganan Nilai Finansial
- **Heading & Merek**: `Plus Jakarta Sans` (Bobot 600, 700, 800), `letter-spacing: -0.02em`.
- **Antarmuka Operasional POS & Struk**: `Inter` (Bobot 400, 500, 600).
- **Aturan Tabular Numerics**: Seluruh nominal Rupiah (`Rp 28.000`), nomor antrean (`A-014`), timer KDS (`04:12`), dan stok wajib menggunakan `font-variant-numeric: tabular-nums` agar lebar karakter sejajar presisi dan antarmuka kasir tidak bergetar (*no layout shift*) saat nilai berubah.
- **Log Jaringan & IP Hardware**: `JetBrains Mono` (`500`).

---

## 4. Penegakan Kualitas Anti-Slop (`antislop`)
1. **Dilarang keras memakai tanda hubung panjang (*em-dash*, U+2014)** pada seluruh antarmuka, pesan error API, struk kasir, maupun buku panduan. Gunakan titik dua, tanda kurung, koma, atau kalimat ringkas.
2. **Dilarang menggunakan gradien ungu-biru AI atau glassmorphism buram** yang menurunkan keterbacaan di layar tablet kasir di bawah pencahayaan toko.
3. **Ukuran Target Sentuh Minimum**: Tombol angka PIN kasir, tombol tambah item `+` / `-`, dan tombol cetak wajib berukuran minimal **48x48 px** untuk mencegah salah tekan saat antrean sibuk.
4. **Pemisah Uang Rupiah**: Format angka rupiah baku adalah `Rp 150.000` dengan titik pemisah ribuan dan tanpa sen desimal `,00`.

---

## 5. Inventaris Aset Brand Vault (`C:\Users\Acer\OneDrive\Documents\AzureDevId\BrandAssetsLogo`)
- **Vektor Scalable**:
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

