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
main (branch produksi terlindungi + Immutable GHCR Image)
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
