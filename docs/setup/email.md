# Email: Setup, Queue & Templates

Owner decisions E1–E5 (see [decision-register.md](../architecture/decision-register.md)). This phase sends
**transactional and notification email only**; marketing consent is collected (planned) but no campaigns
are sent through the Hostinger mailbox.

## 1. How it works

```text
business transaction (WithTenantTx)
   ├─ change rows (user, token, ...)
   └─ INSERT email_outbox (rendered subject + HTML + text)     ← same commit, so no email for rolled-back work
email worker (goroutine in the API, every 10 s)
   ├─ list_stores_with_due_email()                             ← SECURITY DEFINER, returns store UUIDs only
   ├─ per store under RLS: claim batch FOR UPDATE SKIP LOCKED, status=sending, lease 10 min
   ├─ SMTP send outside the transaction
   └─ sent → bodies scrubbed | transient error → retry | 5xx / permanent → failed (bodies scrubbed)
```

- Retry waits: 1 min, 5 min, 30 min, 2 h, 8 h → 6 attempts over ~10.5 h, then `failed`.
- A crashed worker's lease expires after 10 minutes and the row is picked up again.
- Bodies of activation/reset emails contain link secrets, so they are replaced after delivery.
- Links put the token in the URL **fragment** (`/staff/pin#token=...`): browsers never send fragments to
  servers, so the secret does not appear in Nginx or Cloudflare logs. The frontend reads it and calls
  `POST /api/v1/auth/staff/pin/setup`.

## 2. Templates

`internal/pkg/mailer/templates/` — each email has an HTML version (table layout, inline styles, Espresso &
Crema palette, 560 px, preheader, button + plain link fallback) and a plain-text version. HTML is rendered
with `html/template` (auto-escaped); headers are stripped of CR/LF.

| Template | Sent to | Trigger |
| ---------- | --------- | --------- |
| `staff_invite` | new staff | owner invites (`POST /api/v1/staff`); link valid 48 h |
| `pin_reset` | staff | "lupa PIN" or owner `POST /api/v1/staff/{id}/pin-reset`; link valid 30 min |
| `pin_changed` | staff | PIN changed via reset link or `pin/change` (security notice) |
| `account_locked` | every active owner | a staff account locks after 5 wrong PINs |

Planned templates (backlog): digital receipt, refund confirmation, shift close summary, duplicate-payment
alert, daily owner summary.

## 3. Environment configuration

| Variable | Dev (`go run`) | Demo (Quick Tunnel) | Production (Hostinger) |
| ---------- | ---------------- | --------------------- | ------------------------ |
| `EMAIL_DRIVER` | `log` (default) | `log`, or `smtp` via `.env.demo` | `smtp` (enforced) |
| `SMTP_HOST` / `SMTP_PORT` | — | `smtp.gmail.com` / `587` | `smtp.hostinger.com` / `465` |
| `SMTP_TLS_MODE` | — | `starttls` (auto for 587) | `implicit` (auto for 465) |
| `SMTP_USERNAME` / `SMTP_PASSWORD` | — | Gmail address / **App Password** | mailbox address / mailbox password |
| `EMAIL_FROM_ADDRESS` | — | same Gmail address | e.g. `noreply@<domain>` (the mailbox) |
| `EMAIL_FROM_NAME` | `MyCaffe` | `MyCaffe Demo` | e.g. `MyCaffe` |
| `APP_PUBLIC_URL` | `http://localhost:3000` | the tunnel URL | `https://pos.<domain>` |

- The `log` driver writes the plain-text email (including links) to the API log. It is rejected in
  production at startup. With the demo stack: `docker compose -f deploy/docker/docker-compose.demo.yml logs api`.
- STARTTLS is mandatory on port 587: the sender refuses to authenticate if the server does not offer it.

### Gmail for dev/demo

1. Turn on 2-Step Verification for the Google account.
2. Create an App Password at <https://myaccount.google.com/apppasswords> (16 characters).
3. Copy `.env.demo.example` to `.env.demo`, fill the Gmail address and App Password, and set
   `APP_PUBLIC_URL` to the current tunnel URL (`./scripts/dev.ps1 demo-url`).
4. `./scripts/dev.ps1 demo-up`. Gmail limits personal accounts to roughly 500 recipients/day; fine for demos.

### Hostinger mailbox for production

1. Buy the domain and an email plan; create a mailbox such as `noreply@<domain>`.
2. In Hostinger DNS add the records Hostinger shows for the mailbox: **MX**, **SPF**
   (`v=spf1 include:_spf.mail.hostinger.com ~all`), **DKIM** (enable in hPanel), and a **DMARC** record
   (start with `v=DMARC1; p=none; rua=mailto:<owner mailbox>`; tighten to `quarantine` after a few clean weeks).
3. Set the production variables above in the VPS `.env` (never in git).
4. Check the per-mailbox sending limit of your Hostinger plan; transactional volume for one cafe
   (invites, resets, receipts) is far below it, which is why marketing campaigns are excluded (E3).

## 4. Failure handling & monitoring

- `email_outbox.status = 'failed'` rows with `last_error` are the delivery problems to review. A backoffice
  view and an owner alert for failed security emails are on the backlog.
- Emails are never sent twice by concurrent workers (`SKIP LOCKED`), but SMTP itself can deliver a message
  and still time out before acknowledging; a retry may then duplicate it. Templates are written so a
  duplicate is harmless (newest link wins because older links are invalidated).
