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
- **Cloudflare R2 Storage**: Dual-Bucket (`mycaffe-media` untuk CDN gambar katalog & `mycaffe-backups` untuk backup `pg_dump` terenkripsi GPG AES256+SHA512)
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
| **Authentication & Token Validator** (`internal/pkg/auth`) | **Implemented & Tested** | JWT HMAC-SHA256 dengan validasi algoritma, masa berlaku (`exp`), issuer (`mycaffe-backend`), audience (`mycaffe-pos`), UUID `store_id` & `user_id`, serta 4 role operasional (`owner`, `cashier`, `barista`, `kitchen`). |
| **Database Migrations, Triple-Role & RLS** (`db/migrations/`, `cmd/migrate`) | **Implemented & Tested** | Skema PostgreSQL 18 (17 tabel) dengan pemisahan `postgres`, `mycaffe_migrator`, dan `mycaffe_app` (`NOSUPERUSER NOBYPASSRLS`), `ON DELETE RESTRICT` pada histori finansial, checksum SHA-256 pada `schema_migrations`, dan migrasi aditif `orders.request_hash`. |
| **Catalog Pricing Engine & 1-Level Sub-Addons** (`internal/service/pricing.go`) | **Implemented & Tested** | Validasi server-side untuk ketersediaan produk/modifier, aturan grup (`single`/`multiple`, `min_select`/`max_select`, `override_required`), sub-addon 1 level (`OptionModifierGroup`), pembulatan BPS *Round Half Up*, dan proteksi overflow `int64`. |
| **Transactional Order Repository & Service** (`internal/repository/postgres`, `internal/service/order_service.go`) | **Implemented & Tested** | Eksekusi transaksi penuh di dalam `database.WithTenantTx`: deteksi idempotensi & konflik payload (`ErrIdempotencyConflict`), penguncian & pengurangan stok atomik (`FOR UPDATE`), alokasi nomor pesanan (`ORD-YYYYMMDD-XXXX`) & antrean (`A001`), penyimpanan `orders`, `order_items`, `order_item_options`, `payment_splits`, serta `outbox_events`. Diuji pada PostgreSQL 18 sebagai `mycaffe_app`. |
| **Docker, Nginx TLS Bootstrap & R2 Backup Scripts** (`deploy/`, `scripts/`) | **Implemented & Tested** | Multi-stage Dockerfile (`api` + `migrate`), `docker-compose.prod.yml`, entrypoint bootstrap TLS mandiri Nginx, serta skrip backup/restore terenkripsi GPG AES256+SHA512 ke Cloudflare R2. |
| **Public Order HTTP Endpoint** (`POST /api/v1/orders`) | **Guarded / Fail-Closed** | Mengembalikan `503 Service Unavailable` pada `staging` dan `production` hingga middleware identitas tenant (JWT staf atau signed table QR token) diikat pada rute HTTP publik. Mode simulasi hanya aktif pada `development`. |
| **Realtime WebSocket Hub** (`/ws`) | **Guarded / Fail-Closed** | Memvalidasi token JWT dan kesesuaian `store_id` pada `development`; mengembalikan `503 Service Unavailable` pada `staging` dan `production` hingga tiket sesi WebSocket dan relay `outbox_events` diaktifkan. |
| **Midtrans Webhook Callback** (`POST /api/v1/payments/midtrans/webhook`) | **Implemented & Tested** | Verifikasi signature SHA-512, batas body `64 KB`, `IDR`, integer `gross_amount`, pemetaan status; inbox durable `payment_notification_logs` (hash SHA-256 per event), proteksi notifikasi out-of-order, deteksi selisih nominal dan pembayaran ganda (`payment.refund_required`). Mengembalikan `503` (`Retry-After: 60`) hanya bila commit database gagal agar Midtrans mengulang. |
| **Payment Initiation, Ledger & Reconciliation Worker** (Phase 01 Sub-task 04) | **Implemented & Tested** (layanan) / **Guarded** (HTTP) | Tunai/static QRIS/EDC dalam satu transaksi beraudit; Midtrans Core API dua fase tanpa menahan lock saat HTTP; idempotensi `idempotency_key` + `request_hash`; worker kedaluwarsa pesanan & rekonsiliasi `CheckTransaction`. Rute `POST /api/v1/orders/:id/payments/initiate` tetap `503` di `staging`/`production` sampai auth Phase 02. Diuji di PostgreSQL 18. |
| **Signed Table QR Token & Staff Auth HTTP Middleware** (Phase 02) | **Planned** | Pengikatan konteks `store_id` terautentikasi pada endpoint HTTP publik dan kasir untuk membuka guard `503` pada `POST /api/v1/orders`. |
| **Live Midtrans Production & Hostinger VPS Cutover** | **Blocked by Dependency** | Menunggu kredensial produksi Midtrans, domain/DNS aktif, dan provisioning Hostinger KVM 2 VPS oleh pemilik proyek. |

---

## Local Development Setup

### 1. Prasyarat
- Go 1.27.3 terpasang di PATH
- Docker & Docker Compose

### 2. Konfigurasi Environment
Salin template konfigurasi:
```powershell
Copy-Item .env.example .env
```
Sesuaikan nilai `DATABASE_URL`, `DATABASE_MIGRATION_URL`, dan `MIDTRANS_SERVER_KEY` untuk environment sandbox lokal.

### 3. Menjalankan Database Lokal & Migrasi via Docker
```powershell
docker compose -f deploy/docker/docker-compose.yml up -d postgres
go run ./cmd/migrate -action=up
go run ./cmd/migrate -action=seed
```

### 4. Menjalankan Tes & Linting
```powershell
gofmt -l .
go vet ./...
go test -v -race ./...
```

### 5. Menjalankan Aplikasi
```powershell
go run ./cmd/api
```

---

## Git Branching & Release Strategy
- `main`: Branch produksi yang dilindungi. Setiap merge ke `main` memicu workflow rilis image kontainer bertanda versi ke GitHub Container Registry (`ghcr.io`).
- `staging`: Branch integrasi pra-rilis. Seluruh PR wajib lolos 3 job CI (`build-and-test`, `docker-build`, `database-migration-test`) sebelum ditinjau.
- `feature/*`: Branch pengembangan fitur atau perbaikan spesifik yang dibuat dari baseline stabil dan diajukan melalui Pull Request ke `staging`.
