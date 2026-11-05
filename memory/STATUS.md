# MyCaffe Backend — Project Status (`memory/STATUS.md`)

**Last Updated**: 2026-11-06  
**Active Branch**: `feat/seeder-catalog-webp-agent-setup` (branched from `origin/staging`)  
**Latest Merged Milestone**: v0.9 Cafe Floor (`#73`–`#77`) + Infra/Cloudflare/Sentry/Modularization (`#65`)

## 1. Infrastructure & Integrations Status
| Subsystem | Tier | Details |
| :--- | :--- | :--- |
| **PostgreSQL 18 + RLS** | *Implemented & Tested* | 37 migrations (`000001`–`000037`), `mycaffe_migrator` (DDL) + `mycaffe_app` (`NOBYPASSRLS` DML). Verified by `verify_schema_constraints.sql`. |
| **Midtrans Core API (QRIS/VA/E-Wallet)** | *Implemented & Tested* | 2-phase charge lifecycle (`payments.id.String()` as `provider_order_id` & `Idempotency-Key`), SHA-512 webhook verification, no Snap dependency. Sandbox active for dev/demo; Production keys stored for post-review switch. |
| **Cloudflare (`abbdullaziz.online`)** | *Implemented & Tested* | Zone `7fa2f37d613b1bb7bc0c3a830c023730`. Named Tunnel `mycaffe-demo` (`https://demo.abbdullaziz.online`), R2 public media bucket `mycaffe-media` (`https://cdn.abbdullaziz.online`), R2 private backup bucket `mycaffe-backups`, Email Routing active. |
| **Sentry Error & Panic Tracking** | *Implemented & Tested* | `SentryMiddleware` + `BeforeSend` PII scrubbing active (`internal/handler/middleware.go`, `cmd/api/main.go`). |
| **Email & SMS Outbox** | *Implemented & Tested* | Transactional `email_outbox` (Gmail App Password SMTP) & `message_outbox` (`SMS_DRIVER=email|webhook|noop`, `internal/pkg/sms/sms.go`). |
| **Cafe Floor v0.9 & Receipt Delivery** | *Implemented & Tested* | Table move/merge/split (`000033`), table timers (`000034`), queue TV display tokens (`000035`), server print queue (`000036`), receipt delivery ledger (`000037`). |
| **Catalog Seeder & R2 WebP Assets** | *Implemented & Tested* | [`db/seeds/development.sql`](../db/seeds/development.sql) overhauled: 0 pre-seeded orders/transactions, 2 stores (*MyCaffe Flagship Roastery — Senopati* & *MyCaffe Express Grab & Go — Sudirman MRT*), owner `abdulaziz543333@gmail.com`, 38 products across 11 categories, 51 variants, 10 modifier groups, 28 options, 11 tables, 3 cash drawers, and 144 `.webp` assets (`1:1`, `4:3`, `16:9`) live on `https://cdn.abbdullaziz.online/seeds/...`. |

