# Git Workflow and Release Policy

Dokumen ini mendefinisikan protokol branching, promosi rilis, dan integritas version control di repositori `mycaffe-backend`.

---

## 1. Branching Model

```text
feature/* (fitur / bugfix baru)
    │
    ▼ (Pull Request + 3 Quality Gates CI + Squash Merge)
staging (branch integrasi pra-rilis)
    │
    ▼ (Pull Request Promosi + PERSETUJUAN EKSPLISIT OWNER + Merge Commit --no-ff)
main (branch produksi + Immutable GHCR Image; tanpa proteksi teknis, lihat §3)
```

1. **`feature/*` $\to$ `staging`**:
   - Dibuat dari `staging` terbaru.
   - Menggunakan format Pull Request.
   - Wajib lolos seluruh quality gates (Lint, Vet, Govulncheck, Docker build, PostgreSQL 18 triple-role migration, seed idempotency, integration test `-race`).
   - Di-merge menggunakan strategi **Squash and Merge** (`gh pr merge <PR> --squash --delete-branch`).

2. **`staging` $\to$ `main` (Promosi Rilis)**:
   - **WAJIB MERGE COMMIT (`--merge` / `--no-ff`)**: DILARANG KERAS menggunakan *Squash and Merge* saat mempromosikan `staging` ke `main`. Squash akan memutuskan tree commit dan membuat histori `git log origin/main..staging` terus menampilkan commit lama.
   - **WAJIB PERSETUJUAN EKSPLISIT**: Agent dilarang keras melakukan merge ke `main` tanpa instruksi/persetujuan eksplisit dari Owner di chat.
   - Setiap merge ke `main` otomatis memicu publikasi image kontainer Docker bertanda versi ke GitHub Container Registry (`ghcr.io`).
   - Diberikan Git Tag semver (misal: `v0.4.0`) dan GitHub Release resmi.

3. **Integritas Working Tree & Stash**:
   - Jika terdapat uncommitted / modified files di lokal, **WAJIB menggunakan `git stash`**, BUKAN `git checkout -- .` atau menghapus file tanpa konfirmasi.
   - Setiap file yang di-stash harus dilaporkan secara transparan kepada Owner.

---

## 2. Checklist Promosi Staging ke Main

1. Pastikan seluruh PR fitur telah berstatus MERGED di `staging`.
2. Pastikan CI di `staging` hijau (3 job lolos).
3. Buat PR promosi dari `staging` ke `main`:

   ```bash
   gh pr create --base main --head staging --title "release: vX.Y.Z - <Milestone Name>" --body "<Changelog>"
   ```

4. Tunggu seluruh checks di PR hijau.
5. Laporkan ke Owner dan tunggu persetujuan eksplisit.
6. Setelah disetujui, merge menggunakan **merge commit**:

   ```bash
   gh pr merge <PR> --merge
   ```

7. Buat tag semver pada `main`:

   ```bash
   git checkout main && git pull origin main
   git tag -a vX.Y.Z -m "Release vX.Y.Z: <Summary>"
   git push origin vX.Y.Z
   ```

8. Push tag memicu **Release - Publish** yang membuat (atau melengkapi) GitHub Release untuk tag itu: digest image, perintah `scripts/deploy/deploy.sh <digest>`, serta lampiran SBOM (`*.spdx.json`) dan provenance SLSA (`*.provenance.json`). Tag dengan `-` (misal `v0.6.0-rc.1`) menjadi prerelease. Image yang punya CVE CRITICAL dengan fix sudah ditolak di CI (Trivy) sebelum sampai ke sini (P0-06).

---

## 3. Tanpa Branch Protection Teknis (keputusan IN2, 2026-11-02)

Repo privat ini memakai paket GitHub gratis, sehingga ruleset/branch protection tidak tersedia (API 403). Owner memilih disiplin manual. Aturan yang berlaku:

1. Tidak ada push langsung ke `staging` atau `main`; setiap perubahan lewat Pull Request.
2. Tidak ada merge (ke `staging` maupun `main`) sebelum 3 job CI lulus **dan** Owner menyetujui secara eksplisit di chat.
3. Dilarang `git push --force` ke `staging`/`main` dan dilarang menghapus kedua branch itu.
4. Setiap promosi ke `main` dicek ulang sesudahnya: `gh pr list --state merged --base main` harus cocok dengan persetujuan yang tercatat.

Risiko yang diterima: merge tanpa persetujuan (seperti PR #23) tetap mungkin secara teknis; aturan di atas hanya mendeteksi, tidak mencegah.
