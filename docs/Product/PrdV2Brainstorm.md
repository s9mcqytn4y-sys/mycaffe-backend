# PRD v2 Brainstorm (draf, belum disetujui)

Status: **draf untuk sesi wawancara owner.** Lampiran PRD v2 01–07 belum diterima, jadi isi dokumen ini
adalah hipotesis dari kode v0.6.0 dan [OwnerOperationalAssessment.md](OwnerOperationalAssessment.md).
Tidak ada implementasi sebelum PRD disetujui.

## 1. Pernyataan masalah

Satu kafe ingin menerima pesanan dari meja (QR), kasir, dan nanti kanal lain. Owner ingin uangnya cocok
setiap hari tanpa harus hadir di toko. Versi v0.6.0 sudah menjaga **kebenaran uang**. PRD v2 harus
menjaga **waktu owner dan staf**.

## 2. Tema dan epik kandidat

| Tema | Epik | Ukuran keberhasilan |
|------|------|---------------------|
| T1 Owner jarak jauh | Remote approval (P1-04), notifikasi owner (P1-03), ringkasan harian otomatis, audit log viewer | Owner tidak perlu datang ke kasir untuk approval; < 1 menit dari permintaan sampai persetujuan |
| T2 Kelancaran kasir | Daftar dan detail pesanan, daftar tab, kick laci kas, cetak ulang | Kasir menemukan pesanan apa pun dalam < 5 detik |
| T3 Pengalaman pelanggan | Layar antrean TV, tamu membayar tab dine-in dari HP, struk digital via WA/email | Pertanyaan "pesanan saya mana?" berkurang |
| T4 White-label | Branding di settings, footer struk, domain per klien | Klien kedua di-onboard dalam < 1 hari |
| T5 Ketahanan | ADR-005 offline tunai (beku), dead-letter outbox (P0-08) | Penjualan tunai tetap jalan saat internet mati |
| T6 Pertumbuhan | Inventaris C5, promo/bundle K7, loyalty K6, delivery (semua beku) | Setelah go-live dan data penjualan 1–3 bulan |

## 3. Roadmap usulan

| Fase | Isi | Gate |
|------|-----|------|
| **v0.7 Demo-ready** | G-1 daftar/detail pesanan, G-2 daftar tab, L-1..L-3 kebocoran demo, docs | Demo frontend end-to-end |
| **v0.8 Owner-remote** | Remote approval, notifikasi owner, audit log viewer, PATCH staf, kick laci, TV antrean | Owner menyetujui void dari HP |
| **v0.9 Go-live** | P0-07/08/09, RF1-a/b/d/f setelah sandbox, Named Tunnel, Grafana, backup drill di VPS | Checklist go-live hijau; owner menandatangani |
| **v1.0** | Satu kafe live ±2 minggu tanpa insiden P1 | Stabil |
| **v1.x** | Cairkan satu fitur beku per rilis sesuai data (kandidat pertama: offline tunai atau inventaris) | Persetujuan owner per fitur |

## 4. Pertanyaan terbuka (dibahas di sesi wawancara)

1. Siapa target demo pertama: investor, calon klien kafe, atau staf internal?
2. Apakah demo memakai perangkat nyata (printer, laci, tablet), atau cukup simulasi?
3. Remote approval: PIN di HP owner, biometrik, atau cukup tombol setuju dari sesi owner yang sudah login?
4. Ambang void/refund yang realistis per kafe?
5. Aplikasi frontend pertama: POS kasir, PWA tamu, atau backoffice?
6. Native Android/iOS sekarang, atau PWA dulu?
7. Klien kedua: kapan, dan apakah perlu branding sendiri?
8. Anggaran bulanan infrastruktur maksimum?
