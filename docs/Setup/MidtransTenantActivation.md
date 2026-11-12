# Checklist aktivasi Midtrans untuk mitra gerai (PRD v3 N12, B6)

Dokumen ini dibagikan saat onboarding. Cassy memakai akun Midtrans milik gerai sendiri (BYOK): uang tamu masuk langsung ke rekening gerai, Cassy tidak memegang dana.

Persyaratan dan tampilan dashboard Midtrans bisa berubah. Bila ada beda dengan dokumen ini, ikuti dashboard dan tim Midtrans, lalu kabari tim AzureDevID agar dokumen ini diperbarui.

## 1. Siapkan sebelum mendaftar

- [ ] Data usaha: nama usaha, alamat gerai, nomor telepon, dan rekening bank atas nama usaha atau pemilik.
- [ ] Identitas pemilik: KTP, dan foto selfie memegang KTP bila diminta Midtrans.
- [ ] Legalitas sesuai bentuk usaha (misalnya NIB, dan NPWP bila ada).
- [ ] Alamat website yang menampilkan produk dan harga, kontak, serta kebijakan pengembalian dana. Bila gerai belum punya website, kabari tim AzureDevID sebelum mendaftar: kami tanyakan ke Midtrans apakah profil media sosial atau halaman lain diterima.
- [ ] Penjelasan alur bayar (payment flow): tamu memindai QR di meja, memilih menu, membayar dengan QRIS, GoPay, atau ShopeePay, lalu pesanan masuk ke dapur. Tim AzureDevID dapat menyiapkan tangkapan layar alur ini dari akun demo.

## 2. Daftar dan aktifkan metode di Midtrans

- [ ] Buat akun di dashboard Midtrans dan lengkapi verifikasi usaha.
- [ ] Ajukan aktivasi metode yang akan dipakai: **QRIS**, **GoPay**, **ShopeePay**.
- [ ] Catat status tiap metode di tabel bagian 5. Metode bisa disetujui pada waktu berbeda.

## 3. Hubungkan ke Cassy

- [ ] Di dashboard Midtrans, salin **Server Key** dan **Client Key** lingkungan yang dipakai (sandbox untuk uji coba, production untuk jualan sungguhan).
- [ ] Masukkan keduanya di pengaturan pembayaran Cassy oleh pemilik gerai. Jangan kirim kunci lewat chat atau email ke siapa pun, termasuk tim AzureDevID.
- [ ] Di dashboard Midtrans, isi **Payment Notification URL** dengan:
      `https://<domain Cassy>/api/v1/payments/midtrans/webhook/<slug gerai>`
      Tim AzureDevID memberikan domain dan slug gerai Anda saat onboarding.
- [ ] URL kembali setelah membayar di aplikasi GoPay atau ShopeePay tidak perlu diisi. Cassy mengirimnya sendiri di setiap transaksi.

## 4. Uji sebelum dibuka untuk tamu

- [ ] Uji satu transaksi di sandbox untuk tiap metode yang disetujui.
- [ ] Pastikan pesanan uji berubah menjadi "Pembayaran diterima" di halaman tamu dan muncul di tablet kasir.
- [ ] Setelah beralih ke production, uji satu transaksi kecil sungguhan per metode, lalu batalkan atau kembalikan dananya lewat kasir.

## 5. Status per metode

Metode baru tampil untuk tamu hanya setelah disetujui Midtrans **dan** diaktifkan di Cassy oleh tim AzureDevID.

| Metode | Status di Midtrans | Tanggal disetujui | Aktif di Cassy | Transaksi uji production |
|---|---|---|---|---|
| QRIS | | | | |
| GoPay | | | | |
| ShopeePay | | | | |

## Bila tertahan

- Ditolak karena website: lihat butir website di bagian 1, lalu hubungi tim AzureDevID.
- Notifikasi pembayaran tidak sampai (pesanan tetap "Menunggu pembayaran" padahal tamu sudah bayar): periksa Payment Notification URL di bagian 3. Cassy juga memeriksa status ke Midtrans setiap 30 detik, jadi pesanan biasanya tetap tercatat lunas tanpa notifikasi.
