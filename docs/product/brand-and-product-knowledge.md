# MyCaffe: Pedoman Identitas Merek, Sistem Desain, dan Product Knowledge

Dokumen ini menetapkan pedoman identitas merek internal, token desain visual default, matriks aksesibilitas kontras warna WCAG 2.2 AA, model operasional deployment, konvensi penomoran entitas, serta pengetahuan produk inti (product knowledge) untuk platform **MyCaffe**. Walaupun setiap tenant/toko dapat melakukan kustomisasi white-label (logo, warna aksen, nama toko, dan pengaturan struk), platform inti wajib memiliki identitas standar yang jelas, teruji, dan siap pakai sejak instalasi pertama.

> [!NOTE]
> **Status Hukum Merek & Hak Kekayaan Intelektual (HKI):**
> Nama **MyCaffe** dalam dokumen dan repositori ini ditetapkan sebagai *working brand* dan standar desain produk internal. Istilah ini tidak menyatakan klaim bahwa merek dagang telah terdaftar secara hukum. Sebelum peluncuran komersial publik, pemilik produk wajib melakukan penelusuran ketersediaan merek pada Pangkalan Data Kekayaan Intelektual (PDKI) Direktorat Jenderal Kekayaan Intelektual (DJKI) Kementerian Hukum RI pada **Kelas 9** (perangkat lunak POS/aplikasi kasir), **Kelas 42** (Software as a Service / penyediaan perangkat lunak berbasis cloud), dan **Kelas 35** (manajemen bisnis/administrasi ritel), serta mendaftarkan merek kata dan logo resmi melalui DJKI. Hak atas kode sumber, dokumentasi teknis, dan desain sistem dilindungi sebagai Hak Cipta (Copyright) dan Rahasia Dagang (Trade Secret).

---

## 1. Identitas Merek, Positioning, dan 3 Model Deployment

| Atribut | Ketentuan Standar Platform |
| :--- | :--- |
| **Nama Merek Kerja (Working Brand)** | **MyCaffe** (Sub-sistem backend: **MyCaffe Core POS**) |
| **Tagline Resmi** | *Operasional kafe yang rapi, dari meja sampai laporan.* |
| **Positioning Produk** | Platform POS, QR Table Ordering, Kitchen Display System (KDS), TV Queue Display, dan Owner Backoffice berarsitektur multi-tenant (`store_id` + PostgreSQL 18 Row-Level Security) yang fleksibel dijalankan pada satu VPS mandiri maupun multi-cabang. |
| **Target Pengguna Utama** | Specialty coffee shop, kafe independen, jaringan gerai kopi multi-cabang, artisan bakery, dan restoran kasual di Indonesia. |
| **Bahasa Antarmuka** | Bahasa Indonesia alami untuk seluruh antarmuka pelanggan, kasir, barista, dapur, dan pemilik usaha. Identifier kode dan skema database menggunakan Bahasa Inggris. |

### 1.1 Tiga Model Deployment dan Lisensi Operasional

Kode aplikasi MyCaffe dibangun di atas fondasi isolasi `store_id` (UUID) dan PostgreSQL 18 `FORCE ROW LEVEL SECURITY`, sehingga satu basis kode yang sama mendukung 3 model bisnis dan topologi deployment tanpa menulis ulang aplikasi:

| Model Deployment | Target Pelanggan | Topologi Infrastruktur | Isolasi Data & Kredensial |
| :--- | :--- | :--- | :--- |
| **Model A: Dedicated Single-VPS per Merchant (Target MVP)** | Kafe independen tunggal yang menginginkan kendali penuh dan biaya bulanan tetap tanpa langganan database PaaS. | 1 VPS Hostinger KVM 2 (`2 vCPU`, `8 GB RAM`, `100 GB NVMe`) milik merchant yang menjalankan Docker Compose (`postgres:18-alpine`, `api`, `web`, `nginx`, `certbot`). | 1 toko utama (`store_id`) di dalam database VPS tersebut, dengan cadangan terenkripsi ke bucket Cloudflare R2 terisolasi. |
| **Model B: Multi-Outlet Single-Merchant** | Jaringan kafe/restoran dengan 2 hingga 20 cabang di bawah satu pemilik merek yang sama. | 1 VPS terpusat milik grup merchant (dapat ditingkatkan kapasitas CPU/RAM sesuai jumlah cabang). | Setiap cabang memiliki `store_id` terpisah dengan konfigurasi pajak, service charge, prefix nomor pesanan, dan laporan per cabang maupun konsolidasi pemilik. |
| **Model C: Managed Multi-Tenant SaaS** | Banyak merchant berbeda yang berlangganan bulanan pada infrastruktur yang dikelola penyedia platform MyCaffe. | Cluster VPS terkelola dengan pemisahan beban kerja, monitoring terpusat, dan manajemen kredensial pembayaran per tenant (`store_id`). | Isolasi ketat antar-merchant menggunakan PostgreSQL 18 RLS (`app.current_store_id`), kunci API per tenant, serta kuota penyimpanan media per `store_id`. |

