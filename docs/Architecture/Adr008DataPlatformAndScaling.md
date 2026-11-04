# ADR-008: Data Platform, Queues, Realtime and Scaling Triggers

- **Status**: Accepted (owner, 2026-11-04, decisions V7 and V9). Documents the current design and the
  conditions that would change it.
- **Related**: ADR-002 (single VPS), ADR-005 (offline), [ApiConventions.md](ApiConventions.md) §6.

## 1. Audit of the current platform

| Area | Today | Assessment |
|------|-------|-----------|
| Database | PostgreSQL 18, 37 tables, 24 additive migrations, RLS on every tenant table, `mycaffe_app` NOBYPASSRLS | Sound. Keep it as the only source of truth |
| Pool | `MaxOpenConns 25`, `MaxIdleConns 10`, lifetime 15 min, idle 5 min | Enough for one API instance on KVM 2. Raise only with measured wait time |
| Queues | `email_outbox` and payment reconciliation claim rows with `FOR UPDATE SKIP LOCKED`; `outbox_events` relayed every 2 s | Correct at-least-once semantics. P0-08 adds `attempts`, `dead_at` and a purge |
| Realtime | One in-process hub; broadcast buffer 256, 64 messages per client, ping/pong 60 s, per-store `seq` replay of up to 500 events | Fine for one instance. Gaps trigger `resync_required` |
| Search | Order list: prefix `ILIKE` on order and queue number inside a date window, served by `(store_id, created_at)` | Fine at cafe volume. Catalog search needs a trigram index once menus exceed a few hundred items (§3) |
| Sorting / pagination | Keyset `(created_at, id) DESC` with an opaque cursor | O(log n) per page, stable under inserts. Use it for every new list |
| Rate limits | In-memory per IP and guest; PIN guesses counted from `audit_logs` (HF5) | PIN limits survive restarts. IP limits reset on restart, which is acceptable behind Nginx `limit_req` |

## 2. Redis: not now (V7)

PostgreSQL already provides the needed primitives:

- **queues:** `SKIP LOCKED`;
- **wake-ups:** `LISTEN/NOTIFY`, if 2-second polling ever matters;
- **TTL-like expiry:** an `expires_at` column with a partial index, purged by a job;
- **atomic counters:** `UPDATE … RETURNING`.

All of these sit inside the same transactions, RLS and backups.

**Triggers to add Redis 8.x:**

1. More than one API instance: WebSocket fan-out needs pub/sub, and IP rate limits need a shared counter.
2. Outbox relay p95 latency above 1 s with `LISTEN/NOTIFY` in place.
3. Catalog read p95 above 50 ms after ETags and indexes.

**Design when triggered:**

- Redis pub/sub channel `store:<id>` mirrors hub broadcasts; each instance keeps its own clients. The outbox stays the durable source, and Redis is only transport.
- Rate limits use `INCR` + `EXPIRE`.
- OTPs stay in PostgreSQL, because they need an audit trail.
- No money state ever lives in Redis.

## 3. Algorithms and data structures

| Need | Choice | Why |
|------|--------|-----|
| Catalog search (v0.9 SKU/barcode, name) | Exact SKU via a unique `(store_id, sku)` btree; names via `pg_trgm` GIN with `similarity` ranking | O(log n) for exact lookups, typo-tolerant names, no external search engine |
| Order and audit lists | Keyset pagination | No `OFFSET` scans, stable cursors |
| Work queues (email, SMS, print jobs, outbox) | Table plus `SKIP LOCKED` claim, exponential backoff `min(2^attempts × 30 s, 1 h)`, dead letter after N attempts | Durable, parallel-safe, observable |
| KDS ordering | Priority by `(paid_at, round_number)` with optimistic `expected_status` | Fair first-in-first-out, no lost updates |
| Table timers | `ends_at` column; a ticker scans `ends_at < now() + warn` every 30 s via an index | No in-memory timers to lose on restart |
| Device presence (v0.9/v0.10) | Heartbeat updates `devices.last_seen_at`; status is derived (`online` < 45 s, `stale` < 5 min, otherwise `offline`). The hub keeps a per-store map of connected device ids | Status survives restarts, and live view comes from the hub |
| Idempotency | Key + SHA-256 request hash (existing) | Safe retries from flaky tablets |

## 4. Memory budget (KVM 2, 8 GB)

| Item | Estimate | Guard |
|------|----------|-------|
| PostgreSQL `shared_buffers` | 2 GB | Compose configuration |
| API process | ~150 MB idle; image decode bounded by a 2-slot semaphore (~100 MB each) | `media_service` semaphore |
| WebSocket | ~64 × message size per client; caps of 3 per guest, 20 per IP and 150 per store | `realtime.Limits` |
| Print/ESC-POS rendering | under 64 KB per job | Streaming, no buffering of batches |

## 5. Networking and devices

- **Ingress:** Cloudflare tunnel only (I2). Named Tunnel config is prepared in P0-07, waiting for a domain (V8).
- **Device to API:** HTTPS plus one WebSocket per screen. Reconnect with exponential backoff and `last_seq` replay.
- **Peripherals:** printers are driven by the client (RawBT or a LAN bridge on port 9100). The API returns ESC/POS or TSPL bytes and never connects to cafe LAN devices.
- **Connection status for the frontend:** `GET /pos/devices` (v0.9) lists registered screens with derived status. The WebSocket `presence` events update it live.
