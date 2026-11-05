# SMS & OTP Delivery Setup

SMS delivers two kinds of 6-digit OTPs (owner decision **V4**, **V5**):

- **Customer phone verification at the cashier** (`POST /api/v1/pos/customers/verify` -> `POST /api/v1/pos/customers/verify/:id/confirm`), storing the verified customer on the order when provided.
- **Staff self-service PIN reset by phone** (`POST /api/v1/auth/staff/pin/forgot-sms` -> `POST /api/v1/auth/staff/pin/reset-sms`), after the owner has set a verified phone on the staff member (`POST /api/v1/staff/:id/phone`).

Messages are queued in `message_outbox` inside the caller's tenant transaction and delivered asynchronously by the background worker in [`internal/service/message_service.go`](file:///c:/Projects/myCaffe-backend/internal/service/message_service.go) (5-second poll, up to 5 attempts with exponential backoff).

## Supported `SMS_DRIVER` Modes

| Driver | Allowed Environments | Cost | Description |
| :--- | :--- | :--- | :--- |
| `log` | `development`, `test` (**refused in `staging` & `production`**) | Free | Logs `[SMS LOG DRIVER] to=+628... body="..."` to structured stdout. Blocked in `staging`/`production` so OTP codes never leak into shared container logs. |
| `email` | `development`, `test`, `staging` (Demo), `production` | **100% Free** | **SMS-to-Email Bridge** ([`sms.EmailSender`](file:///c:/Projects/myCaffe-backend/internal/pkg/sms/sms.go)). Wraps the active `mailer.Sender` (`EMAIL_DRIVER=smtp`) and delivers SMS OTP messages in real time to `SMS_EMAIL_TO` (or `EMAIL_FROM_ADDRESS`) with subject `[MyCaffe SMS Gateway] Pesan untuk +628...`. Ideal for live end-to-end OTP verification during client demos on `abbdullaziz.online` without paying telco SMS fees. |
| `webhook` | `development`, `test`, `staging`, `production` (`https://` required in `production`) | Free / BYO Gateway | **HTTP Webhook Bridge** ([`sms.WebhookSender`](file:///c:/Projects/myCaffe-backend/internal/pkg/sms/sms.go)). Sends `POST` JSON `{"to":"+628...","body":"..."}` with optional `Authorization: Bearer <SMS_WEBHOOK_TOKEN>` to `SMS_WEBHOOK_URL` (compatible with Telegram Bot bridge, WhatsApp Cloud API worker, Fonnte, or Zenziva). |
| `disabled` | All environments | Free | Turns off SMS delivery; SMS endpoints fail closed with `503 SMS_UNAVAILABLE` while email PIN reset (`POST /api/v1/auth/staff/pin/forgot`) continues to work. |

## Environment Variables

```dotenv
# Driver selection: log | email | webhook | disabled
SMS_DRIVER=email

# Destination inbox when SMS_DRIVER=email (defaults to EMAIL_FROM_ADDRESS if omitted)
SMS_EMAIL_TO=owner@abbdullaziz.online

# Endpoint and optional Bearer token when SMS_DRIVER=webhook
SMS_WEBHOOK_URL=
SMS_WEBHOOK_TOKEN=
```

## Phone Format & Abuse Limits

- Indonesian inputs (`0812…`, `62812…`, `+62 812-…`) are normalized to E.164 (`+62812…`); Indonesian mobile numbers must start with `8` after `+62`. Other international E.164 numbers (`+1…`, `+65…`) are accepted as-is.
- Per-phone cooldown: **60 seconds** between sends.
- Per-phone daily cap: **5 OTPs per UTC day**.
- OTP TTL: **5 minutes**, single-use, maximum **5 verification attempts** before the code is burned.
- Codes are stored only as `SHA-256(code)` in `phone_verifications.code_hash` and `staff_pin_resets.token_hash`.