### 1.2 Tone of Voice per Persona Pengguna

Setiap permukaan produk memiliki kondisi kerja dan tingkat tekanan waktu yang berbeda. Microcopy antarmuka wajib mengikuti aturan nada komunikasi berikut:

1. **Pelanggan (QR Table Ordering & E-Receipt):**
   - **Nada:** Ramah, ringkas, membantu, dan memberikan kepastian status pesanan.
   - **Contoh Baik:** *"Pembayaran Rp 57.500 diterima. Pesanan Anda masuk antrean A012 dan sedang disiapkan barista."*
   - **Contoh Buruk (Dilarang):** *"Webhook settlement berhasil memutakhirkan status transaksi ke QUEUED."*
2. **Kasir & Staf Operasional (POS, Dual-Screen, KDS Barista & Dapur):**
   - **Nada:** Singkat, tegas, dapat dipindai dalam `< 1 detik`, dan langsung menyebutkan tindakan yang harus dilakukan.
   - **Contoh Baik:** *"Stok Oat Milk sisa 2 porsi."* / *"Pembayaran QRIS kedaluwarsa. Buat QR baru atau ganti metode bayar."*
3. **Pemilik Usaha (Owner Backoffice & Laporan Keuangan):**
   - **Nada:** Lugas, berbasis angka bulat yang presisi, transparan menjelaskan selisih kas atau biaya MDR, dan siap diaudit.
   - **Contoh Baik:** *"Penjualan kotor Rp 4.250.000 | Estimasi MDR QRIS (0,70%) Rp 18.900 | Selisih kas fisik Rp 0 (Seimbang)."*

---

## 2. Sistem Desain, Token Visual Default (Espresso & Crema), dan Standar WCAG 2.2 AA

Ketika tenant baru dibuat dan belum mengunggah kustomisasi warna sendiri, aplikasi menerapkan palet **Espresso & Crema**. Seluruh pasangan warna teks dan latar belakang wajib memenuhi standar aksesibilitas **WCAG 2.2 Level AA** (rasio kontras minimum `4.5:1` untuk teks normal dan `3.0:1` untuk teks besar `>= 18pt` atau `>= 14pt bold`, serta ukuran target sentuh minimum `44x44px` di ponsel pelanggan dan `48x48px` di layar sentuh POS/KDS).

### 2.1 Palet Warna Inti dan Pasangan Kontras Terverifikasi (WCAG 2.2 AA)

> [!IMPORTANT]
> **Aturan Penggunaan Warna Aksen & Status:**
> Warna `#C87D55` (*Warm Crema*), `#16A34A` (*Botanical Mint*), dan `#D97706` (*Amber Roast*) adalah warna aksen/indikator visual dan **dilarang** digunakan sebagai latar belakang untuk teks putih berukuran kecil karena rasio kontrasnya terhadap `#FFFFFF` berada di bawah `4.5:1`. Gunakan varian gelap (*Dark Text/Fill Pair*) pada tabel di bawah ini untuk teks atau tombol dengan teks putih.

