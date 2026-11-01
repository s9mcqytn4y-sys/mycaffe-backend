# Cloudflare R2 Media & Backup Storage Setup

Dokumentasi ini menjelaskan konfigurasi, arsitektur, dan prosedur operasional untuk Cloudflare R2 Object Storage pada MyCaffe Core POS.

---

## 1. Arsitektur Triple-Bucket

Sesuai evaluasi keamanan K9 dan persetujuan arsitektur, MyCaffe menggunakan pemisahan 3 bucket terisolasi:

```mermaid
graph TD
    subgraph Cloudflare Account [Cloudflare R2 Account]
        PublicBucket["mycaffe-media<br/>(Public CDN / dev-url)<br/>Foto Menu, Kategori, Logo"]
        PrivateBucket["mycaffe-private-media<br/>(Private Presigned GET 15m)<br/>QRIS Statis Toko, Dokumen Sensitif"]
        BackupBucket["mycaffe-backups<br/>(Strictly Private)<br/>GPG AES256 Encrypted PostgreSQL Dumps"]
    end

    Browser["Browser / Customer / POS UI"] -->|Public Direct Read| PublicBucket
    POSStaff["Staff POS Terminal"] -->|Presigned Short-Lived URL| PrivateBucket
    BackendAPI["MyCaffe Go API Backend"] -->|S3 API Presigned PUT / Upload| PublicBucket
    BackendAPI -->|Generate Presigned GET 15m| PrivateBucket
    BackupCron["VPS Backup Script / Cron"] -->|AWS CLI S3 sync / put| BackupBucket
```

| Nama Bucket | Sifat Akses | Penggunaan | Public Domain / URL |
| :--- | :--- | :--- | :--- |
| `mycaffe-media` | **Public** (Managed Public Access / dev-url) | Gambar menu, foto kategori, logo kafe | `https://pub-f8731699e90848268b2a7f8219e69cc5.r2.dev` (atau custom CDN domain) |
| `mycaffe-private-media` | **Private** (Signed URL Only, TTL 15m) | Gambar QRIS statis toko (K9), bukti transfer internal | Tidak ada akses publik. Direct presigned GET via API |
| `mycaffe-backups` | **Private** (Encrypted S3 Object) | Backup database harian `pg_dump` GPG AES256 | Tidak ada akses publik sama sekali |

---

## 2. Persyaratan & Kredensial

Perangkat yang dibutuhkan:
1. `wrangler` CLI (>= v3.0 / v4.x terpasang via `npm install -g wrangler` atau `npx wrangler`)
2. `aws` CLI (v2) untuk eksekusi backup/restore via S3-compatible API
3. Akun Cloudflare aktif dengan R2 diaktifkan.

### Variabel Lingkungan (`.env`)

```ini
STORAGE_DRIVER=r2
R2_ACCOUNT_ID=3261ead881772371ef739b742a36c1e9
R2_ACCESS_KEY_ID=<your-r2-access-key-id>
R2_SECRET_ACCESS_KEY=<your-r2-secret-access-key>
R2_BUCKET_NAME=mycaffe-media
R2_PRIVATE_BUCKET_NAME=mycaffe-private-media
R2_PUBLIC_URL=https://pub-f8731699e90848268b2a7f8219e69cc5.r2.dev

# Backup & Restore Scripts
R2_BACKUP_BUCKET=mycaffe-backups
R2_BACKUP_PREFIX=backups
```

---

## 3. Otomasi Provisioning & Verifikasi

Skrip PowerShell telah disediakan di folder `scripts/cloudflare/`:

### A. Setup & Konfigurasi Bucket (`setup-r2.ps1`)
Membuat ketiga bucket jika belum ada, mengaktifkan public dev-url pada `mycaffe-media`, dan menerapkan aturan CORS browser:

```powershell
powershell -File scripts/cloudflare/setup-r2.ps1
```

### B. Verifikasi Konfigurasi (`verify-r2.ps1`)
Memeriksa status bucket, public access, dan CORS rule:

```powershell
powershell -File scripts/cloudflare/verify-r2.ps1
```

