# Owner Operational Assessment (v0.6.0)

Disusun 2026-11-04 terhadap `staging` setelah rilis v0.6.0. Sudut pandang: pemilik satu kafe yang
kritis terhadap biaya, waktu, dan risiko uang. Status memakai empat kategori `AGENTS.md`.
Bukti kode: `cmd/api/main.go` (rute), `internal/pkg/rbac` (izin), `internal/domain` (status).

## 1. Ringkasan untuk owner

Backend sudah **kuat di inti uang**. Harga dihitung ulang di server. Webhook Midtrans diverifikasi dan
idempoten. Shift dan laci kas punya X/Z report. Void dan refund diaudit. RLS memisahkan data per toko.

Yang **belum** siap adalah **pengalaman sehari-hari owner dan staf**:

- persetujuan harus dilakukan di tablet kasir;
- kasir belum bisa melihat daftar pesanan;
- belum ada layar antrean (TV);
- belum ada notifikasi ke owner;
- perangkat keras belum terhubung langsung ke backend.

Semua celah ini bisa ditutup dengan perubahan aditif. Tidak ada yang memerlukan desain ulang.

## 2. Pain point per peran

### Owner (Anda)

| # | Pain point | Kondisi backend sekarang | Status |
|---|------------|--------------------------|--------|
| O-1 | **Harus datang ke tablet kasir untuk mengetik PIN owner.** Ini terjadi saat void/refund di atas ambang (default Rp100.000), diskon melebihi batas kasir, refund transfer bank, dan selisih kas saat tutup shift. | Field `owner_pin` di body request kasir (`operational_service.go`, `shift_service.go`). Tidak ada jalur persetujuan jarak jauh. | Planned (P1-04) |
| O-2 | Tidak tahu kejadian penting secara real time: void besar, refund, selisih kas, percobaan PIN gagal. | Hanya email untuk percobaan PIN owner gagal (PR A). Selain itu tidak ada notifikasi. | Planned (P1-03) |
| O-3 | Melihat omzet harus membuka backoffice. | `GET /reports/sales` dan `/reports/summary` (JSON/CSV) sudah ada. Belum ada ringkasan harian yang dikirim otomatis. | Implemented & Tested (laporan); Planned (kirim otomatis) |
| O-4 | Tidak bisa menelusuri siapa melakukan apa. | `audit_logs` lengkap, tetapi **tidak ada endpoint baca**. | Planned (gap) |
| O-5 | Menonaktifkan staf yang keluar atau mengganti perannya. | Ada invite, reset PIN, dan PIN sementara. **Tidak ada endpoint PATCH staf** untuk menonaktifkan atau mengganti peran lewat API. | Planned (gap, P1-05) |
| O-6 | Biaya bulanan. | Satu VPS KVM 2, R2, dan Grafana Cloud free tier (ADR-002). Biaya tetap rendah. | Implemented |

### Kasir

| # | Pain point | Kondisi backend | Status |
|---|------------|-----------------|--------|
| K-1 | **Tidak bisa melihat daftar pesanan hari ini atau pesanan terbuka** untuk mencari ulang, mencetak ulang, void, atau refund. | Tidak ada `GET /pos/orders` dan tidak ada `GET /pos/orders/:id`. Yang ada hanya struk per id, tab per id, dan tiket KDS. | **Gap kritis frontend** |
| K-2 | Daftar tab terbuka. | Ada `GET /pos/tabs/:id` dan `GET /pos/tables` (papan meja). Tidak ada daftar tab. | Gap |
| K-3 | EDC: harus mengetik nominal dua kali (di EDC dan di POS). | Pembayaran EDC dicatat manual beserta referensi. | Implemented (manual); integrasi EDC tidak direncanakan |
| K-4 | Laci kas harus dibuka dengan tangan. | ESC/POS tidak berisi perintah buka laci (`ESC p`). | Gap kecil |
| K-5 | Internet mati, tetapi transaksi tunai tetap harus jalan. | ADR-005 offline dibekukan sampai go-live. | Frozen |

### Barista / dapur

| # | Pain point | Kondisi backend | Status |
|---|------------|-----------------|--------|
| B-1 | Tiket masuk tanpa suara atau cetak dapur. | KDS realtime lewat WebSocket. Belum ada cetak tiket dapur (kitchen printer). | Implemented (layar); Planned (printer dapur) |
| B-2 | Menandai menu habis. | `PATCH /catalog/products/:id/availability` per stasiun. | Implemented & Tested |

### Pelanggan

