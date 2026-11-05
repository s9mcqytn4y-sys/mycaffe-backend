# SMS One-Time Codes (decisions V4, V5)

SMS is used only for **staff PIN reset** and **customer phone verification** (V4). WhatsApp receipts
reuse the same `message_outbox` queue in v0.9 once a domain allows Meta business verification (V8).

## Drivers

| `SMS_DRIVER` | Where | Behavior |
|----------------|-------|----------|
| `log` (default in development/test) | Local only | The message, including the code, is printed to the API log. Refused in staging and production, because codes are secrets |
| `disabled` (default in staging/production) | Demo until a provider is chosen | SMS endpoints answer `503 SMS_UNAVAILABLE`; nothing is queued |
| provider driver | Demo and production | Not built yet: added behind `sms.Sender` when the owner picks a local aggregator and puts its API key in `.env` |

## Limits

| Limit | Value |
|-------|-------|
| Code | 6 digits, valid for 5 minutes, at most 5 tries; only SHA-256(id + code) is stored |
| Per phone and purpose | 1 code per minute, 5 per hour |
| Public staff endpoints | Shared per-IP budget of 20 per 15 minutes, plus the strict Nginx `limit_req` zone |
| Message body | Scrubbed after delivery or final failure; purged after 90 days with the other queues |

## Flows

1. **Staff PIN reset:**
   - The owner records the staff phone with `POST /api/v1/staff/:id/phone`.
   - The staff member calls `POST /api/v1/auth/staff/pin/forgot-sms {store_slug, phone}`. It always answers 202.
   - The staff member then calls `POST /api/v1/auth/staff/pin/reset-sms {store_slug, phone, code, pin}`. This revokes every session and sends the "PIN changed" e-mail.
2. **Customer verification:**
   - The cashier calls `POST /api/v1/pos/customers/verify {phone, name?}`, which returns a `challenge_id`.
   - The customer reads the code out, and the cashier calls `POST /api/v1/pos/customers/verify/:id/confirm {code}`.
