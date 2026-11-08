# MyCaffe Core POS: Backend API

Backend RESTful API dan Realtime Hub untuk platform white-label multi-tenant QR Table Ordering, Point of Sale (POS), Kitchen Display System (KDS), dan TV Queue Display untuk kafe dan restoran. Lihat [Brand Guidelines, Merk, dan Product Knowledge](docs/Product/BrandAndProductKnowledge.md) untuk identitas merek resmi, token desain visual (*Espresso & Crema*), format penomoran entitas, serta aturan bisnis produk.

## Tech Stack
- **Language**: Go 1.27.3
- **Web Framework**: Gin Gonic (`v1.12.0`)
- **Database Driver**: `jackc/pgx/v5` & `jmoiron/sqlx`
- **Database Engine**: PostgreSQL 18-alpine (Native Row-Level Security & Triple-Role Isolation)
- **Infrastructure**: Docker Compose (`docker-compose.yml` & `docker-compose.prod.yml`) & Nginx Reverse Proxy (Hostinger KVM 2 / Ubuntu 24.04 LTS)
- **Payment Gateway**: Midtrans Core API Custom UI (`qris`, `bank_transfer`, `gopay`, `shopeepay`, `card`, `cstore`) + Cashier Offline (`cash`, `static_qris`, `edc`)
- **Realtime Hub**: Gorilla WebSocket (`internal/realtime`)
- **Cloudflare R2 Storage**: Triple-Bucket (`mycaffe-media` di `https://cdn.abbdullaziz.online` untuk CDN gambar katalog publik, `mycaffe-private-media` untuk presigned GET 15m QRIS statis/dokumen, & `mycaffe-backups` untuk backup `pg_dump` terenkripsi GPG AES256+SHA512)
- **Monitoring & Error Tracking**: Sentry Go SDK (dengan middleware panic/5xx recovery & PII scrubbing) + Prometheus `/metrics` (`:9091`) & Grafana Alloy

---

## Security & Architecture Guarantees
1. **Multi-Tenant Scoping**: Setiap query, mutasi database (`database.WithTenantTx`), dan event WebSocket di-scope ketat berdasarkan `store_id` (tenant UUID) yang terautentikasi.
2. **Payment Integrity**: Harga produk, opsi modifier, 1-level sub-addon, diskon, service charge, dan pajak dihitung ulang sepenuhnya di sisi server dari katalog resmi menggunakan integer Rupiah (`int64` / PostgreSQL `BIGINT`) dan Basis Points (`BPS`).
3. **Order Idempotency & Atomic Stock Locking**: Pembuatan pesanan di `OrderService` mengunci baris stok produk dan opsi modifier secara terurut (`SELECT ... ORDER BY id ASC FOR UPDATE`), memvalidasi `idempotency_key` beserta `orders.request_hash` (SHA-256), mengalokasikan nomor urut harian (`store_daily_sequences`), dan menulis event ke `outbox_events` dalam satu transaksi ACID.
4. **Webhook Verification**: Webhook Midtrans diverifikasi menggunakan signature key resmi SHA-512:
   $$\text{SHA512}(\text{order\_id} + \text{status\_code} + \text{gross\_amount} + \text{ServerKey})$$
5. **Fail-Closed Operations**: Startup server ditolak (`log.Fatalf`) bila `DATABASE_URL` kosong atau bila `JWT_SECRET_KEY` lemah/placeholder di `staging` maupun `production` (L-1..L-3 tertutup). Endpoint publik (QR tamu, tracker, struk) memakai token bertanda tangan dan resolver `SECURITY DEFINER` yang sempit; fitur tanpa konfigurasi (mis. R2/SMS) menjawab `503`.
6. **Zero In-Place Coding on Production VPS**: Dilarang mengedit kode sumber, mengubah migrasi SQL, atau menjalankan `go build` langsung di server VPS produksi. Seluruh perubahan wajib melalui alur `feature/*` -> PR `staging` (CI hijau) -> PR `main` -> penarikan image kontainer bertanda versi dari `ghcr.io`.

---

## Feature Status

Status saat ini: **v0.8 (PR 8.5b — 29 migrasi PostgreSQL 18, 42 tabel)**. Status per modul (empat kategori `AGENTS.md`) dipelihara di
satu tempat saja, yaitu [docs/Architecture/Roadmap.md](docs/Architecture/Roadmap.md). Kontrak untuk
frontend ada di
[docs/Architecture/FrontendIntegrationContract.md](docs/Architecture/FrontendIntegrationContract.md).

---

## Local Development Setup (Windows)

