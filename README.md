# MyCaffe Core POS — Backend API

Backend RESTful API dan Realtime Hub untuk platform white-label multi-tenant QR Table Ordering, Point of Sale (POS), Kitchen Display System (KDS), dan TV Queue Display untuk kafe dan restoran.

## 🚀 Tech Stack
- **Language**: Go 1.27.x
- **Web Framework**: Gin Gonic (`v1.12.0`)
- **Database Driver**: `jackc/pgx/v5` & `jmoiron/sqlx`
- **Database Engine**: PostgreSQL 18-alpine
- **Infrastructure**: Docker Compose & Nginx Reverse Proxy (Hostinger KVM 2 / Ubuntu 24.04 LTS)
- **Payment Gateway**: Midtrans Core API (Direct Dynamic QRIS & Virtual Account)
- **Realtime Hub**: Gorilla WebSocket (`internal/realtime`)
- **Offsite Backup**: Cloudflare R2 (Encrypted `pg_dump` with zero egress fee)
- **Monitoring & Error Tracking**: Sentry Go SDK

---

## 🛡️ Security & Architecture Guarantees
1. **Multi-Tenant Scoping**: Setiap query dan event WebSocket di-scope ketat berdasarkan `store_id` (tenant UUID). Event kosong tidak pernah di-broadcast secara global.
2. **Payment Integrity**: Harga dan kalkulasi pajak dihitung ulang di sisi server dari katalog resmi. Nominal uang menggunakan integer Rupiah (`int64` / PostgreSQL `BIGINT`).
3. **Webhook Verification**: Webhook Midtrans diverifikasi menggunakan signature key resmi SHA512:
   $$\text{SHA512}(\text{order\_id} + \text{status\_code} + \text{gross\_amount} + \text{ServerKey})$$
4. **Idempotent Webhook Logging**: Tabel `payment_notification_logs` memiliki constraint unik gabungan untuk mencegah eksekusi ganda callback notifikasi.
5. **Fail-Closed Configuration**: Startup server otomatis ditolak (`log.Fatalf`) pada environment `production` jika secret key masih default atau kredensial database kosong.

---

## 📊 Feature Implementation Status

| Feature / Endpoint | Status | Description |
| :--- | :--- | :--- |
| **Authentication & Token Validator** (`internal/pkg/auth`) | **Implemented** | JWT HMAC-SHA256 dengan validasi ketat: masa berlaku (`exp`), issuer (`mycaffe-backend`), audience (`mycaffe-pos`), UUID store & user, serta validasi role. |
| **Realtime WebSocket Hub** (`/ws`) | **Implemented** | Tenant-scoped event distribution. Wajib menyertakan token JWT autentik dan `store_id` yang cocok. Fail-closed di production jika token tidak valid. |
| **Database Migrations & Seeds** (`db/migrations/`, `db/seeds/`) | **Implemented** | Schema PostgreSQL 18 dengan `ON DELETE RESTRICT` pada histori pesanan, sequence harian atomik, dan seeds development netral tanpa identitas statis. |
| **Docker & Nginx Deployment** (`deploy/docker/`, `deploy/nginx/`) | **Implemented** | Multi-stage Dockerfile, `docker-compose.yml`, reverse proxy Nginx dengan rate limiting dan WebSocket upgrades. |
| **Order Creation** (`POST /api/v1/orders`) | **Guarded / Simulation** | Menjalankan validasi input format dan kalkulasi total IDR di memori. |
| **Payment Webhook** (`POST /api/v1/payments/midtrans/webhook`) | **Guarded / Fail-Closed** | Memvalidasi HMAC SHA512 signature Midtrans. |
| **Catalog & Menu Management** | **Planned** | CRUD kategori, produk, modifier groups, dan kontrol stok per tenant. |
| **Payment Ledger & Reconciliation** | **Planned** | Transaksi ACID database untuk idempotency log, payment update, dan auto-kds dispatch. |

---

## 🛠️ Local Development Setup

### 1. Prasyarat
- Go 1.24+ terpasang di PATH
- Docker & Docker Compose

### 2. Konfigurasi Environment
Salin template konfigurasi:
```powershell
Copy-Item .env.example .env
```
Sesuaikan nilai `DATABASE_URL` dan `MIDTRANS_SERVER_KEY` untuk environment sandbox lokal.

### 3. Menjalankan Database Lokal via Docker
```powershell
docker compose -f deploy/docker/docker-compose.yml up -d postgres
```

### 4. Menjalankan Tes & Linting
```powershell
gofmt -l .
go vet ./...
go test -v -race ./...
```

### 5. Menjalankan Aplikasi
```powershell
go run cmd/api/main.go
```

---

## 🌿 Git Branching Strategy
- `main`: Branch produksi yang dilindungi. Dilarang commit langsung ke `main`.
- `staging`: Branch integrasi pra-rilis.
- `feature/*`: Branch pengembangan tugas spesifik yang dimerge via Pull Request setelah lolos checks CI GitHub Actions.
