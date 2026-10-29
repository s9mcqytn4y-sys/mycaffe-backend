# MyCaffe: Brand Guidelines, Merk, dan Product Knowledge

Dokumen ini mematenkan identitas merek, token desain visual default, konvensi penomoran entitas, serta pengetahuan produk inti (product knowledge) untuk platform **MyCaffe**. Walaupun setiap tenant/toko dapat melakukan kustomisasi white-label (logo, warna aksen, nama toko, dan pengaturan struk), platform inti wajib memiliki identitas standar yang jelas, teruji, dan siap pakai sejak instalasi pertama.

---

## 1. Identitas Merek dan Positioning Platform

| Atribut | Ketentuan Paten Platform |
| :--- | :--- |
| **Nama Merek Resmi** | **MyCaffe** (Sub-sistem backend: **MyCaffe Core POS**) |
| **Tagline Resmi** | *Operasional kafe yang rapi, dari meja sampai laporan.* |
| **Positioning Produk** | Platform POS, QR Table Ordering, Kitchen Display System (KDS), TV Queue Display, dan Owner Backoffice multi-tenant yang berjalan mandiri di atas satu VPS milik merchant tanpa biaya langganan database PaaS bulanan yang membengkak. |
| **Target Pengguna Utama** | Specialty coffee shop, kafe independen, jaringan gerai kopi multi-cabang, artisan bakery, dan restoran kasual di Indonesia. |
| **Kepribadian Merek (Brand Voice)** | Sigap, transparan, akurat secara finansial, ramah bagi kru operasional (barista/kasir), dan bebas jargon teknis yang membingungkan pelanggan. |
| **Bahasa Antarmuka** | Bahasa Indonesia alami untuk seluruh antarmuka pelanggan, kasir, barista, dapur, dan pemilik usaha. Identifier kode dan skema database menggunakan Bahasa Inggris. |

---

## 2. Sistem Desain dan Token Visual Default (Espresso & Crema)

Ketika tenant baru dibuat dan belum mengunggah kustomisasi warna sendiri, aplikasi menerapkan palet **Espresso & Crema** yang dirancang khusus untuk keterbacaan kontras tinggi di area bar maupun di bawah sinar matahari luar ruangan (outdoor seating).

### 2.1 Palet Warna Inti

| Peran Token | Nama Warna | Hex Code | Penggunaan Utama |
| :--- | :--- | :--- | :--- |
| **Primary Brand** | Deep Espresso | `#2C1810` | Header navigasi, tombol aksi primer pada tema terang, teks utama bernuansa hangat. |
| **Brand Accent** | Warm Crema | `#C87D55` | Highlight kategori aktif, badge populer, aksen fokus input, dan indikator interaktif. |
| **Surface Light** | Warm Milk / Alabaster | `#FAF7F2` | Latar belakang utama aplikasi QR Table Ordering dan Owner Backoffice agar mata tidak cepat lelah. |
| **Surface Card** | Pure Porcelain | `#FFFFFF` | Kartu produk, panel ringkasan keranjang, dan modal struk digital. |
| **Surface Dark (KDS & TV)** | Roasted Charcoal | `#12100E` | Latar belakang mode gelap untuk layar KDS Barista/Dapur dan TV Queue Display (mencegah burn-in dan silau). |
| **Status: Success / Ready** | Botanical Mint | `#16A34A` | Status pembayaran lunas (`paid`), pesanan siap diambil (`ready`), dan stok tersedia. |
| **Status: Warning / Preparing** | Amber Roast | `#D97706` | Pesanan sedang diracik (`preparing`), peringatan stok menipis, atau menunggu pembayaran (`pending`). |
| **Status: Danger / SLA Breach** | Crimson Berry | `#DC2626` | Pembayaran gagal/kedaluwarsa, pembatalan pesanan (`cancelled`), stok habis, dan tiket KDS melewati batas SLA. |

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
| **Payment Provider ID** | `UUID v4` (`payments.id`) | `{ORDER_NUMBER}-S{SPLIT}-A{ATTEMPT}` | Menjamin keunikan `provider_order_id` saat dikirim ke Midtrans Core API untuk skenario split bill maupun percobaan ulang (retry). |
| **Staff ID Card** | `UUID v4` (`users.id`) | `STF-{STORE_SLUG_UPPER}-{XXXX}` | Digunakan untuk pencetakan kartu identitas staf (ID Card) beserta PIN operasional kasir. |

---

## 4. Product Knowledge & Kapabilitas Operasional

