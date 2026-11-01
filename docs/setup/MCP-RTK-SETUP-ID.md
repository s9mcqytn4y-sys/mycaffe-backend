# Setup Google Antigravity, MCP, dan RTK (Windows)

Panduan ini mengatur lingkungan kerja lokal Google Antigravity (`agy`), RTK, dan MCP untuk repositori `mycaffe-backend`.

## A. Preset Keamanan Antigravity

1. Buka pengaturan proyek di Antigravity dan gunakan preset **Review-Required / Default** selama pengembangan sistem POS dan pembayaran.
2. Wajibkan konfirmasi eksplisit untuk operasi mutasi database di luar lingkungan uji, penambahan izin MCP baru, rotasi kredensial, dan eksekusi deployment produksi.
3. Jangan pernah menyalin isi `.env` atau `.env.local` ke ruang obrolan, aturan agen, maupun dokumentasi.

## B. Struktur Konfigurasi Agen di Repositori

Simpan seluruh artefak panduan agen berikut di dalam kontrol versi Git (tanpa kredensial rahasia):

- `AGENTS.md` dan `GEMINI.md`: instruksi utama proyek yang selalu aktif.
- `.agents/rules/`: aturan terkondisi untuk bahasa Go, isolasi multi-tenant, keamanan pembayaran QRIS/Midtrans, migrasi database, dan rilis.
- `.agents/skills/`: alur kerja terstruktur untuk pengiriman fitur, migrasi database, integrasi Midtrans QRIS, tinjauan keamanan, dan kesiapan rilis.
- `docs/product/` dan `docs/architecture/`: dokumen spesifikasi produk, identitas merek (`brand-and-product-knowledge.md`), serta catatan keputusan arsitektur (`ADR-002`).

## C. Instalasi dan Verifikasi RTK (Rust Token Killer)

Gunakan paket resmi `rtk-ai/rtk` untuk mengompresi output perintah terminal agar efisien di dalam jendela konteks agen.

Di PowerShell:

```powershell
winget install rtk-ai.rtk
```

Tutup dan buka kembali terminal, lalu verifikasi:

```powershell
rtk --version
rtk gain
```

Pratinjau integrasi Antigravity untuk workspace ini:

```powershell
rtk init --agent antigravity --dry-run
```

Jika pratinjau sudah sesuai, pasang untuk workspace ini:

```powershell
rtk init --agent antigravity
```

Apabila output sebuah perintah terpotong atau tidak terbaca oleh hook RTK, jalankan ulang perintah tersebut dengan awalan `rtk bypass <perintah>`.

## D. Penggunaan MCP yang Terbatas dan Aman

Contoh konfigurasi MCP tersedia pada `.agents/mcp_config.example.json`. Salin ke `.agents/mcp_config.json` hanya untuk layanan yang benar-benar dibutuhkan di lingkungan lokal:

- **PostgreSQL 18 Lokal (Docker):** Gunakan database lokal (`localhost:5432`) dengan data seed pengembangan (`db/seeds/development.sql`). Jangan pernah menghubungkan MCP ke database produksi.
- **Playwright MCP:** Digunakan untuk pengujian browser dan verifikasi tampilan lokal/staging tanpa menggunakan data kartu atau pembayaran uang asli.
- **Context7 MCP:** Digunakan untuk pencarian dokumentasi pustaka resmi.
- **Nonaktifkan MCP yang tidak relevan:** Biarkan server MCP yang tidak digunakan oleh proyek ini (seperti layanan database cloud pihak ketiga atau alat desain 3D) dalam kondisi nonaktif agar ruang konteks tetap bersih.

## E. Pemeriksaan Setelah Setup

1. Mulai ulang Antigravity (`agy`).
2. Pastikan direktori kerja aktif berada di `c:\Projects\myCaffe-backend`.
3. Pastikan `git status` bersih dari file `.env`, `.env.local`, maupun token kredensial.
4. Gunakan Docker Compose lokal (`deploy/docker/docker-compose.yml`) untuk menjalankan PostgreSQL 18 saat menguji migrasi dan integrasi layanan.