Prasyarat: Go 1.27.3, Docker Desktop. Semua perintah ada di `scripts/dev.ps1`
(detail: [docs/Setup/WindowsDevEnvironment.md](docs/Setup/WindowsDevEnvironment.md)).

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
| Indeks docs | [docs/README.md](docs/README.md) |
| Assessment owner, kontrak frontend, checklist demo, draf PRD v2 | [OwnerOperationalAssessment.md](docs/Product/OwnerOperationalAssessment.md), [FrontendIntegrationContract.md](docs/Architecture/FrontendIntegrationContract.md), [DemoReadinessChecklist.md](docs/Setup/DemoReadinessChecklist.md), [PrdV2Brainstorm.md](docs/Product/PrdV2Brainstorm.md) |
| Keputusan owner (semua sesi wawancara) | [docs/Architecture/DecisionRegister.md](docs/Architecture/DecisionRegister.md) |
| Roadmap & status sub-task | [docs/Architecture/Roadmap.md](docs/Architecture/Roadmap.md) |
| Use case, alur & sequence per peran | [docs/Architecture/PosUseCasesAndSequences.md](docs/Architecture/PosUseCasesAndSequences.md) |
| Evaluasi operasional POS 2026-10 (arsip) | [docs/Archive/PosOperationsEvaluation2026-10.md](docs/Archive/PosOperationsEvaluation2026-10.md) |
| Register risiko keamanan | [docs/Architecture/SecurityRiskRegister.md](docs/Architecture/SecurityRiskRegister.md) |
| Katalog, varian, BOM, inventaris, media | [docs/Architecture/CatalogInventoryDesign.md](docs/Architecture/CatalogInventoryDesign.md) |
| Kebutuhan owner, staf, pelanggan | [docs/Product/RequirementsOwnerStaffCustomer.md](docs/Product/RequirementsOwnerStaffCustomer.md) |
| Auth, sesi & RBAC | [docs/Architecture/Adr003StaffAuthenticationSessionAndRbac.md](docs/Architecture/Adr003StaffAuthenticationSessionAndRbac.md) |
| Modernisasi Skema Menu & Modifier (ADR-004) | [docs/Architecture/Adr004CatalogSchemaModernization.md](docs/Architecture/Adr004CatalogSchemaModernization.md) |
| Rencana Split Bill Iterasi 4c-2 (arsip) | [docs/Archive/Iteration4c2SplitBillPlan.md](docs/Archive/Iteration4c2SplitBillPlan.md) |
| Topologi VPS & Skalabilitas Data (ADR-002, ADR-008) | [docs/Architecture/Adr002SingleVpsDockerTopology.md](docs/Architecture/Adr002SingleVpsDockerTopology.md), [docs/Architecture/Adr008DataPlatformAndScaling.md](docs/Architecture/Adr008DataPlatformAndScaling.md) |
| Kontrak database & ledger pembayaran | [Phase01DatabaseContract.md](docs/Architecture/Phase01DatabaseContract.md), [Phase01PaymentLedgerSpec.md](docs/Architecture/Phase01PaymentLedgerSpec.md) |
| Email, SMS/OTP, Demo (`abbdullaziz.online`), Monitoring, Onboarding | [docs/Setup/Email.md](docs/Setup/Email.md), [docs/Setup/Sms.md](docs/Setup/Sms.md), [docs/Setup/DemoStagingEnvironment.md](docs/Setup/DemoStagingEnvironment.md), [docs/Setup/Monitoring.md](docs/Setup/Monitoring.md), [docs/Setup/FirstClientOnboardingRunbook.md](docs/Setup/FirstClientOnboardingRunbook.md) |
| Kontrak API & standar CRUD/error | [docs/Api/OpenApi.yaml](docs/Api/OpenApi.yaml), [docs/Architecture/ApiConventions.md](docs/Architecture/ApiConventions.md) |

## Client Applications & Ecosystem Map

Sistem MyCaffe by AzureDevID mencakup backend dan suite aplikasi client multi-platform:
- **`MyCaffeClient` Monorepo** (`https://github.com/s9mcqytn4y-sys/mycaffe-client`):
  - **`apps/android`**: Cassy POS Native Android Tablet application (`com.azuredevid.cassy`) dengan CameraX store pairing, ESC/POS Port 9100 KDS ticket routing, dan ZXing dynamic QRIS.
  - **`apps/web`**: Guest QR Table Ordering & Owner Backoffice PWA berbasis Next.js 15 App Router dan real-time WebSocket.
  - **`apps/ios`**: iOS Hardware HAL dan printer driver serial/network.
  - **`packages/design-tokens`**: Master token visual *Espresso & Crema* dan AzureDevID Control Plane.
- **`AzureDevId` Vault**: Dokumen legalitas (HAKI, PSE), panduan brand vector resmi, kontrak payment gateway Midtrans, dan kunci security recovery.

---

## Git Branching & Release Strategy
- `main`: Branch produksi yang dilindungi. Setiap merge ke `main` memicu workflow rilis image kontainer bertanda versi ke GitHub Container Registry (`ghcr.io`).
- `staging`: Branch integrasi pra-rilis. Seluruh PR wajib lolos 3 job CI (`build-and-test`, `docker-build`, `database-migration-test`) sebelum ditinjau.
- `feature/*`: Branch pengembangan fitur atau perbaikan spesifik yang dibuat dari baseline stabil dan diajukan melalui Pull Request ke `staging`.
