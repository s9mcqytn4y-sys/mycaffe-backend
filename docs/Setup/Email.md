# Transactional Email & Domain Routing (`abbdullaziz.online`)

MyCaffe sends transactional email (staff PIN setup/reset links and notifications) through the transactional `email_outbox` table and a background worker in [`internal/service/email_service.go`](file:///c:/Projects/myCaffe-backend/internal/service/email_service.go) (owner decision **E5**).

HTTP handlers never block on an external SMTP server: they enqueue a row inside `database.WithTenantTx` and commit immediately. The worker polls every 10 seconds using the narrowly-scoped `SECURITY DEFINER` function `list_stores_with_due_email()` and retries up to 5 times with exponential backoff (`30s`, `2m`, `10m`, `1h`).

## Architecture by Environment

| Environment | `EMAIL_DRIVER` | Outbound SMTP Relay | Inbound Domain Routing (`*@abbdullaziz.online`) | Cost |
| :--- | :--- | :--- | :--- | :--- |
| **Development (`APP_ENV=development`)** | `smtp` (or `log`) | `smtp.gmail.com:587` (`STARTTLS` with Google App Password) | Cloudflare Email Routing forwards `noreply@`, `owner@`, `support@`, and catch-all `*@abbdullaziz.online` to the owner's Gmail inbox | **Rp 0 (Free)** |
| **Demo / Staging (`APP_ENV=staging`)** | `smtp` | `smtp.gmail.com:587` (`STARTTLS` with Google App Password) | Cloudflare Email Routing (`abbdullaziz.online` MX/SPF records managed on Cloudflare Free Plan) | **Rp 0 (Free)** |
| **Production (`APP_ENV=production`)** | `smtp` (`log` & `disabled` fail closed at startup) | `smtp.hostinger.com:465` (Implicit TLS) or transactional SMTP provider | Hostinger Email / Cloudflare Email Routing + SPF/DKIM/DMARC | Included with domain/VPS |

## Cloudflare Email Routing Setup (`abbdullaziz.online`)

1. **MX & SPF Records**: Enabled on Cloudflare Zone `abbdullaziz.online` (`c4d1c304cb402f372016fb554df434c2`). Ensure any legacy registrar MX records (`mx1.hostinger.com`, `mx2.hostinger.com`) are removed if Cloudflare reports `misconfigured/locked`.
2. **Destination Verification**: Click the confirmation link in the email from Cloudflare (`"Verify your email address for Cloudflare Email Routing"`) sent to your Gmail inbox.
3. **Custom Addresses**: Once verified, run `./scripts/cloudflare-setup.ps1` (or add in Cloudflare Dashboard -> **Email** -> **Email Routing**) to route `noreply@abbdullaziz.online`, `owner@abbdullaziz.online`, and `support@abbdullaziz.online` to your verified Gmail address.

## Environment Variables (`.env` / `.env.demo`)

```dotenv
EMAIL_DRIVER=smtp
EMAIL_FROM_ADDRESS=noreply@abbdullaziz.online
EMAIL_FROM_NAME=MyCaffe POS
SMTP_HOST=smtp.gmail.com
SMTP_PORT=587
SMTP_USERNAME=your-account@gmail.com
SMTP_PASSWORD=your-16-char-google-app-password
APP_PUBLIC_URL=https://demo.abbdullaziz.online
```