---

## 4. Konfigurasi CORS `mycaffe-media`

Untuk mengizinkan upload langsung (presigned PUT) dan pengambilan gambar dari frontend web:

File konfigurasi: `config/cloudflare/r2-media-cors.json`
```json
{
  "rules": [
    {
      "allowed": {
        "origins": [
          "http://localhost:3000",
          "http://127.0.0.1:3000",
          "http://localhost:8088",
          "https://*.trycloudflare.com"
        ],
        "methods": [
          "GET",
          "HEAD",
          "PUT"
        ],
        "headers": [
          "Content-Type",
          "Content-MD5",
          "Cache-Control"
        ]
      },
      "exposeHeaders": [
        "ETag"
      ],
      "maxAgeSeconds": 3600
    }
  ]
}
```

Penerapan manual via CLI:
```bash
npx wrangler r2 bucket cors set mycaffe-media --file config/cloudflare/r2-media-cors.json
```

---

## 5. Prosedur Backup & Restore PostgreSQL ke R2

### Keamanan Backup
- Database di-dump menggunakan `pg_dump` format biner.
- Dienkripsi secara lokal menggunakan GPG symmetric cipher AES-256 (`gpg --symmetric --cipher-algo AES256`).
- Dihitung manifest SHA-256 menggunakan nama file relatif (bebas dependensi path lokal).
- Di-upload ke `s3://mycaffe-backups/backups/`.

### Menjalankan Backup
```bash
bash scripts/backup/backup_to_r2.sh
```

### Menjalankan Restore (Drill Recovery)
> [!CAUTION]
> Skrip `restore_from_r2.sh` memiliki **destructive safety guard**: secara default memblokir restore ke database non-disposable kecuali nama database mengandung `test`, `temp`, `drill`, atau `recovery`, atau jika `CONFIRM_DESTRUCTIVE_RESTORE=true` diberikan.
> Selalu uji pemulihan pada database sementara (misal: `mycaffe_recovery_drill`).

Contoh eksekusi restore drill ke database sekali pakai:

```bash
export DB_HOST="127.0.0.1"
export DB_PORT="5432"
export DB_NAME="mycaffe_recovery_drill"
export DB_MIGRATOR_USER="mycaffe_migrator"
export DB_MIGRATOR_PASSWORD="<migrator_password>"
export BACKUP_ENCRYPTION_PASSPHRASE="<backup_gpg_passphrase>"
export R2_ENDPOINT="https://3261ead881772371ef739b742a36c1e9.r2.cloudflarestorage.com"
export R2_BACKUP_BUCKET="mycaffe-backups"
export AWS_ACCESS_KEY_ID="<r2_backup_access_key>"
export AWS_SECRET_ACCESS_KEY="<r2_backup_secret_key>"
export BACKUP_OBJECT="backup_mycaffe_prod_20261101_000000Z.dump.gpg"

bash scripts/backup/restore_from_r2.sh
```

---

## 6. Matriks Status Implementasi Storage

| Komponen Storage | Status | Keterangan |
| :--- | :--- | :--- |
| **Triple-Bucket Provisioning** | **Implemented & Verified** | 3 bucket (`mycaffe-media`, `mycaffe-private-media`, `mycaffe-backups`) aktif di Cloudflare R2. |
| **Browser CORS Policy** | **Implemented & Verified** | Rules diterapkan pada `mycaffe-media` via `config/cloudflare/r2-media-cors.json`. |
| **GPG Encrypted DB Backup** | **Implemented & Tested** | `scripts/backup/backup_to_r2.sh` menghasilkan dump biner terenkripsi AES-256 + manifest sha256 relatif. |
| **Non-Destructive DB Restore** | **Implemented & Tested** | `scripts/backup/restore_from_r2.sh` memverifikasi checksum sha256 dan memproteksi DB operasional. |
| **Presigned URL & Media Upload (C4)** | **Planned (Iteration 5)** | Endpoint upload presigned PUT, validasi magic bytes, stripping EXIF/GPS, dan resize WebP. |
