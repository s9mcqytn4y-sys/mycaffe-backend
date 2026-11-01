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

Files never pass through the API body limit. `POST /api/v1/media/uploads` validates kind, MIME
(JPEG/PNG/WebP), size ≤ 5 MB and permission, then returns a presigned R2 PUT (5 min). `POST
/api/v1/media/uploads/{id}/complete` re-reads the object, checks magic bytes and dimensions, strips
EXIF, writes WebP 200/600/1200 + blurhash, and links it (`image_url`). Errors: 413 too large, 415
unsupported type, 422 dimensions, 409 already completed. Blocked until the R2 bucket exists.

## 5. Network & performance notes

- One VPS (ADR-002): PostgreSQL pool via pgx; every request runs at most a few indexed queries inside
  one tenant transaction. Auth revalidation adds two point lookups (session, settings) per request.
- Catalog read loads 7 small queries per request; clients should use the ETag. Add an in-process cache
  keyed by `(store_id, catalog_version)` only if profiling shows it matters (it is bounded per store).
- No N+1: catalog and order loading use set queries (`IN (...)`) and in-memory assembly.
- Long external calls (Midtrans 12 s) run outside DB transactions (two-phase payment).
- WebSocket fan-out is in-process; the outbox relay (Phase 03) will push events without polling the API.
