# Git Workflow and Release Policy

Keputusan AL11 (2026-11-09): trunk-based. Berlaku untuk `mycaffe-backend` dan `mycaffe-client`. Branch `staging` sudah tidak ada dan tidak dihidupkan lagi.

## 1. Alur

```text
feat/* fix/* docs/* chore/*  (branch pendek dari main, 1 sampai 2 hari)
    │  Pull Request, CI hijau, persetujuan Owner
    ▼
main  ──▶ demo (https://demo.abbdullaziz.online)
    │  tag vX.Y.Z, persetujuan Owner
    ▼
image GHCR immutable ──▶ produksi (scripts/deploy/deploy.sh <digest>)
```

Status: deploy `main` ke demo masih manual. Demo berjalan di workstation Owner, jadi otomasinya perlu runner atau skrip lokal dan belum dibangun.

`APP_ENV=staging` tetap dipakai sebagai konfigurasi environment demo. Ini bukan branch.

## 2. Aturan

1. Tidak ada push langsung ke `main`. Setiap perubahan lewat Pull Request.
2. Merge ke `main` dan pembuatan tag `v*` butuh persetujuan eksplisit Owner di chat.
3. Dilarang `git push --force` ke `main` dan dilarang menghapus `main`.
4. Satu PR, satu tujuan. Migrasi selalu aditif, file migrasi yang sudah di-merge tidak boleh diubah.
5. Perubahan lokal yang belum di-commit disimpan dengan `git stash` dan dilaporkan ke Owner, bukan dibuang.

Repo privat paket gratis tidak punya branch protection (API 403), jadi aturan ini mengandalkan disiplin. Periksa berkala: `gh pr list --state merged --base main` harus cocok dengan persetujuan yang tercatat.

## 3. Quality gates

Setiap PR wajib lolos job CI: lint dan vet, govulncheck, Docker build, migrasi PostgreSQL 18 triple-role, seed idempotency, dan integration test `-race`. Integration test hanya benar-benar jalan di job PostgreSQL.

## 4. Versi

- SemVer per repo. API tetap `v0.x` sampai `v1.0.0`, yaitu saat tenant berbayar pertama sudah berjalan 30 hari.
- Client mengikuti kontrak `docs/Api/OpenApi.yaml`.
- Tag dengan `-` (misal `v0.13.0-rc.1`) menjadi prerelease.

## 5. Merilis

```bash
git switch main && git pull --ff-only origin main
git tag -a vX.Y.Z -m "Release vX.Y.Z: <ringkasan>"
git push origin vX.Y.Z
```

Push tag memicu **Release - Publish**: GitHub Release berisi digest image, perintah deploy, SBOM, dan provenance SLSA. Image dengan CVE CRITICAL yang punya perbaikan ditolak CI (Trivy).

Rollback: deploy digest image sebelumnya. Jangan mengedit file di server.
