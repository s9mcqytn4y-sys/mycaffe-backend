# Kebutuhan Owner, Staf, dan Pelanggan

Disusun dari wawancara owner 2026-10-31 (keputusan O1–O4, I1–I7, E1–E5, T1–T2, C1–C3, H1, R1, N1, K1–K9;
lihat [DecisionRegister.md](../Architecture/DecisionRegister.md)). Kolom **Status**: ✅ Implemented & Tested,
🟡 sebagian, ⏳ Planned, ⛔ Blocked by Dependency.

## 1. Owner / pengusaha

### 1.1 Kontrol & keamanan

| Kebutuhan | Status |
| ----------- | -------- |
| Mengundang staf, reset PIN, PIN sementara, melihat siapa yang terkunci | ✅ |
| Peran tetap (owner, kasir, barista, dapur) dengan izin yang jelas | ✅ (matriks RBAC) |
| Kasir boleh merangkap barista (toggle) | ✅ setting; ⏳ layar KDS |
| Ambang void/refund (Rp100.000) dan diskon kasir (10%) yang butuh PIN owner | ✅ setting; ⏳ alur void/refund/diskon |
| Jejak audit setiap aksi sensitif (siapa, kapan, alasan) | ✅ auth, pembayaran, pengaturan; ⏳ void/refund/stok |
| Notifikasi email saat akun staf terkunci | ✅ |
| Notifikasi pembayaran ganda / refund diperlukan | ⏳ |

### 1.2 Konfigurasi mandiri tanpa developer (K3)

| Bisa diubah owner di aplikasi | Status | Tetap di server (.env), hanya statusnya terlihat |
| ------------------------------- | -------- | -------------------------------------------------- |
| Ambang void/diskon/selisih kas, toggle kasir-barista, tutup meja otomatis, jeda QR, mode QRIS statis | ✅ (`/api/v1/store/settings`) | Kunci Midtrans |
| Identitas toko, logo, warna, jam buka, pajak/service/takeaway, metode bayar | ⏳ | Password SMTP/mailbox |
| Menu, varian, harga, foto, ketersediaan, promo, bundling | ⏳ | Password database, JWT secret |
| Template struk (header/footer, NPWP, Wi-Fi) | ⏳ | Domain/DNS, sertifikat TLS (status otomatis) |
| Staf & PIN | ✅ | Token backup R2 |
| Tanggal kedaluwarsa domain/VPS/email + pengingat 30/7/1 hari (K4) | ⏳ | — |

### 1.3 Data real-time yang dibutuhkan

Antrean aktif per stasiun + umur tiket (SLA), meja terisi/kosong, penjualan hari ini (bruto, metode bayar),
shift aktif dan kas di laci, menu habis/stok bahan menipis, pembayaran pending/gagal/ganda, email gagal.
Semua dikirim lewat outbox relay → WebSocket (⏳ Phase 03); ringkasan penting lewat email (N1).

### 1.4 Laporan & keuangan (FAT)

| Laporan | Butuh | Status |
| --------- | ------- | -------- |
| Penjualan per hari/jam/produk/varian/kategori/metode bayar | order snapshot | ⏳ (data ✅) |
| Laporan shift X/Z, selisih kas, paid-out | tabel shift (C1) | ⏳ |
| Perkiraan setoran Midtrans (bruto, MDR, neto) + impor CSV settlement + selisih (K5) | `payments`, `qris_mdr_bps` | ⏳ |
| HPP & gross profit per menu | resep + biaya bahan (K2) | ⏳ |
| Net profit | gross profit − pengeluaran (`expenses`) | ⏳ |
| Pajak (PB1) & service charge terkumpul | order snapshot | ⏳ |
| Perputaran meja | `table_sessions` (T1) | ⏳ |
| Invoice/struk pajak per transaksi | struk digital (R1) | ⏳ |

Catatan: gross profit hanya akurat untuk menu yang punya resep; net profit hanya akurat bila semua
pengeluaran dicatat.

### 1.5 Otomatisasi & digitalisasi

