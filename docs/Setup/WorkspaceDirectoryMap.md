# Peta Distribusi Folder Workspace & Ekosistem Produk AzureDevID

Dokumen ini menjadi acuan tunggal bagi seluruh AI Agent (Antigravity, Claude Code, Codex) dan Engineer manusia agar tidak terjadi tabrakan perubahan antar-direktori maupun antar-worktree.

## 1. Direktori Aktif Ekosistem MyCaffe (Produk Pertama AzureDevID)

| Path Absolut | Peran & Cakupan | Status | Aturan Agent & Docker |
| :--- | :--- | :--- | :--- |
| `C:\Projects\myCaffe-backend` | Repository utama Go 1.26 Backend API, PostgreSQL 18 RLS Migrations, Mailer, Midtrans Core API, & Docker Compose (`deploy/docker/`). | Aktif (`staging` / `main`) | Jangan ubah file secara langsung saat worktree fitur sedang aktif; gunakan untuk sinkronisasi branch rilis & `.env` lokal. |
| `C:\Projects\mycaffe-wt-platform` | Git worktree terisolasi untuk stabilisasi SaaS Governance, Strict Onboarding + Email OTP, MikroTik `.rsc` Automation, & Webhook Lane 1/Lane 2. | Aktif (`feat/saas-stabilization-e2e`) | Terhubung ke repository Git yang sama dengan `myCaffe-backend`. Semua PR stabilisasi SaaS dikirim dari worktree ini ke `staging`. |
| `C:\Projects\mycaffe-wt-sc` | Git worktree agen paralel lain (`feat/v011-stamp-card`). | Aktif Paralel (JANGAN DISENTUH) | **DILARANG** memodifikasi file di dalam folder ini atau menyentuh `pos_handler.go`, `order_create.go`, `order_quote.go`, `payment_settlement.go`, dan `pricing.go`. |
| `C:\Projects\MyCaffeClient` | Unified Frontend & Native Monorepo (`online.abbdullaziz.mycaffe`): Next.js 15 Responsive/Tablet/iPad PWA (`apps/web`), Android Native Jetpack Compose (`apps/android`), iOS Hardware Bridge (`apps/ios`), dan Shared TS SDK (`packages/shared`). | Aktif (`feat/product-one-stabilization`) | Memiliki `.vscode/` dan `Dockerfile` multi-stage tersendiri untuk Next.js standalone container. |
| `WSL Ubuntu-24.04: /opt/mycaffe` | Live Demo Stack (`https://demo.abbdullaziz.online`) di balik Cloudflare Named Tunnel (`mycaffe-demo`), menjalankan `mycaffe-postgres` (PG 18), `mycaffe-api`, `mycaffe-nginx`, `mycaffe-wa-bridge`, dan Next.js PWA. | Live Demo Runtime | Digunakan untuk verifikasi aktual E2E sebelum klien membeli VPS produksi sendiri. |

## 2. Brankas Dokumen Bisnis, Legalitas, & Aset Brand Resmi

| Path Absolut | Peran & Keamanan | Konvensi Penamaan |
| :--- | :--- | :--- |
| `C:\Users\Acer\OneDrive\Documents\AzureDevId` | Brankas tunggal dokumen bisnis resmi **AzureDevID** (`BrandAssetsLogo`, `LegalAndBrandGuidelines`, `LegalitasPerizinan`, `MerchantPaymentGateway`, `ProductSpecCompetitor`, `SecurityRecovery`, `Metadata.json`). | Pure `PascalCase` ketat (tanpa awalan angka `01..06`, tanpa `_` atau `-`). `LegalitasPerizinan` & `SecurityRecovery` dikunci dengan NTFS ACL khusus akun pemilik Windows. |
| `C:\Users\Acer\Downloads` | Folder unduhan yang telah dikelompokkan rapi (`InstallersAndDrivers`, `CareerAndResume`, `MedicalCheckup`, `DatabaseAndReports`, `ProjectArchivesAndDesign`, `GameDevAndThreeD`). | Pure `PascalCase` tanpa karakter `_` atau `-`. |

## 3. Proyek Lain di `C:\Projects` (Non-MyCaffe, Pure `PascalCase`)

Seluruh proyek non-aktif di `C:\Projects` telah distandardisasi ke `PascalCase` murni disertai konfigurasi `.vscode/`:
- `ArisparisSuratDigital`, `BlenderMcp`, `CryptoBanningTools`, `Hris`, `McuApp`, `MerchandiseStore`, `MerchandiseStoreNext`, `PeepsFrontend`, `RobloxGame`, `StarterLaravel`, `VoidSupply`, `ZeeOs`.
- Skrip otomasi `C:\Projects\RenameMyCaffeWorkspace.ps1` disediakan untuk mengganti nama `myCaffe-backend` menjadi `MyCaffeBackend` secara aman setelah seluruh worktree paralel selesai di-merge.
