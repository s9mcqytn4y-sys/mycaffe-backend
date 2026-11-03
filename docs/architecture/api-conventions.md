# API Conventions: Contracts, CRUD, Middleware & Errors

Applies to every module from iteration 3 on. The catalog module (`internal/handler/catalog_handler.go`,
`internal/service/catalog_service.go`) is the reference implementation; copy its shape for tables,
shifts, promos, and later modules. Machine contract: [docs/api/openapi.yaml](../api/openapi.yaml).

## 1. Request pipeline

```text
Cloudflare / Nginx (TLS, limit_req, X-Request-ID=$request_id, real client IP)
 └─ Gin
    ├─ RequestID          echo/propagate X-Request-ID (logs + responses)
    ├─ LimitBody(1 MiB)   413 above the cap (uploads go straight to R2, never through the API)
    ├─ CORS               exact origins, credentials, X-CSRF-Protection allowed
    ├─ RequireStaff | RequirePermission(p)   token → DB revalidation → permission, before the handler
    ├─ handler            bind JSON → call service → map error → write JSON
    └─ service            validation, tenant tx (WithTenantTx), RLS, audit, outbox/email in the same tx
```

Server limits: `ReadHeaderTimeout` 5 s, `ReadTimeout` 15 s, `WriteTimeout` 30 s, 64 KiB headers.

## 2. Status codes & error body

| Code | When | Body |
| ------ | ------ | ------ |
| 200 | read / update | resource |
| 201 | create | created resource |
| 202 | accepted, side effect async (email) | `{message}` |
| 204 | delete / archive / toggle | — |
| 304 | `If-None-Match` matches the ETag | — |
| 400 `INVALID_JSON` / `INVALID_REQUEST` | body cannot be parsed or required transport field missing | `{code, error}` |
| 401 `UNAUTHENTICATED` / `SESSION_INVALID` / `SESSION_STALE` | no/invalid token | `{code, error}` |
| 403 `FORBIDDEN` / `CSRF_HEADER_REQUIRED` | permission | `{code, error}` |
| 404 `NOT_FOUND` | id not in the caller's store (never "exists elsewhere") | `{code, error}` |
| 409 `CONFLICT` / domain codes | uniqueness, state conflict, idempotency | `{code, error, field?}` |
| 413 | body over 1 MiB | — |
| 422 `VALIDATION_FAILED` | business validation | `{code, error, fields: {"json.path": "pesan"}}` |
| 429 `RATE_LIMITED` | limiter | `{code, error}` + `Retry-After` |
| 503 `SERVICE_TEMPORARILY_UNAVAILABLE` | infrastructure failure (logged with request id) | `{code, error}` |

Rules: messages are Bahasa Indonesia for UI display; `code` is the stable key for the frontend; field
keys are the request's JSON paths (`variants[0].price`) so forms highlight the exact input. Internal
errors are never echoed to clients.

## 3. CRUD checklist (per resource)

| Item | Catalog (done) | Required for every new resource |
| ------ | ---------------- | --------------------------------- |
| Permission per route (`RequirePermission`) + re-check in service | ✅ | ✅ |
| Store from token only; ids scoped by `WithTenantTx` + RLS | ✅ | ✅ |
| Create returns 201 with the resource; PATCH is partial (pointer fields) | ✅ | ✅ |
| Field validation in the service → 422 with `fields` | ✅ (`fieldErrors`) | ✅ |
| Uniqueness by DB constraint → 409 with `field` (`postgres.UniqueViolation`) | ✅ | ✅ |
| Money as integer Rupiah with upper bound; enums validated; text trimmed + length | ✅ | ✅ |
| Archive instead of delete when history references the row | ✅ products/variants | ✅ |
| Audit for price/stock/role/settings changes | ✅ PRICE_CHANGE | ✅ |
| Read model with ETag/304 for lists the POS polls | ✅ catalog | when cacheable |
| Pagination for unbounded lists | n/a (catalog is bounded) | `?limit=` ≤ 100 + cursor `?after=` (orders, audit, movements) |
| Optimistic concurrency (`If-Match` / `updated_at`) | ⏳ | add when two owners may edit the same row |
| Integration test over HTTP: success, 400, 403, 404, 409, 422 | ✅ `TestCatalogIntegration` | ✅ |
| OpenAPI path + schemas, `x-implementation-status` | ✅ | ✅ |

## 4. Uploads (catalog C4, owner decision K9)

Files never pass through the API body limit. `POST /api/v1/media/uploads` (`catalog.manage`) checks
kind (`product_image`), product (this store, not archived), MIME (JPEG/PNG/WebP) and size ≤ 5 MB, then
returns a presigned R2 PUT (5 min) to `uploads/<store>/<id>`. `POST /api/v1/media/uploads/{id}/complete`
(within 15 min) re-reads the object, checks its real size, magic bytes against the declared type and
dimensions (≥ 200 × 200, ≤ 25 MP), applies the JPEG EXIF orientation, drops every metadata block by
re-encoding, writes **JPEG** q80 variants 200/600/1200 px (never upscaled) to
`products/<store>/<id>/<width>.jpg`, deletes the raw upload, sets `products.image_url` to the 600 px
variant and audits `PRODUCT_IMAGE_CHANGED`. Rejected files are deleted. Errors: 413 too large, 415
unsupported or spoofed type, 422 dimensions, 409 already completed / not uploaded yet, 410 expired,
503 bucket not configured. `image_url` cannot be written through the catalog API (422).

Deviation from K9 (approved 2026-11-04): variants are JPEG, not WebP (lossy WebP encoding needs CGO),
and no blurhash yet. At most two images are processed at once (memory bound on the VPS).

## 5. Network & performance notes

- One VPS (ADR-002): PostgreSQL pool via pgx; every request runs at most a few indexed queries inside
  one tenant transaction. Auth revalidation adds two point lookups (session, settings) per request.
- Catalog read loads 7 small queries per request; clients should use the ETag. Add an in-process cache
  keyed by `(store_id, catalog_version)` only if profiling shows it matters (it is bounded per store).
- No N+1: catalog and order loading use set queries (`IN (...)`) and in-memory assembly.
- Long external calls (Midtrans 12 s) run outside DB transactions (two-phase payment).
- WebSocket fan-out is in-process; the outbox relay (Phase 03) will push events without polling the API.

## 6. Realtime delivery (WebSocket)

- **At-least-once.** Outbox events reach `/ws` clients at least once, in per-store sequence order. A
  replay after reconnect (`last_seq`) can resend an event the client already has. Clients must keep
  the highest `seq` (`store_seq`) they processed and ignore any event with `seq` ≤ that value.
- **Gaps mean reload.** More than 500 missed events produce a single `resync_required`; the client
  reloads its state over REST and continues from the new highest `seq`.
- **One sequence per store serialises outbox writers.** `store_event_sequences` hands out `seq` under a
  row lock, so transactions that write outbox events in the same store commit one after another.
  Accepted for one cafe per deployment (single VPS); revisit if a store's write rate makes that lock a
  bottleneck.
- **Connections end with their session.** Every connection re-checks its staff or guest session every
  30 s and is closed when the session is revoked, expired or logged out, the staff account is
  deactivated or locked, or the guest session ended. Caps: 3 per guest session, 20 per client IP, 150
  per store (`429 WS_CONNECTION_LIMIT`). A closed client requests a new ticket and reconnects with
  `last_seq`.