| Otomatis | Status |
| ---------- | -------- |
| Kedaluwarsa order belum dibayar + pengembalian stok | ✅ |
| Rekonsiliasi status Midtrans | ✅ |
| Email aktivasi/reset/notifikasi dengan retry | ✅ |
| Potong stok bahan dari resep saat lunas, alert stok minimum | ⏳ |
| Promo happy hour otomatis, bundling | ⏳ |
| Stempel loyalty otomatis per order lunas (K6) | ⏳ |
| Tutup meja otomatis (T1) | ⏳ |
| Fallback QRIS statis saat Midtrans gagal (K8) | 🟡 setting ada; ⏳ alur checkout |
| Pengingat langganan & backup gagal (K4) | ⏳ |
| Backup database terenkripsi ke R2 | ✅ skrip; ⛔ belum ada VPS |

### 1.6 Inventaris & SDM

- Inventaris: barang siap saji per unit, bahan baku dengan satuan g/ml/pcs, resep per varian, stok minimum,
  pembelian (harga rata-rata), opname, riwayat pergerakan, waste. Aset non-habis-pakai (mesin, tablet,
  printer) dicatat terpisah dengan log perawatan — ⏳.
- SDM: daftar staf & peran ✅, shift kerja kasir ⏳, catatan kinerja per staf (jumlah transaksi, void,
  selisih kas) dari audit ⏳. Penggajian/absensi di luar lingkup tahap ini.

### 1.7 Langganan & pembayaran infrastruktur (K4, K5)

- Domain, VPS KVM 2, dan email dibeli owner di Hostinger; perpanjangan memakai **auto-renew Hostinger**.
  Aplikasi tidak menagih kartu Anda; aplikasi mengingatkan.
- Midtrans tidak punya tombol "withdraw": dana disetor otomatis ke rekening terdaftar sesuai jadwal kontrak
  setelah dipotong MDR. Aplikasi menampilkan perkiraan setoran dan mencocokkannya dengan laporan settlement.

### 1.8 Promo, loyalty, dan fallback

- Promo (K7): persen/nominal per produk/kategori, happy hour, periode, kuota, bundling harga tetap, diskon
  kasir dalam batas O4. Satu promo terbaik per item, tidak bertumpuk. Voucher menyusul.
- Loyalty (K6): stempel "beli N gratis 1" berdasarkan email terverifikasi; 1 stempel per order; hadiah =
  diskon 100% satu item, tercatat di audit.
- QRIS statis (K8): otomatis saat Midtrans gagal 2×, atau dipaksa owner; kasir konfirmasi dengan nomor
  referensi; kembali ke dinamis setelah 10 menit.

## 2. Staf

| Peran | Yang dibutuhkan di aplikasi | Status |
| ------- | ----------------------------- | -------- |
| Kasir | Login cepat PIN, buat order walk-in/dine-in/takeaway, terima tunai/QRIS/EDC dengan kembalian otomatis, lihat pesanan QR, buka/tutup shift, void/refund dengan alasan, cetak struk, antrean bar bila merangkap | ✅ login, order POS, bayar; ⏳ sisanya |
| Barista | Tiket bar real-time + bunyi, modifier menonjol, tandai diproses/siap, tandai minuman habis | ⏳ (izin ✅) |
| Dapur | Sama seperti barista untuk stasiun dapur | ⏳ (izin ✅) |
| Semua | Ganti PIN sendiri, lupa PIN lewat email, sesi tidak putus di tengah shift | ✅ |

## 3. Pelanggan reguler (awam)

Alur ideal: scan QR di meja → menu langsung terbuka (tanpa install, tanpa daftar) → foto, harga, varian
jelas, item habis ditandai → pilih ukuran & tambahan → keranjang → bayar QRIS/e-wallet di HP → layar
"pesanan diterima" dengan nomor antrean & status real-time (diproses/siap) → struk digital (link/email
bila diisi) → stempel loyalty otomatis bila memakai email → bisa pesan lagi dari HP yang sama tanpa
mengulang. Bila pembayaran online gangguan: QRIS statis toko + konfirmasi kasir.

| Kebutuhan | Status |
| ----------- | -------- |
| Menu digital dengan foto & varian | ⏳ (catalog API C1–C4) |
| Pesan dari meja tanpa login | ⏳ (02.01-D/E) |
| Bayar QRIS dinamis/e-wallet | ✅ backend; ⏳ rute tamu |
| Status pesanan real-time | ⏳ (Phase 03) |
| Struk digital + email opsional + opt-in promo terpisah | ⏳ (R1, E4) |
| Kartu stempel digital | ⏳ (K6) |
| Privasi: data tidak terlihat tamu lain di meja yang sama | ✅ desain (Q6); ⏳ implementasi |
