# Cassy Backend: Status (`memory/STATUS.md`)

**Last Updated**: 2026-11-09
**Branch dasar**: `main` (tidak ada `staging`)
**Rilis terakhir**: v0.12.1 (`e25e648`), image GHCR tag `0.12.1`, `0.12`, `latest`
**Keputusan aktif**: [ReAlignment2026-11.md](../docs/Product/ReAlignment2026-11.md), AL1 sampai AL11 di [DecisionRegister](../docs/Architecture/DecisionRegister.md)

| Area | Status | Catatan |
| :--- | :--- | :--- |
| PostgreSQL 18 + RLS | Implemented & Tested | 56 migrasi, demo sudah di 000056 sejak 2026-11-09 |
| Midtrans Core API, BYOK per tenant | Implemented & Tested di sandbox | Akun AzureDevID masih review, koreksi Website dan Foto Selfie belum dikirim |
| Stok, kanal ojol manual, stamp card, invoice B2B, pengingat | Implemented & Tested | Client belum punya layarnya |
| Batas paket langganan | Planned | Kolom ada, penegakan belum terlihat. Wajib sebelum pilot berbayar |
| Website publik | Ditunda (AL2) | `abbdullaziz.online` masih parkir. Koreksi "Website" Midtrans tertahan sampai halaman ini hidup |
| Email produksi | Planned | Pindah ke SES (AL6). Saat ini SMTP Gmail |
| WhatsApp | Guarded / Fail-Closed | `whatsmeow` ditolak di produksi. Template `tagihan_invoice` butuh persetujuan Meta |
| Demo | Sebagian | API v0.12.1 sehat dan lolos smoke test login serta endpoint baca. Web client tayang dari `main` client (`1dc89b9`), termasuk perpanjangan token staf. Menu dan quote tamu lolos uji browser, pembayaran dan login browser belum |
| Klikit | Planned | Sandbox gratis, tunggu toko yang membayar add-on |
