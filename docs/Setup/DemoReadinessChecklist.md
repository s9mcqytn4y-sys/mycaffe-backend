# Demo Readiness Checklist (v0.6.0)

Disusun 2026-11-04. Tujuan: demo publik backend + frontend tanpa kebocoran data atau kredensial,
lalu memakai jalur yang sama untuk produksi. Runbook stack demo:
[DemoStagingEnvironment.md](DemoStagingEnvironment.md).

## 1. Evaluasi kebocoran lingkungan demo

| # | Temuan | Risiko | Tindakan sebelum demo publik |
|---|--------|--------|------------------------------|
| L-1 | Demo menjalankan seed development: semua akun memakai **PIN `482913`**, termasuk owner. Email dan slug seed tercatat di repo. URL tunnel bersifat publik. | Siapa pun yang tahu URL dan email bisa masuk sebagai owner. | **Ditutup (PR #54):** `migrate -action=demo-pins` mengganti PIN seed dengan `DEMO_OWNER_PIN` / `DEMO_STAFF_PIN` dari `.env.demo`. |
| L-2 | `JWT_SECRET_KEY` di compose demo punya nilai bawaan publik. Pemeriksaan placeholder hanya berlaku di `production`. | Token bisa dipalsukan bila validasi sesi lolos. Token staf terikat ke `auth_sessions`, jadi risikonya lebih kecil, tetapi tetap tidak boleh. | **Ditutup (PR #54):** `staging` menolak secret lemah, dan compose demo gagal start tanpa `JWT_SECRET_KEY`. |
| L-3 | Seed berisi alamat Gmail pribadi. | Data pribadi muncul di demo dan email undangan. Repo `PRIVATE`. | **Ditutup (PR #54):** `owner.central@mycaffe.local`. |
| L-4 | Quick Tunnel: URL berganti setiap restart, dan tidak ada akses terbatas. | Link demo lama mati; bot memindai URL. | Named Tunnel + Cloudflare Access (email OTP) untuk backoffice dan POS. Rute tamu (`/api/v1/public/*`) tetap publik. |
| L-5 | Midtrans sandbox key ada di `.env` lokal. | Aman selama `.env` tidak di-commit (`.gitignore` sudah mengecualikan). | Pakai key sandbox saja. Jangan pernah key produksi di mesin demo. |
| L-6 | Metrik (`METRICS_ADDR`) hanya internal, dan DB tidak dipublikasikan. | — | OK, sudah ditutup. |
| L-7 | Log JSON tidak memuat PIN, token, atau header Authorization. | — | OK. Setelah sesi demo, cari kata `pin` di `docker compose logs api` sekali lagi. |

## 2. Yang masih terbuka minimal untuk demo

Keputusan demo DM1–DM8 ada di [DecisionRegister.md](../Architecture/DecisionRegister.md).

| Item | Pemilik | Gate |
|------|---------|------|
| G-1 `GET /pos/orders` + `GET /pos/orders/:id` | Backend | Kontrak API disetujui owner |
| G-2 daftar tab terbuka (DM5) | Backend | Kontrak API disetujui owner |
| G-5 persetujuan owner dari HP dengan PIN (DM7, DM8) | Backend + frontend | Desain auth disetujui owner |
| L-1, L-2, L-3 | Backend | Sebelum URL dibagikan |
| Printer Bluetooth 58 mm + RawBT diuji dengan struk asli (DM3, DM6) | Owner + frontend | H-7 |
| Video cadangan alur lengkap (DM2) | Owner | H-2 |
| G-3 TV antrean, G-6 kick laci | — | Tidak ikut demo (DM3) |

## 3. Strategi Git, versioning, dan penamaan

- **Branch:** `feature/*`, `fix/*`, `docs/*`, `chore/*` → PR ke `staging` (squash) → PR `staging` → `main`
  (merge commit) → tag annotated `vX.Y.Z` (Release GHCR + SBOM + provenance). Detail:
  [GitWorkflowAndReleases.md](../Engineering/GitWorkflowAndReleases.md).
- **Persetujuan:** setiap merge menunggu "Setuju, merge" dari owner. Tidak ada push langsung ke `staging`
  atau `main`.
- **Versi:**
  - `MINOR` untuk fitur atau migrasi aditif;
  - `PATCH` untuk perbaikan;
  - `MAJOR` bila kontrak API rusak (belum pernah).
  - Demo memakai image yang sama dengan produksi, dipilih berdasarkan **digest**.
  - Frontend mengunci versi API lewat `info.version` di OpenAPI.
- **Penamaan:**
  - Go dan DB: [GoStyleAndNaming.md](../Engineering/GoStyleAndNaming.md) (snake_case untuk tabel, kolom, dan JSON);
  - docs: folder dan file PascalCase (`docs/Architecture/Roadmap.md`), kecuali `README.md`;
  - migrasi: `YYYYMMDDNNNNNN_snake_case.{up,down}.sql`;
  - commit: Conventional Commits (`feat(scope):`, `fix(scope):`, `docs:`).

## 4. Smoke test dengan curl

```bash
BASE=https://<demo-host>/api/v1
curl -s $BASE/health
# Login (cookie refresh disimpan ke jar)
curl -s -c jar -H 'Content-Type: application/json' \
  -d '{"store_slug":"central-cafe","email":"<email>","pin":"<pin>"}' $BASE/auth/staff/login
TOKEN=<access_token dari respons>
curl -s -H "Authorization: Bearer $TOKEN" $BASE/auth/staff/me
curl -s -H "Authorization: Bearer $TOKEN" $BASE/pos/catalog
curl -s -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"order_type":"takeaway","items":[{"product_id":"<id>","variant_id":"<id>","quantity":1}]}' $BASE/pos/orders/quote
curl -s -H "Authorization: Bearer $TOKEN" "$BASE/reports/summary?from=2026-11-01&to=2026-11-04"
curl -s -b jar -X POST $BASE/auth/staff/refresh
```

Bentuk body lengkap ada di [OpenApi.yaml](../Api/OpenApi.yaml). Webhook sandbox diuji dengan
simulator Midtrans dashboard ke `<host>/api/v1/payments/midtrans/webhook`.

## 5. Simulasi perangkat keras tanpa perangkat asli

| Perangkat | Simulasi |
|-----------|----------|
| Printer thermal | `GET /pos/orders/:id/receipt?format=escpos&width=58` → simpan `.bin`. Render dengan emulator ESC/POS (misalnya `escpos-tools`/`esc2html`) atau bandingkan dengan golden file `receipt_58mm.golden`. Printer LAN: `nc -l 9100 > out.bin` sebagai printer palsu. |
| Cash drawer | Belum ada perintah kick (G-6). Setelah ditambah, verifikasi byte `1B 70` di awal output. |
| EDC | Pembayaran `edc` dengan referensi manual. Tidak ada perangkat. |
| QRIS dinamis | Midtrans sandbox simulator (bayar dari dashboard simulator). |
| KDS / TV | Dua tab browser dengan peran berbeda; WebSocket diuji dengan `websocat "wss://<host>/ws?ticket=<t>"`. |
| Email | `EMAIL_DRIVER=log` (isi email muncul di log), atau Gmail app password. |
| R2 | `storage.Memory` di test. Demo tanpa R2 menghasilkan `503 MEDIA_UNAVAILABLE`. |

## 6. Checklist kesiapan demo + integrasi frontend

- [ ] G-1, G-2 (dan G-3 bila TV) sudah merge ke `staging`
- [ ] L-1, L-2, L-3 sudah ditutup
- [ ] Image demo = digest rilis terbaru; `migrate up` sukses; seed demo idempoten
- [ ] Named Tunnel + Cloudflare Access aktif; `APP_PUBLIC_URL` dan CORS mengarah ke domain frontend
- [ ] Midtrans sandbox: Payment Notification URL di-set, satu transaksi QRIS sukses end-to-end
- [ ] Frontend: login → shift → pesanan → bayar tunai/QRIS → KDS → struk → laporan, tanpa error
- [ ] WebSocket reconnect diuji (matikan Wi-Fi 30 detik)
- [ ] Skenario cadangan bila internet venue mati: video rekaman alur
- [ ] Reset data demo (`down -v` + seed) setelah demo

## 7. Kesiapan infrastruktur

| Area | Demo (laptop + tunnel) | Produksi (VPS) |
|------|------------------------|----------------|
| Compose | `docker-compose.demo.yml` siap | `docker-compose.prod.yml` siap |
| TLS / domain | Cloudflare edge (Quick Tunnel) | Blocked: domain belum dibeli |
| Backup | Tidak perlu | Skrip siap; drill di VPS belum |
| Monitoring | `docker compose logs` | Alloy → Grafana Cloud siap; akun belum dibuat |
| Deploy | `up -d --build` | `deploy.sh <digest>` dengan rollback; belum pernah dijalankan di VPS |
| Uptime | Bergantung pada laptop menyala | VPS KVM 2 |

## 8. Strategi server demo

Diputuskan (DM4): **laptop + Quick Tunnel** untuk demo tatap muka. Urutan pada hari demo:

1. Laptop disambungkan ke charger, sleep dimatikan, dan semua perangkat terhubung ke hotspot HP owner
   (bukan Wi-Fi kafe).
2. Jalankan `docker compose -f deploy/docker/docker-compose.demo.yml up -d` dan frontend di `:3000`.
3. Ambil URL tunnel, set `APP_PUBLIC_URL`/CORS, dan Payment Notification URL Midtrans sandbox.
4. Cetak QR meja. **Setelah ini tunnel tidak boleh di-restart**, karena URL dan semua QR akan mati.
5. Jalankan smoke test §4. Bila gagal, putar video cadangan.

VPS + domain + Named Tunnel ditunda sampai ada klien yang membayar. Saat itu demo pindah ke
`demo.<domain>` di VPS yang sama dengan produksi (DB terpisah), dan menjadi gladi bersih deploy,
backup, dan monitoring.

## 9. Yang butuh persetujuan owner

| Keputusan | Kenapa butuh persetujuan |
|-----------|--------------------------|
| Beli domain + VPS KVM 2 + mailbox | Biaya |
| Named Tunnel / Cloudflare Access / firewall (P0-07) | Konfigurasi deploy |
| Akun Grafana Cloud, Midtrans produksi | Akun dan kredensial |
| G-1, G-2, G-4 endpoint baru (kontrak API publik) | Kontrak API |
| G-5 remote approval (tabel `approval_requests`, alur auth) | Model auth/izin |
| L-2 wajib secret kuat di `staging` | Konfigurasi deploy |
| Refresh token untuk klien native (bila tanpa cookie jar) | Model auth |
| Peran `manager` | Model izin |
| Branding di `store_settings` | Skema + kontrak |
| Mencairkan fitur beku (ADR-005, C5, promo, loyalty, delivery) | Arahan owner 2026-11-03 |
| RF1-a/b/d/f refund Midtrans | Uang nyata, setelah uji sandbox |
| Setiap merge PR dan promosi `staging` → `main` | Aturan proyek |