### 4.1 Empat Peran Staf Operasional (Tanpa Birokrasi Approval Bertingkat)
Operasional kafe membutuhkan kecepatan tinggi di jam sibuk. MyCaffe sengaja membatasi peran pengguna menjadi **4 peran inti** (`owner`, `cashier`, `barista`, `kitchen`) tanpa peran `manager` terpisah dan tanpa alur persetujuan (approval) berlapis yang menghambat antrean:
- **Owner (`owner`):** Akses penuh ke konfigurasi toko, katalog menu, manajemen harga/pajak/service charge, manajemen staf, inventaris, penarikan dana, laporan keuangan, dan seluruh aksi operasional.
- **Cashier (`cashier`):** Mengoperasikan POS Kasir (termasuk layar kedua pelanggan/Dual-Screen), menerima pembayaran tunai/EDC/QRIS statis/QRIS dinamis, mengelola split bill, membuka/menutup shift kasir, serta melakukan void/refund dengan pencatatan alasan wajib ke `audit_logs`.
- **Barista (`barista`):** Mengoperasikan KDS Barista untuk pesanan minuman, memperbarui status tahapan peracikan (`queued` -> `preparing` -> `ready` -> `served`), dan mengatur ketersediaan cepat item minuman (86-ing stok habis).
- **Kitchen (`kitchen`):** Mengoperasikan KDS Dapur untuk pesanan makanan dan pastry dengan kapabilitas yang setara dengan Barista pada stasiun dapur.

### 4.2 Fleksibilitas Mode Shift (`stores.shift_mode`)
Setiap toko dapat memilih satu dari dua mode shift sesuai skala operasionalnya:
1. **`single_auto` (Default untuk kafe 1 shift):** Sistem membuka dan menutup buku operasional harian secara otomatis mengikuti jam operasional toko tanpa mewajibkan kasir melakukan ritual buka/tutup laci kas berlapis.
2. **`multi_shift` (Untuk kafe 2-3 shift):** Mewajibkan pencatatan modal awal kas tunai (opening cash float), serah terima antar-kasir, dan rekonsiliasi uang fisik saat tutup shift (closing cash count).

### 4.3 Katalog Menu dan 1-Level Nested Sub-Addons
- Mendukung grup modifier (`modifier_groups`) dengan aturan pemilihan tunggal (`single`) maupun jamak (`multiple`), batas minimum/maksimum (`min_select`, `max_select`), dan override kewajiban per produk (`product_modifier_groups.override_required`).
- Mendukung **1-Level Nested Sub-Addons** melalui tabel `option_modifier_groups`: pemilihan sebuah opsi induk (misalnya memilih *Oat Milk*) dapat memicu munculnya grup modifier anak (misalnya *Plant Milk Preparation: Standard Pour / Whipped Oat Cold Foam*).
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

### 4.5 Pemisahan Status Pesanan dan Status Pembayaran
MyCaffe memisahkan siklus hidup persiapan dapur (`orders.status`) dari siklus hidup finansial (`orders.payment_status`, `payment_splits.status`, dan `payments.status`):
- **Order Status (`orders.status`):** `pending_payment`, `queued`, `preparing`, `ready`, `served`, `completed`, `cancelled`.
- **Order Payment Status (`orders.payment_status`):** `unpaid`, `pending`, `partially_paid`, `paid`, `failed`, `cancelled`, `expired`, `partially_refunded`, `refunded`, `chargeback`, `partial_chargeback`.
- **Payment Attempt Status (`payments.status`):** `pending`, `authorized`, `paid`, `failed`, `cancelled`, `expired`, `partially_refunded`, `refunded`, `chargeback`, `partial_chargeback`.
- **Split Bill Status (`payment_splits.status`):** `unpaid`, `pending`, `partially_paid`, `paid`, `cancelled`, `refunded`.

### 4.6 Pemisahan Inventaris Habis Pakai vs Aset Tidak Habis Pakai
- **Barang Habis Pakai (Consumables / Tracked Stock):** Biji kopi, susu, sirup, pastry, dan cup dikelola melalui pengurangan stok transaksional (`track_stock = true`, `stock_quantity`) yang dikunci secara atomik (`SELECT ... FOR UPDATE`) saat pesanan dibuat.
- **Aset Operasional Tidak Habis Pakai (Non-Consumable Assets):** Mesin espresso, grinder, tablet POS, printer thermal, dan perangkat jaringan dicatat sebagai inventaris aset toko untuk pemantauan kondisi/perawatan (maintenance log) tanpa tercampur dengan stok bahan baku penjualan.