| Peran Token | Nama Warna | Hex Code | Pasangan Latar / Teks yang Diizinkan | Rasio Kontras | Status WCAG 2.2 |
| :--- | :--- | :--- | :--- | :--- | :--- |
| **Primary Brand** | Deep Espresso | `#2C1810` | Teks `#FFFFFF` di atas `#2C1810`, atau teks `#2C1810` di atas `#FAF7F2` / `#FFFFFF` | `14.8:1` (vs `#FAF7F2`) / `16.1:1` (vs `#FFFFFF`) | **AAA** (`>= 7.0:1`) |
| **Brand Accent (Decorative / Border)** | Warm Crema | `#C87D55` | Hanya untuk border aktif (`2px`), badge background dengan teks `#2C1810`, atau ikon non-teks di atas `#12100E` | `4.8:1` (teks `#2C1810` di atas `#EED4C5`) / `5.6:1` (di atas `#12100E`) | **AA** |
| **Brand Accent Text (Accessible Link/CTA)** | Roasted Copper | `#8C4A27` | Teks aksen atau tombol sekunder dengan teks `#FFFFFF` di atas `#8C4A27`, atau teks `#8C4A27` di atas `#FAF7F2` | `5.8:1` (vs `#FFFFFF`) / `5.3:1` (vs `#FAF7F2`) | **AA** (`>= 4.5:1`) |
| **Surface Light** | Warm Milk / Alabaster | `#FAF7F2` | Latar belakang utama QR Table Ordering & Backoffice dengan teks utama `#2C1810` dan teks sekunder `#5C4940` | `14.8:1` (`#2C1810`) / `7.2:1` (`#5C4940`) | **AAA** |
| **Surface Card** | Pure Porcelain | `#FFFFFF` | Kartu produk, panel keranjang, dan struk digital dengan teks `#2C1810` | `16.1:1` | **AAA** |
| **Surface Dark (KDS & TV)** | Roasted Charcoal | `#12100E` | Latar belakang mode gelap KDS & TV Queue dengan teks utama `#FAF7F2` dan teks sekunder `#C7BEB8` | `16.9:1` (`#FAF7F2`) / `9.8:1` (`#C7BEB8`) | **AAA** |
| **Status: Success / Ready** | Botanical Mint (Icon) / Forest Green (Text & Solid Badge) | Icon: `#16A34A` / Text & Badge: `#14532D` (di atas `#DCFCE7` atau `#FFFFFF`) | Teks `#14532D` di atas latar `#DCFCE7` (badge terang), atau teks `#FFFFFF` di atas `#15803D` | `8.4:1` (`#14532D` vs `#DCFCE7`) / `4.9:1` (`#FFFFFF` vs `#15803D`) | **AA / AAA** |
| **Status: Warning / Preparing** | Amber Roast (Icon) / Dark Amber (Text & Solid Badge) | Icon: `#D97706` / Text & Badge: `#78350F` (di atas `#FEF3C7` atau `#FFFFFF`) | Teks `#78350F` di atas latar `#FEF3C7` (badge terang), atau teks `#12100E` di atas `#F59E0B` (KDS Dark Mode) | `8.9:1` (`#78350F` vs `#FEF3C7`) / `9.1:1` (`#12100E` vs `#F59E0B`) | **AAA** |
| **Status: Danger / SLA Breach** | Crimson Berry (Solid) / Deep Crimson (Text) | Solid: `#B91C1C` / Text: `#991B1B` (di atas `#FEE2E2`) | Teks `#FFFFFF` di atas `#B91C1C` (tombol bahaya/void), atau teks `#991B1B` di atas `#FEE2E2` (alert banner) | `6.4:1` (`#FFFFFF` vs `#B91C1C`) / `7.6:1` (`#991B1B` vs `#FEE2E2`) | **AA / AAA** |

> [!TIP]
> **Indikator Non-Warna (Color-Blindness Safety):**
> Status pesanan dan pembayaran tidak pernah mengandalkan warna semata. Setiap status wajib menyertakan **ikon eksplisit + label teks Bahasa Indonesia** (contoh: `[✓ Lunas]`, `[⏳ Menunggu Pembayaran]`, `[🔥 Sedang Disiapkan]`, `[! Kedaluwarsa]`).

### 2.2 Tipografi Standar

1. **UI dan Katalog (`Plus Jakarta Sans`):**
   - Digunakan untuk judul halaman, nama produk, deskripsi menu, dan tombol navigasi.
   - Memiliki keterbacaan tinggi pada layar ponsel berukuran kecil hingga layar sentuh kasir.
2. **Angka, Harga, Nomor Antrean, dan Timer KDS (`JetBrains Mono` dengan `font-variant-numeric: tabular-nums`):**
   - Wajib digunakan pada seluruh nominal Rupiah (`Rp 24.000`), nomor pesanan (`ORD-20261029-0001`), nomor antrean (`A001`), dan penghitung waktu (timer) di KDS.
   - Penggunaan `tabular-nums` memastikan lebar setiap digit angka identik sehingga kolom harga di kasir dan laporan keuangan selalu sejajar vertikal dan tidak bergoyang saat timer berjalan.

---

## 3. Standar Format ID dan Penomoran Entitas

Seluruh identitas statis dari prototipe awal telah dihapus sepenuhnya dan digantikan oleh identitas multi-tenant generik. MyCaffe menerapkan pemisahan tegas antara **Primary Key internal** dan **Nomor Operasional yang ramah manusia**:

