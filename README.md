# MyCaffe Core POS: Backend API

Backend RESTful API dan Realtime Hub untuk platform white-label multi-tenant QR Table Ordering, Point of Sale (POS), Kitchen Display System (KDS), dan TV Queue Display untuk kafe dan restoran. Lihat [Brand Guidelines, Merk, dan Product Knowledge](docs/product/brand-and-product-knowledge.md) untuk identitas merek resmi, token desain visual (*Espresso & Crema*), format penomoran entitas, serta aturan bisnis produk.

## Tech Stack
- **Language**: Go 1.27.3
- **Web Framework**: Gin Gonic (`v1.12.0`)
- **Database Driver**: `jackc/pgx/v5` & `jmoiron/sqlx`
- **Database Engine**: PostgreSQL 18-alpine (Native Row-Level Security & Triple-Role Isolation)
- **Infrastructure**: Docker Compose (`docker-compose.yml` & `docker-compose.prod.yml`) & Nginx Reverse Proxy (Hostinger KVM 2 / Ubuntu 24.04 LTS)
- **Payment Gateway**: Midtrans Core API Custom UI (`qris`, `bank_transfer`, `gopay`, `shopeepay`, `card`, `cstore`) + Cashier Offline (`cash`, `static_qris`, `edc`)
- **Realtime Hub**: Gorilla WebSocket (`internal/realtime`)
- **Cloudflare R2 Storage**: Triple-Bucket (`mycaffe-media` untuk CDN gambar katalog publik, `mycaffe-private-media` untuk presigned GET 15m QRIS statis/dokumen, & `mycaffe-backups` untuk backup `pg_dump` terenkripsi GPG AES256+SHA512)
- **Monitoring & Error Tracking**: Sentry Go SDK

---

## Security & Architecture Guarantees
1. **Multi-Tenant Scoping**: Setiap query, mutasi database (`database.WithTenantTx`), dan event WebSocket di-scope ketat berdasarkan `store_id` (tenant UUID) yang terautentikasi.
2. **Payment Integrity**: Harga produk, opsi modifier, 1-level sub-addon, diskon, service charge, dan pajak dihitung ulang sepenuhnya di sisi server dari katalog resmi menggunakan integer Rupiah (`int64` / PostgreSQL `BIGINT`) dan Basis Points (`BPS`).
3. **Order Idempotency & Atomic Stock Locking**: Pembuatan pesanan di `OrderService` mengunci baris stok produk dan opsi modifier secara terurut (`SELECT ... ORDER BY id ASC FOR UPDATE`), memvalidasi `idempotency_key` beserta `orders.request_hash` (SHA-256), mengalokasikan nomor urut harian (`store_daily_sequences`), dan menulis event ke `outbox_events` dalam satu transaksi ACID.
4. **Webhook Verification**: Webhook Midtrans diverifikasi menggunakan signature key resmi SHA-512:
   $$\text{SHA512}(\text{order\_id} + \text{status\_code} + \text{gross\_amount} + \text{ServerKey})$$
5. **Fail-Closed Operations**: Startup server otomatis ditolak (`log.Fatalf`) pada environment `staging` dan `production` jika secret key masih default atau kredensial database kosong. Endpoint HTTP publik yang belum diikat ke middleware identitas tenant atau repositori pembayaran otomatis mengembalikan `503 Service Unavailable`.
6. **Zero In-Place Coding on Production VPS**: Dilarang mengedit kode sumber, mengubah migrasi SQL, atau menjalankan `go build` langsung di server VPS produksi. Seluruh perubahan wajib melalui alur `feature/*` -> PR `staging` (CI hijau) -> PR `main` -> penarikan image kontainer bertanda versi dari `ghcr.io`.

---

## Feature Implementation Status (4-Tier Matrix)

Status setiap komponen diklasifikasikan ke dalam empat kategori tegas: **Implemented & Tested**, **Guarded / Fail-Closed**, **Planned**, atau **Blocked by Dependency**.

