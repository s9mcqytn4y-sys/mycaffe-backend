# Cassy Backend: Project Instructions (`CLAUDE.md`)

> Lihat [`AGENTS.md`](AGENTS.md) dan [`GEMINI.md`](GEMINI.md) untuk kontrak lengkap produk, serta direktori [`docs/Product/ReAlignment2026-11.md`](docs/Product/ReAlignment2026-11.md) untuk keputusan re-alignment, serta [`memory/`](memory/) untuk status sesi terkini (`STATUS.md`, `TODO.md`, `DECISIONS.md`, `TOOLCHAIN_CATALOG.md`).

## 1. Peran Repository & Stack Inti
- **Repo**: `mycaffe-backend` (Modular Monolith Go 1.25+ / Gin Gonic / `sqlx` / `pgx/v5` / PostgreSQL 18 RLS / Cloudflare R2 / Midtrans Core API / Sentry).
- **Frontend**: Terpisah di repository Next.js App Router + TypeScript + Tailwind CSS/PWA. Jangan membuat kode frontend di repo ini.
- **Domain & CDN Demo**:
  - Domain utama: `abbdullaziz.online` (Cloudflare Zone `7fa2f37d613b1bb7bc0c3a830c023730`)
  - Demo API Tunnel: `https://demo.abbdullaziz.online` (`mycaffe-demo` tunnel `85b97ba7-79d7-48d7-b045-b62645435708`)
  - Public Media CDN (R2 `mycaffe-media`): `https://cdn.abbdullaziz.online`

## 2. Kontrak Keamanan & Finansial Wajib
1. **Multi-Tenant RLS**: Setiap query/mutasi wajib berjalan di dalam `database.WithTenantTx(ctx, db, storeID, ...)` menggunakan role `mycaffe_app` (`NOBYPASSRLS`). Hanya fungsi `SECURITY DEFINER` resmi (`resolve_payment_webhook_context`, `resolve_store_for_login`, `resolve_table_qr`, `track_order_by_token`, `resolve_split_share`, `list_stores_with_*`) yang boleh dipanggil sebelum `WithTenantTx`.
2. **Whole Integer IDR (`int64` / `BIGINT`)**: Dilarang menggunakan `float` untuk nominal uang. Kalkulasi harga produk, varian, modifier, 1-level sub-addon, diskon, pajak (`tax_bps`), dan service charge (`service_bps`) wajib dihitung ulang di server.
3. **2-Phase Midtrans Core API (No Snap)**: Gunakan `payments.id.String()` sebagai `provider_order_id` sekaligus `Idempotency-Key`. Jangan pernah menahan lock transaksi DB saat melakukan panggilan HTTP eksternal ke Midtrans.
4. **Immutability Migrasi**: Jangan pernah mengubah file migrasi yang sudah di-merge ke `main` karena `cmd/migrate` memverifikasi checksum SHA-256 di `schema_migrations`. Selalu buat migrasi aditif baru.

## 3. Perintah Verifikasi Kualitas (Wajib Sebelum Commit/PR)
```powershell
gofmt -l .
go vet ./...
go test -v -race ./...
go run ./cmd/migrate -action=up
go run ./cmd/migrate -action=seed
```