| Entitas | Format Internal (DB PK) | Format Operasional Default | Aturan Kustomisasi & Reset |
| :--- | :--- | :--- | :--- |
| **Store (Tenant)** | `UUID v4` (`stores.id`) | Slug URL: `central-cafe`, `express-counter` | Ditetapkan per cabang/toko. |
| **Order Number** | `UUID v4` (`orders.id`) | `{ORDER_PREFIX}-{YYYYMMDD}-{XXXX}` (Contoh: `ORD-20261029-0001`) | `order_prefix` dapat dikustomisasi per toko (mis. `ORD`, `EXP`). Nomor urut (`XXXX`) dialokasikan secara atomik per tanggal bisnis toko (`stores.timezone`) melalui tabel `store_daily_sequences`. |
| **Queue Number** | Disimpan pada `orders.queue_number` | `{QUEUE_PREFIX}{XXX}` (Contoh: `A001`, `B001`) | `queue_prefix` dapat dikustomisasi per toko. Reset otomatis kembali ke `001` setiap pergantian tanggal bisnis lokal toko. |
| **Payment Provider ID** | `UUID v4` (`payments.id`) | UUID payment attempt yang sama dengan `provider_order_id` | Menjamin keunikan global per provider pada akun gateway bersama; `order_number` tetap menjadi nomor operasional untuk pelanggan, bukan ID gateway. |
| **Staff ID Card** | `UUID v4` (`users.id`) | `STF-{STORE_SLUG_UPPER}-{XXXX}` | Digunakan untuk pencetakan kartu identitas staf (ID Card) beserta PIN operasional kasir. |

---

## 4. Product Knowledge & Kapabilitas Operasional

### 4.1 Empat Peran Staf Operasional (Tanpa Birokrasi Approval Bertingkat)
Operasional kafe membutuhkan kecepatan tinggi di jam sibuk. MyCaffe sengaja membatasi peran pengguna menjadi **4 peran inti** (`owner`, `cashier`, `barista`, `kitchen`) tanpa peran `manager` terpisah dan tanpa alur persetujuan (approval) berlapis yang menghambat antrean:
- **Owner (`owner`):** Akses penuh ke konfigurasi toko, katalog menu, manajemen harga/pajak/service charge, manajemen staf, inventaris, penarikan dana, laporan keuangan, dan seluruh aksi operasional.
- **Cashier (`cashier`):** Mengoperasikan POS Kasir (termasuk layar kedua pelanggan/Dual-Screen), menerima pembayaran tunai/EDC/QRIS statis/QRIS dinamis, membuka pesanan `open_tab`, mengelola split bill, membuka/menutup shift kasir, serta melakukan void/refund dengan pencatatan alasan wajib ke `audit_logs`.
- **Barista (`barista`):** Mengoperasikan KDS Barista untuk pesanan minuman, memperbarui status tahapan peracikan (`queued` -> `preparing` -> `ready` -> `served`), dan mengatur ketersediaan cepat item minuman (86-ing stok habis).
- **Kitchen (`kitchen`):** Mengoperasikan KDS Dapur untuk pesanan makanan dan pastry dengan kapabilitas yang setara dengan Barista pada stasiun dapur.

### 4.2 Fleksibilitas Mode Shift (`stores.shift_mode`)
Setiap toko dapat memilih satu dari dua mode shift sesuai skala operasionalnya:
1. **`single_auto` (Default untuk kafe 1 shift):** Sistem membuka dan menutup buku operasional harian secara otomatis mengikuti jam operasional toko tanpa mewajibkan kasir melakukan ritual buka/tutup laci kas berlapis.
2. **`multi_shift` (Untuk kafe 2-3 shift):** Mewajibkan pencatatan modal awal kas tunai (opening cash float), serah terima antar-kasir, dan rekonsiliasi uang fisik saat tutup shift (closing cash count).

### 4.3 Katalog Menu dan 1-Level Nested Sub-Addons
- Mendukung grup modifier (`modifier_groups`) dengan aturan pemilihan tunggal (`single`) maupun jamak (`multiple`), batas minimum/maksimum (`min_select`, `max_select`), dan override kewajiban per produk (`product_modifier_groups.override_required`). Pada grup bertipe `single`, kuantitas opsi (`quantity`) wajib bernilai `1`.
- Mendukung **1-Level Nested Sub-Addons** melalui tabel `option_modifier_groups`: pemilihan sebuah opsi induk (misalnya memilih *Oat Milk*) dapat memicu munculnya grup modifier anak (misalnya *Plant Milk Preparation: Standard Pour / Whipped Oat Cold Foam*). Jika opsi induk pada grup `multiple` dipilih dengan `quantity > 1`, maka harga dan pemotongan stok sub-addon dikalikan secara proporsional (`item.Quantity * parentMod.Quantity * subAddon.Quantity`).
- Seluruh nama produk, harga dasar, nama grup modifier, nama opsi, dan selisih harga (`price_delta`) di-snapshot secara permanen ke `order_items` dan `order_item_options` saat pesanan dibuat. Perubahan harga katalog di masa depan tidak pernah mengubah struk historis.