| Feature / Module | Status | Verification & Scope Details |
| :--- | :--- | :--- |
| **Staff Authentication & Sessions** (Phase 02.01-B, `internal/service/auth_service.go`) | **Implemented & Tested** | Login `store_slug + email + PIN 6 digit` (argon2id), access token 15 menit, refresh berotasi (cookie HttpOnly) dengan deteksi reuse, maks 3 sesi, idle 2 jam, lockout 5×/15 menit, CLI `bootstrap-store` / `reset-owner-pin`. |
| **Email Outbox & Staff Account Lifecycle** (Phase 02, `internal/pkg/mailer`) | **Implemented & Tested** | Undangan staf, lupa/reset PIN, PIN sementara, notifikasi akun terkunci; antrean `email_outbox` + worker retry; Gmail (dev/demo) / Hostinger SMTP (produksi). |
| **RBAC, Store Settings & POS Routes** (Phase 02.01-C, `internal/pkg/rbac`) | **Implemented & Tested** | Matriks izin 4 peran, `RequirePermission`, `/me.permissions`, `store_settings` (ambang void/diskon/selisih kas, toggle kasir-barista), `POST /api/v1/pos/orders` & `/pos/orders/{id}/payments` dengan toko dan pelaku dari token. |
| **Database Migrations, Triple-Role & RLS** (`db/migrations/`, `cmd/migrate`) | **Implemented & Tested** | Skema PostgreSQL 18 (17 tabel) dengan pemisahan `postgres`, `mycaffe_migrator`, dan `mycaffe_app` (`NOSUPERUSER NOBYPASSRLS`), `ON DELETE RESTRICT` pada histori finansial, checksum SHA-256 pada `schema_migrations`, dan migrasi aditif `orders.request_hash`. |
| **Catalog Pricing Engine & 1-Level Sub-Addons** (`internal/service/pricing.go`) | **Implemented & Tested** | Validasi server-side untuk ketersediaan produk/modifier, aturan grup (`single`/`multiple`, `min_select`/`max_select`, `override_required`), sub-addon 1 level (`OptionModifierGroup`), pembulatan BPS *Round Half Up*, dan proteksi overflow `int64`. |
| **Transactional Order Repository & Service** (`internal/repository/postgres`, `internal/service/order_service.go`) | **Implemented & Tested** | Eksekusi transaksi penuh di dalam `database.WithTenantTx`: deteksi idempotensi & konflik payload (`ErrIdempotencyConflict`), penguncian & pengurangan stok atomik (`FOR UPDATE`), alokasi nomor pesanan (`ORD-YYYYMMDD-XXXX`) & antrean (`A001`), penyimpanan `orders`, `order_items`, `order_item_options`, `payment_splits`, serta `outbox_events`. Diuji pada PostgreSQL 18 sebagai `mycaffe_app`. |
| **Docker, Nginx TLS Bootstrap & R2 Backup Scripts** (`deploy/`, `scripts/`) | **Implemented & Tested** | Multi-stage Dockerfile (`api` + `migrate`), `docker-compose.prod.yml`, entrypoint bootstrap TLS mandiri Nginx, serta skrip backup/restore terenkripsi GPG AES256+SHA512 ke Cloudflare R2. |
| **Guest QR Order Endpoint** (`POST /api/v1/orders`) | **Guarded / Fail-Closed** | `503` di `staging`/`production` sampai signed table QR + guest session (02.01-D/E). Mode simulasi hanya di `development`. |
| **Realtime WebSocket Hub** (`/ws`) | **Guarded / Fail-Closed** | Memvalidasi token JWT dan kesesuaian `store_id` pada `development`; mengembalikan `503 Service Unavailable` pada `staging` dan `production` hingga tiket sesi WebSocket dan relay `outbox_events` diaktifkan. |
| **Midtrans Webhook Callback** (`POST /api/v1/payments/midtrans/webhook`) | **Implemented & Tested** | Verifikasi signature SHA-512, batas body `64 KB`, `IDR`, integer `gross_amount`, pemetaan status; inbox durable `payment_notification_logs` (hash SHA-256 per event), proteksi notifikasi out-of-order, deteksi selisih nominal dan pembayaran ganda (`payment.refund_required`). Mengembalikan `503` (`Retry-After: 60`) hanya bila commit database gagal agar Midtrans mengulang. |
| **Payment Ledger & Reconciliation Worker** (Phase 01 Sub-task 04) | **Implemented & Tested** | Tunai/static QRIS/EDC beraudit; Midtrans Core API dua fase; idempotensi; worker kedaluwarsa & `CheckTransaction`; deteksi pembayaran ganda. Diakses lewat rute POS terautentikasi. |
| **Catalog: Menu, Variants & CRUD** (iteration 3, `internal/service/catalog_service.go`) | **Implemented & Tested** | Katalog staf & publik (ETag/304), varian + SKU, tipe menu & stasiun, CRUD kategori/produk/varian dengan error 422/409 per kolom, toggle habis per stasiun, audit perubahan harga, arsip. |
| **Catalog: Modifiers & Order Quote Engine** (iteration 4a, `internal/service/pricing.go`) | **Implemented & Tested** | CRUD grup & opsi modifier, delta harga negatif (P1), validasi kedalaman sub-addon maks 1 & deteksi siklus graf, audit perubahan harga atomik, otorisasi POS quote terikat RBAC (`order.pos.create`), dan quote fingerprint SHA-256 anti-perubahan harga kompensasi (P8) dengan proteksi konflik optimistik 409 (`QUOTE_CHANGED`). |
| **Cloudflare R2 Storage Infrastructure** (Keputusan K9, `scripts/cloudflare/`) | **Implemented & Verified** | Provisioning otomatis Triple-Bucket (`mycaffe-media` publik, `mycaffe-private-media` privat presigned GET 15m, `mycaffe-backups` privat terenkripsi), CORS browser aktif, dan skrip restore drill dengan proteksi non-destruktif database operasional. Media upload handler (C4) dijadwalkan pada fase berikutnya. |
| **Shift & Cash Drawer Management** (Iteration 4b-1, `internal/service/shift_service.go`) | **Implemented & Tested** | Buka/tutup shift kasir, rekonsiliasi kas (expected vs actual), deteksi selisih (discrepancy), pencatatan pergerakan kas masuk/keluar (`cash_movements` append-only trigger anti-update/delete), ringkasan X-Report & Z-Report, dan force-close shift oleh Manajer/Owner. |
| **POS Cash Tender & Drawer Session Binding** (Iteration 4b-2, `internal/service/payment_service.go`) | **Implemented & Tested** | Tender langsung tunai di POS wajib terikat pada shift aktif dan laci kas kasir bersangkutan (`ErrNoActiveShift`, `ErrShiftDrawerMismatch`). Mutasi kas masuk otomatis tercatat dalam transaksi pembayaran atomik. |
| **Open Tab & Incremental Order Rounds** (Iteration 4c-1, `internal/service/tab_service.go`) | **Implemented & Tested** | Buka tagihan meja/nama tamu (`POST /pos/tabs`), advisory lock anti-race condition pembukaan ganda, submit ronde bertahap (`POST /pos/tabs/{id}/rounds`) dengan idempotensi penuh, rekalkulasi kumulatif pajak & service charge yang matematis identik dengan pay-first, guard pembayaran tab yang belum ditutup (`409 TAB_NOT_CLOSED`), dan penutupan tab pra-pembayaran (`POST /pos/tabs/{id}/close`). |
| **Split Bill, Table QR, KDS Realtime Hub, Media Upload** | **Planned** | Lihat `docs/architecture/roadmap.md`, `docs/architecture/iteration-4c2-split-bill-plan.md`, dan `docs/architecture/pos-operations-evaluation.md`. |
| **Live Midtrans Production & Hostinger VPS Cutover** | **Blocked by Dependency** | Menunggu kredensial produksi Midtrans, domain/DNS aktif, dan provisioning Hostinger KVM 2 VPS oleh pemilik proyek. |

