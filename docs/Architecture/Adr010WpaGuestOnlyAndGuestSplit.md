# ADR-010: WPA khusus QR order, operasional di Android, split bill lewat tautan

- **Status**: Accepted
- **Tanggal**: 2026-11-09
- **Pemutus**: Abdul Aziz (Director)
- **Mengubah**: ADR-009 bagian 2.4 dan 2.5 (layar operasional web)

## Konteks

Web client berisi beranda pemasaran, onboarding, konsol platform, konsol gerai, KDS, antrean TV, dan simulator jaringan. Audit demo menemukan sebagian besar layar itu tidak cocok dengan kontrak API, dan banyak klaim yang tidak punya dasar. Tamu yang memindai QR di meja adalah satu-satunya pengguna web yang tidak bisa dilayani aplikasi terpasang.

## Keputusan

1. **WPA hanya melayani tamu.** Rute yang tersisa: `/`, `/order/[token]`, tautan bagian patungan, dan `/legal`.
2. **Semua operasional tenant ada di Android**, termasuk menu, meja, QR, KDS, setup layar, kredensial, dan laporan. Tidak ada layar pengganti di web.
3. **Tampilan tamu mengikuti brand tenant** dari `/public/stores/:slug/cms`, dengan Espresso & Crema sebagai cadangan dan validasi kontras.
4. **Split bill tamu lewat tautan per orang.** Pemesan membagi tagihan rata atau per item, dan tiap bagian punya tautan sendiri.
5. **Pesanan masuk dapur setelah seluruh bagian lunas**, sesuai pay-first yang berlaku.
6. **Key sandbox Midtrans demo dimasukkan lewat API BYOK**, tidak ditulis di SQL seed.

## Kontrak split yang direncanakan

| Rute | Akses | Fungsi |
|---|---|---|
| `POST /api/v1/public/guest/orders/:id/splits` | Sesi pemesan, CSRF | Membuat bagian dan tautannya |
| `GET /api/v1/public/split-shares/:token` | Token | Nama gerai, label bagian, nominal, status |
| `POST /api/v1/public/split-shares/:token/payments` | Token, CSRF | Membuat QRIS untuk bagian itu |

Token acak 32 byte, hanya hash yang disimpan, berakhir bersama sesi meja. Halaman tautan tidak menampilkan isi pesanan orang lain. Migrasi bersifat aditif, dengan RLS dan `SECURITY DEFINER` baru yang hanya mengembalikan `(store_id, split_id)`. Tambahan fungsi itu perlu masuk daftar putih `AGENTS.md`.

## Alternatif yang ditolak

| Opsi | Alasan ditolak |
|---|---|
| Layar operasional tetap di web sampai Android siap | Director memilih memutus sekarang, belum ada tenant nyata |
| Split per HP, tiap orang pesan sendiri | Tidak melayani satu orang memesan lalu patungan |
| QRIS bergantian di satu HP | Lebih aman, tetapi Director memilih tautan agar bisa bayar dari HP sendiri |

## Konsekuensi

- Sampai layar Android dibangun, tenant demo hanya bisa disiapkan lewat seed dan API, dan tiket KDS tidak punya layar.
- Tautan publik menambah permukaan serangan, jadi butuh rate limit sendiri dan pengujian IDOR.
- Bila ada bagian yang tidak dibayar sampai kedaluwarsa, pesanan tidak masuk dapur. Pemesan harus bisa melunasi sisanya dan dana yang sudah masuk perlu jalur refund.
