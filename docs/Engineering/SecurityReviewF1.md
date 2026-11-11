# Review keamanan terarah F1 (PRD v3 N11)

Tanggal: 2026-11-12. Cakupan sesuai PRD v2 F1: callback dan deeplink pembayaran, rate limit initiate payment per sesi tamu, data pribadi di URL, dan flag cookie tamu. Ditambah sapuan cepat jalur berisiko tinggi di kedua repo.

## Ringkasan

Satu temuan tinggi diperbaiki di rilis ini: initiate payment tamu tidak dibatasi per sesi, padahal tiap percobaan membuka charge baru di Midtrans. Tiga temuan high dari `pnpm audit` (postcss di dalam Next) ikut ditutup di repo client. Temuan sedang dan rendah masuk ledger `MyCaffeClient/docs/TechDebt.md`.

## Hasil per butir F1

| Butir | Temuan | Status |
|---|---|---|
| Callback URL | Dibangun server dari `APP_PUBLIC_URL` dan `qr_token` meja tamu sendiri; `callback_url` di body diabaikan (`guest_handler.go`) | Aman |
| Deeplink GoPay dan ShopeePay | Diteruskan dari respons Midtrans; web menolak skema `javascript:`, `data:`, `file:`, `vbscript:`, `blob:` (`safeDeeplink`). Allowlist domain sengaja tidak dipakai karena Midtrans menyarankan tidak | Aman |
| Rate limit initiate payment per sesi tamu | **Tidak ada.** Pesanan pay-first menjadikan percobaan lama `expired` lalu membuat charge baru, tanpa batas jumlah | **Diperbaiki**: 20 percobaan per sesi tamu per 10 menit, 429 `TOO_MANY_REQUESTS`, unit test `TestInitiatePaymentLimitedPerGuestSession` |
| Data pribadi di URL | API dan web tidak menaruh email, telepon, atau nama di query atau path. Parameter `to` di laporan adalah tanggal | Aman |
| Token bearer di path | Token struk, pelacakan, dan bagi tagihan ada di path, jadi ikut tercatat di access log nginx (`$request`) | Sedang, masuk ledger (S1) |
| Cookie tamu | `__Secure-mc_guest`, `Secure`, `HttpOnly`, `SameSite=Strict`, path `/api/v1/public`, umur mengikuti sesi 3 jam. Mutasi butuh `X-CSRF-Protection: 1` | Aman |

## Sapuan tambahan

| Area | Temuan | Status |
|---|---|---|
| Webhook Midtrans | Signature SHA-512 dicek dengan `subtle.ConstantTimeCompare`; webhook per tenant memakai kunci tenant | Aman |
| Dependensi Go | `govulncheck` bersih setelah Go 1.27.4 (GO-2026-6859) | Aman |
| Dependensi web | `pnpm audit --prod`: 2 high, 2 moderate di `postcss@8.4.31` yang dipasang Next | **Diperbaiki** di client: override `next>postcss` ke `^8.5.32`, audit bersih |
| XSS sink web | Tidak ada `dangerouslySetInnerHTML`, `innerHTML`, atau `eval` di `apps/web/src` | Aman |
| Rahasia di repo | Hanya `.env.example` yang terlacak di client; backend memakai Gitleaks di CI. Client belum punya pemindai rahasia di CI | Rendah, masuk ledger (S2) |
| Header | CSP, HSTS, `X-Content-Type-Options`, `Referrer-Policy` di nginx prod; halaman struk `no-referrer` dan `noindex` | Aman |

## Di luar kode

Kredensial yang pernah tertempel di chat (server key Midtrans produksi, app password Gmail, token R2, DSN Sentry) wajib dirotasi oleh Director sebelum gerai sungguhan memakai sistem ini.