### 4.4 Mesin Harga Integer Rupiah (`int64`) & Basis Points (`BPS`)
- Seluruh nominal uang disimpan dan dihitung sebagai bilangan bulat Rupiah (`int64` di Go, `BIGINT` di PostgreSQL 18) tanpa tipe floating-point (`float64`).
- Tarif pajak (PB1), service charge, dan MDR QRIS disimpan dalam satuan **Basis Points (`1% = 100 BPS`)** per toko dengan pembulatan deterministik *Round Half Up*:
  $$\text{Fee} = \left\lfloor \frac{\text{Amount} \times \text{BPS} + 5000}{10000} \right\rfloor$$
- Urutan perhitungan resmi:
  1. `SubtotalAmount` = jumlah seluruh `order_items.subtotal_amount`.
  2. `NetSubtotal` = `SubtotalAmount - DiscountAmount`.
  3. `ServiceChargeAmount` = `CalculateRoundHalfUpBPS(NetSubtotal, store.ServiceBps)`.
  4. `TaxableBase` = `NetSubtotal + ServiceChargeAmount`.
  5. `TaxAmount` = `CalculateRoundHalfUpBPS(TaxableBase, store.TaxBps)`.
  6. `TotalAmount` = `TaxableBase + TaxAmount`.

### 4.5 Pemisahan Status Pesanan, Status Pembayaran, dan Siklus Hidup Reservasi Stok
MyCaffe memisahkan siklus hidup persiapan dapur (`orders.status`), siklus hidup finansial (`orders.payment_status`, `payment_splits.status`, dan `payments.status`), serta siklus hidup reservasi stok (`orders.stock_status`):
- **Order Status (`orders.status`):** `pending_payment`, `queued`, `preparing`, `ready`, `served`, `completed`, `cancelled`.
- **Order Payment Status (`orders.payment_status`):** `unpaid`, `pending`, `partially_paid`, `paid`, `failed`, `cancelled`, `expired`, `partially_refunded`, `refunded`, `chargeback`, `partial_chargeback`.
- **Order Stock Reservation Status (`orders.stock_status`):**
  - `not_tracked`: Pesanan tidak memuat produk atau modifier yang melacak stok (`track_stock = false`).
  - `reserved`: Stok produk/modifier telah dikurangi secara atomik saat pesanan `pending_payment` dibuat dengan batas waktu kedaluwarsa (`orders.expires_at = +15 menit`).
  - `committed`: Pembayaran telah lunas (`paid`) atau pesanan dibuat melalui jalur staf terverifikasi (`open_tab`), sehingga stok dikomit secara permanen.
  - `released`: Pesanan belum dibayar hingga kedaluwarsa (`expired`) atau dibatalkan (`cancelled`), dan stok yang sebelumnya `reserved` telah dikembalikan secara idempoten tepat satu kali ke katalog.
- **Payment Attempt Status (`payments.status`):** `pending`, `authorized`, `paid`, `failed`, `cancelled`, `expired`, `partially_refunded`, `refunded`, `chargeback`, `partial_chargeback`.
- **Split Bill Status (`payment_splits.status`):** `unpaid`, `pending`, `partially_paid`, `paid`, `cancelled`, `refunded`.

### 4.6 Pemisahan Inventaris Habis Pakai vs Aset Tidak Habis Pakai
- **Barang Habis Pakai (Consumables / Tracked Stock):** Biji kopi, susu, sirup, pastry, dan cup dikelola melalui reservasi stok transaksional (`track_stock = true`, `stock_quantity`) yang dikunci secara atomik (`SELECT ... FOR UPDATE`) dalam urutan UUID deterministik saat pesanan dibuat.
- **Aset Operasional Tidak Habis Pakai (Non-Consumable Assets):** Mesin espresso, grinder, tablet POS, printer thermal, dan perangkat jaringan dicatat sebagai inventaris aset toko untuk pemantauan kondisi/perawatan (maintenance log) tanpa tercampur dengan stok bahan baku penjualan.
