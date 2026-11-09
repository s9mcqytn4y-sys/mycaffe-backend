# Peta Distribusi Folder Workspace & Ekosistem Produk AzureDevID

Dokumen ini menjadi acuan tunggal bagi seluruh AI Agent (Antigravity, Claude Code, Codex) dan Engineer manusia agar tidak terjadi tabrakan perubahan antar-direktori maupun antar-worktree.

## 1. Direktori Aktif Ekosistem MyCaffe (Produk Pertama AzureDevID)

| Path Absolut | Peran & Cakupan | Status | Aturan Agent & Docker |
| :--- | :--- | :--- | :--- |
| `C:\Projects\myCaffe-backend` | Repository backend Go, migrasi PostgreSQL 18 RLS, Midtrans Core API, bridge WhatsApp (`cmd/wa-bridge`), print agent, dan Docker Compose (`deploy/docker/`). Rencana nama: `CassyBackend` (AL10). | Aktif (`main`) | Kerjakan di branch pendek. Worktree opsional untuk kerja paralel. |
| `C:\Projects\CassyWorktree` | Worktree repo backend pada branch `feat/cassy-rebrand` yang sudah di-merge. Bukan repo terpisah. | Akan dilepas (AL10) | Jangan dianggap sumber bridge WhatsApp. |
| `C:\Projects\MyCaffeClient` | Unified Frontend & Native Monorepo (`com.azuredevid.cassy`): Next.js 15 Responsive/Tablet/iPad PWA (`apps/web`), Android Native Jetpack Compose (`apps/android`), iOS Hardware Bridge (`apps/ios`), dan Shared TS SDK (`packages/shared`). | Aktif (`main`) | Memiliki `.vscode/` dan `Dockerfile` multi-stage tersendiri untuk Next.js standalone container. |
| `WSL Ubuntu-24.04: /opt/mycaffe` | Live Demo Stack (`https://demo.abbdullaziz.online`) di balik Cloudflare Named Tunnel (`mycaffe-demo`), menjalankan `mycaffe-postgres` (PG 18), `mycaffe-api`, `mycaffe-nginx`, `mycaffe-wa-bridge`, dan Next.js PWA. | Live Demo Runtime | Digunakan untuk verifikasi aktual E2E sebelum klien membeli VPS produksi sendiri. |

## 2. Brankas Dokumen Bisnis, Legalitas, & Aset Brand Resmi

| Path Absolut | Peran & Keamanan | Konvensi Penamaan |
| :--- | :--- | :--- |
| `C:\Users\Acer\OneDrive\Documents\AzureDevId` | Brankas tunggal dokumen bisnis resmi **AzureDevID** (`BrandAssetsLogo`, `LegalAndBrandGuidelines`, `LegalitasPerizinan`, `MerchantPaymentGateway`, `ProductSpecCompetitor`, `SecurityRecovery`, `Metadata.json`). | Pure `PascalCase` ketat (tanpa awalan angka `01..06`, tanpa `_` atau `-`). `LegalitasPerizinan` & `SecurityRecovery` dikunci dengan NTFS ACL khusus akun pemilik Windows. |
| `C:\Users\Acer\Downloads` | Folder unduhan yang telah dikelompokkan rapi (`InstallersAndDrivers`, `CareerAndResume`, `MedicalCheckup`, `DatabaseAndReports`, `ProjectArchivesAndDesign`, `GameDevAndThreeD`). | Pure `PascalCase` tanpa karakter `_` atau `-`. |

## 3. Proyek Lain di `C:\Projects` (Non-MyCaffe, Pure `PascalCase`)

Seluruh proyek non-aktif di `C:\Projects` telah distandardisasi ke `PascalCase` murni disertai konfigurasi `.vscode/`:
- `ArisparisSuratDigital`, `BlenderMcp`, `CryptoBanningTools`, `Hris`, `McuApp`, `MerchandiseStore`, `MerchandiseStoreNext`, `PeepsFrontend`, `RobloxGame`, `StarterLaravel`, `VoidSupply`, `ZeeOs`.
- Skrip `C:\Projects\RenameMyCaffeWorkspace.ps1` sudah tidak sesuai: masih menargetkan `MyCaffeBackend` dan worktree yang sudah tidak ada. Jangan dijalankan sebelum diperbarui mengikuti AL10.