| # | Pain point | Kondisi backend | Status |
|---|------------|-----------------|--------|
| C-1 | Tidak tahu kapan pesanan siap. | Tracker per pesanan (`/public/orders/track/:token`) dan WebSocket tamu sudah ada. **Tidak ada endpoint layar antrean TV** (daftar nomor antrean "sedang dibuat" dan "siap"). | Gap |
| C-2 | Struk digital. | Link struk 90 hari (B1). | Implemented & Tested |
| C-3 | Bayar di meja. | QRIS dinamis Midtrans dan pay-first QR sudah ada. Tamu tidak bisa membayar tab dine-in yang dibuka kasir. | Implemented (pay-first) |

## 3. Persetujuan owner tanpa ke tablet kasir: opsi

| Opsi | Cara kerja | Biaya | Risiko | Rekomendasi |
|------|-----------|-------|--------|-------------|
| A. Tetap PIN di tablet | Seperti sekarang | Nol | Owner harus hadir; PIN bisa diintip | Hanya untuk fallback |
| **B. Remote approval lewat aplikasi owner (PWA)** | Kasir membuat `approval_request` (aksi, nominal, alasan, kedaluwarsa 2 menit). Owner menerima push atau WebSocket di HP, lalu menyetujui dengan PIN atau biometrik perangkat. Kasir menerima token sekali pakai yang diikat ke aksi dan nominal. | Satu tabel, 3 endpoint, satu kanal WS | Butuh internet di HP owner; harus single-use dan terikat nominal | **Ya: P1-04, sebelum go-live** |
| C. Manajer shift (peran baru) | Peran `manager` dengan ambang sendiri | Perubahan RBAC | Menambah peran (gate persetujuan) | Setelah B, bila ada lebih dari 1 cabang |
| D. Naikkan ambang + laporan harian | Lebih sedikit persetujuan; owner meninjau laporan void/refund harian | Hampir nol | Kebocoran kecil tidak tercegah, hanya terdeteksi | Pelengkap B |

Kombinasi paling efisien adalah **B + D**:

- void atau refund di atas ambang disetujui dari HP owner;
- nominal di bawah ambang cukup tercatat dan muncul di ringkasan harian;
- PIN di tablet tetap ada sebagai cadangan saat HP owner offline.

## 4. Perangkat keras (wiring)

| Perangkat | Kondisi backend | Pain point | Solusi paling efisien |
|-----------|-----------------|-----------|-----------------------|
| **Printer thermal 58/80 mm** | Backend menghasilkan byte ESC/POS (`GET /pos/orders/:id/receipt?format=escpos`). | Browser PWA tidak bisa langsung mengirim ke printer USB atau LAN. | Android: aplikasi pembungkus (Capacitor) dengan plugin Bluetooth/USB, atau aplikasi bridge seperti RawBT. Printer LAN: kirim ke port 9100 lewat bridge lokal. Backend tidak perlu berubah. |
| **Cash drawer** | Tidak ada perintah kick. | Laci harus dibuka manual. | Tambahkan `ESC p 0 25 250` di awal struk untuk pembayaran tunai dan `no_sale` (perubahan kecil di `receipt_escpos.go`). Laci terhubung ke port RJ11 printer. |
| **EDC** | Pencatatan manual dan referensi. | Nominal diketik dua kali. | Tetap manual untuk MVP. Integrasi ECR bank berbeda per bank dan butuh perjanjian. Tidak sepadan untuk satu kafe. |
| **Layar KDS** | WebSocket dan `GET /kds/tickets`. | — | Tablet murah dengan PWA mode kiosk. |
| **TV antrean** | **Belum ada endpoint.** | — | Endpoint publik read-only per toko dengan token layar, ditambah kanal WS `queue`. Isinya hanya nomor antrean, tanpa nama pelanggan. |
| **Customer-facing display** | Belum ada. | — | Bisa memakai kanal WS POS yang sama. Prioritas rendah. |
| **Email** | Outbox, SMTP Gmail (demo) / Hostinger (produksi). | Domain belum ada; SPF/DKIM/DMARC belum disetel. | Tetap. Domain adalah blocker owner. |
| **Kustomisasi / white-label** | `store_settings`: pajak, service, metode bayar, prefix pesanan dan antrean, mode billing, shift. | Belum ada logo atau warna toko di settings; footer struk tetap. | Tambahkan `branding` (logo URL dari media B3, warna, footer struk). Perubahan aditif. |

## 5. Kesimpulan bisnis

1. **Untuk demo:** cukup. Celah K-1 dan C-1 akan terlihat jelas di layar, jadi tutup sebelum demo
   frontend (lihat [DemoReadinessChecklist.md](../Setup/DemoReadinessChecklist.md)).
2. **Untuk go-live satu kafe:** tutup K-1, K-2, O-1 (remote approval), O-4, O-5, C-1, K-4, ditambah
   P0-07/08/09.
3. **Jangan dibangun sekarang:** integrasi ECR EDC, multi-cabang, loyalty, delivery (sudah dibekukan).
