# Kafe Senja Vibe — Backend API

Backend RESTful API dan Realtime Hub untuk platform white-label QR Table Ordering, Point of Sale (POS), Kitchen Display System (KDS), dan TV Queue Display untuk kafe dan restoran.

## 🚀 Tech Stack
- **Language**: Go 1.27.x
- **Web Framework**: Gin Gonic (`v1.12.0`)
- **Database Access**: `jackc/pgx/v5` & `jmoiron/sqlx`
- **Database Engine**: PostgreSQL 17 (Supabase Development & Managed Production)
- **Payment Gateway**: Midtrans Core API (Direct Dynamic QRIS & Virtual Account)
- **Realtime Hub**: Gorilla WebSocket (`internal/realtime`)
- **Monitoring & Error Tracking**: Sentry Go SDK
- **Token Efficiency**: RTK (`rtk-ai/rtk`)

---

## 🛡️ Security & Architecture Guarantees
1. **Multi-Tenant Scoping**: Setiap query dan event WebSocket di-scope ketat berdasarkan `store_id` (tenant UUID). Event kosong tidak pernah di-broadcast secara global.
2. **Payment Integrity**: Harga dan kalkulasi pajak dihitung ulang di sisi server dari katalog resmi. Nominal uang menggunakan integer Rupiah (`int64`).
3. **Webhook Verification**: Webhook Midtrans diverifikasi menggunakan signature key resmi SHA512:
   $$\text{SHA512}(\text{order\_id} + \text{status\_code} + \text{gross\_amount} + \text{ServerKey})$$
4. **Fail-Closed Configuration**: Startup server otomatis ditolak (`log.Fatalf`) pada environment `production` jika secret key masih default atau kredensial database kosong.

---

## 🛠️ Local Development Setup

### 1. Prasyarat
- Go 1.27+ terpasang di PATH
- Node.js & Supabase CLI (`npx supabase`)

### 2. Konfigurasi Environment
Salin template konfigurasi:
```powershell
Copy-Item .env.example .env
```
Sesuaikan nilai `DATABASE_URL` dan `MIDTRANS_SERVER_KEY` untuk environment sandbox lokal.

### 3. Menjalankan Database Migrations
Struktur migrasi version-controlled tersedia di `supabase/migrations/`:
```powershell
npx supabase migration list
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
Dokumentasi API Swagger interaktif dapat diakses pada `http://localhost:8080/swagger/index.html` (saat server berjalan).

---

## 🌿 Git Branching Strategy
- `main`: Branch produksi yang dilindungi. Dilarang commit langsung ke `main`.
- `staging`: Branch integrasi pra-rilis.
- `feature/*`: Branch pengembangan tugas spesifik yang dimerge via Pull Request setelah lolos checks CI GitHub Actions.