---

## Local Development Setup (Windows)

Prasyarat: Go 1.27.3, Docker Desktop. Semua perintah ada di `scripts/dev.ps1`
(detail: [docs/setup/windows-dev-environment.md](docs/setup/windows-dev-environment.md)).

```powershell
./scripts/dev.ps1 setup      # .env dari template, PostgreSQL 18 lokal, migrasi, seed
./scripts/dev.ps1 run        # API di http://localhost:8080
./scripts/dev.ps1 login      # uji login staf dengan PIN dev 482913
./scripts/dev.ps1 test-int   # tes integrasi PostgreSQL 18 (sama seperti CI)
./scripts/dev.ps1 check      # gofmt, vet, mod tidy, govulncheck, tes
./scripts/dev.ps1 demo-up    # demo publik lewat Cloudflare Quick Tunnel
```

`dev.ps1` selalu memakai PostgreSQL Docker lokal, walaupun `DATABASE_URL` di `.env` menunjuk host lain.

## Dokumentasi

| Topik | Dokumen |
|-------|---------|
| Keputusan owner (semua sesi wawancara) | [docs/architecture/decision-register.md](docs/architecture/decision-register.md) |
| Roadmap & status sub-task | [docs/architecture/roadmap.md](docs/architecture/roadmap.md) |
| Use case, alur & sequence per peran | [docs/architecture/pos-use-cases-and-sequences.md](docs/architecture/pos-use-cases-and-sequences.md) |
| Evaluasi operasional POS & backlog | [docs/architecture/pos-operations-evaluation.md](docs/architecture/pos-operations-evaluation.md) |
| Register risiko keamanan | [docs/architecture/security-risk-register.md](docs/architecture/security-risk-register.md) |
| Katalog, varian, BOM, inventaris, media | [docs/architecture/catalog-inventory-design.md](docs/architecture/catalog-inventory-design.md) |
| Kebutuhan owner, staf, pelanggan | [docs/product/requirements-owner-staff-customer.md](docs/product/requirements-owner-staff-customer.md) |
| Auth, sesi & RBAC | [docs/architecture/adr-003-staff-authentication-session-and-rbac.md](docs/architecture/adr-003-staff-authentication-session-and-rbac.md) |
| Modernisasi Skema Menu & Modifier (ADR-004) | [docs/architecture/adr-004-catalog-schema-modernization.md](docs/architecture/adr-004-catalog-schema-modernization.md) |
| Rencana Arsitektur Split Bill (Iterasi 4c-2) | [docs/architecture/iteration-4c2-split-bill-plan.md](docs/architecture/iteration-4c2-split-bill-plan.md) |
| Topologi VPS | [docs/architecture/adr-002-single-vps-docker-topology.md](docs/architecture/adr-002-single-vps-docker-topology.md) |
| Kontrak database & ledger pembayaran | [phase-01-database-contract.md](docs/architecture/phase-01-database-contract.md), [phase-01-subtask-04-payment-ledger-spec.md](docs/architecture/phase-01-subtask-04-payment-ledger-spec.md) |
| Email, demo, lingkungan Windows | [docs/setup/email.md](docs/setup/email.md), [docs/setup/demo-staging-environment.md](docs/setup/demo-staging-environment.md), [docs/setup/windows-dev-environment.md](docs/setup/windows-dev-environment.md) |
| Kontrak API & standar CRUD/error | [docs/api/openapi.yaml](docs/api/openapi.yaml), [docs/architecture/api-conventions.md](docs/architecture/api-conventions.md) |

---

## Git Branching & Release Strategy
- `main`: Branch produksi yang dilindungi. Setiap merge ke `main` memicu workflow rilis image kontainer bertanda versi ke GitHub Container Registry (`ghcr.io`).
- `staging`: Branch integrasi pra-rilis. Seluruh PR wajib lolos 3 job CI (`build-and-test`, `docker-build`, `database-migration-test`) sebelum ditinjau.
- `feature/*`: Branch pengembangan fitur atau perbaikan spesifik yang dibuat dari baseline stabil dan diajukan melalui Pull Request ke `staging`.
