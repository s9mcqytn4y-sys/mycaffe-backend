# Iteration 5 Plan: Tables, Table Sessions, QR Pay-First Ordering, Outbox Relay & KDS

- **Status**: Plan approved 2026-11-01 (decisions QR1–QR7); not implemented yet. Each phase below is one PR with an additive migration, HTTP
  integration tests on PostgreSQL 18, OpenAPI and docs. Nothing in this document is implemented yet.
- **Rewritten** 2026-11-01 after the owner audit of v0.4.2: the previous draft contradicted decisions
  T1, T2, Q5 and Q6, ignored the emergency pause, and contained SQL that would fail or silently do the
  wrong thing (see §9).
- **Binding decisions**: T1, T2, Q5 (superseded by QR1 once approved), Q6, Q7, C3, N1, R1, E4 in the
  [decision register](decision-register.md) and QR1–QR7; ADR-004 (variant pricing, product stock); hotfix rules
  HF1–HF6.

## 1. Principles

1. **QR ordering is pay-first per device (T2).** A guest order is `pending_payment` with a stock
   reservation and a dynamic QRIS charge; it reaches the kitchen only after a verified Midtrans
   settlement. There is no `pay_after` for QR. Open tabs stay cashier-only (P2).
2. **Table occupancy is derived, never typed in (T1).** A table is occupied while it has an open
   `table_session`; the session opens on the first scan/order and closes by the cashier ("Kosongkan
   meja") or automatically N minutes (store setting, default 30) after all its orders are paid and served.
3. **A guest sees only its own session's orders (Q6).** One guest session per device, TTL 3 h, or until
   all its orders are paid and completed.
4. **The emergency pause is honoured everywhere.** `store_settings.qr_ordering_paused = true` stops new
   guest sessions and new guest orders immediately; open orders keep their payment/tracking flow.
5. **Tenant isolation as today**: every table uses `store_id` + RLS with `current_store_id()`, runtime
   role `mycaffe_app` (NOBYPASSRLS), composite FKs `(store_id, id)`, `ON DELETE RESTRICT` towards
   `stores` and transactional rows, no `DELETE` grant. New `SECURITY DEFINER` functions are listed in §8
   and need owner approval (they extend the allow-list in `AGENTS.md`).
6. **The browser is never trusted** for price, stock, table or store identity: the QR token resolves the
   table server-side; the quote/order engine (ADR-004) re-prices everything.

## 2. Phase 5a: tables, table sessions, QR resolve (migration 000016)

### 2.1 Schema

```sql
CREATE TABLE IF NOT EXISTS tables (
    id            UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    store_id      UUID NOT NULL REFERENCES stores(id) ON DELETE RESTRICT,
    table_number  VARCHAR(50)  NOT NULL,
    label         VARCHAR(100) NOT NULL DEFAULT '',
    section       VARCHAR(50)  NOT NULL DEFAULT '',
    capacity      INT          NOT NULL DEFAULT 4,
    qr_token      VARCHAR(64)  NOT NULL,          -- opaque, >= 128-bit random, base64url (QR1)
    qr_rotated_at TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    is_active     BOOLEAN      NOT NULL DEFAULT true,
    archived_at   TIMESTAMPTZ,
    created_at    TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_tables_store_id UNIQUE (store_id, id),
    CONSTRAINT chk_tables_capacity CHECK (capacity BETWEEN 1 AND 100),
    CONSTRAINT chk_tables_number CHECK (length(btrim(table_number)) > 0),
    CONSTRAINT chk_tables_qr_token CHECK (length(qr_token) >= 22)
);
-- Resolve looks the token up without a store, so it must be unique globally (one index, no duplicate).
CREATE UNIQUE INDEX IF NOT EXISTS uq_tables_qr_token ON tables(qr_token);
CREATE UNIQUE INDEX IF NOT EXISTS uq_tables_store_number_active
    ON tables(store_id, lower(table_number)) WHERE archived_at IS NULL;

CREATE TABLE IF NOT EXISTS table_sessions (
    id                UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    store_id          UUID NOT NULL REFERENCES stores(id) ON DELETE RESTRICT,
    table_id          UUID NOT NULL,
    status            VARCHAR(10) NOT NULL DEFAULT 'open',
    opened_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    opened_via        VARCHAR(10) NOT NULL,               -- 'qr' | 'staff'
    opened_by_user_id UUID,
    settled_at        TIMESTAMPTZ,                        -- all orders paid and served; starts auto-close
    closed_at         TIMESTAMPTZ,
    closed_by_user_id UUID,
    close_reason      VARCHAR(20),                        -- 'cashier' | 'auto'
    CONSTRAINT uq_table_sessions_store_id UNIQUE (store_id, id),
    CONSTRAINT fk_table_sessions_table FOREIGN KEY (store_id, table_id) REFERENCES tables(store_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_table_sessions_opened_by FOREIGN KEY (store_id, opened_by_user_id) REFERENCES users(store_id, id) ON DELETE SET NULL (opened_by_user_id),
    CONSTRAINT fk_table_sessions_closed_by FOREIGN KEY (store_id, closed_by_user_id) REFERENCES users(store_id, id) ON DELETE SET NULL (closed_by_user_id),
    CONSTRAINT chk_table_sessions_status CHECK (status IN ('open', 'closed')),
    CONSTRAINT chk_table_sessions_via CHECK (opened_via IN ('qr', 'staff')),
    CONSTRAINT chk_table_sessions_closed CHECK ((status = 'closed') = (closed_at IS NOT NULL)),
    CONSTRAINT chk_table_sessions_reason CHECK (close_reason IS NULL OR close_reason IN ('cashier', 'auto'))
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_table_sessions_one_open
    ON table_sessions(store_id, table_id) WHERE status = 'open';

-- orders.table_id already exists (000011, nullable, no FK): add the composite FK separately.
-- orders.queue_number and orders.table_number already exist: not added again.
ALTER TABLE orders ADD COLUMN IF NOT EXISTS table_session_id UUID;
ALTER TABLE orders ADD CONSTRAINT fk_orders_table
    FOREIGN KEY (store_id, table_id) REFERENCES tables(store_id, id) ON DELETE RESTRICT NOT VALID;
ALTER TABLE orders VALIDATE CONSTRAINT fk_orders_table;   -- refuses if orphan table_id values exist
ALTER TABLE orders ADD CONSTRAINT fk_orders_table_session
    FOREIGN KEY (store_id, table_session_id) REFERENCES table_sessions(store_id, id) ON DELETE RESTRICT;
CREATE INDEX IF NOT EXISTS idx_orders_table_session ON orders(store_id, table_session_id);

-- The auto-close delay reuses store_settings.table_idle_close_minutes (000007, default 30, 5..240).

-- RLS: same pattern as every tenant table (current_store_id(), never inline current_setting).
ALTER TABLE tables ENABLE ROW LEVEL SECURITY;
CREATE POLICY p_tables_tenant_isolation ON tables FOR ALL TO mycaffe_app
    USING (store_id = current_store_id()) WITH CHECK (store_id = current_store_id());
ALTER TABLE table_sessions ENABLE ROW LEVEL SECURITY;
CREATE POLICY p_table_sessions_tenant_isolation ON table_sessions FOR ALL TO mycaffe_app
    USING (store_id = current_store_id()) WITH CHECK (store_id = current_store_id());
GRANT SELECT, INSERT, UPDATE ON tables, table_sessions TO mycaffe_app;   -- no DELETE: tables are archived
```

`chk_audit_logs_action` gains `TABLE_CREATE`, `TABLE_UPDATE`, `TABLE_QR_ROTATE`, `TABLE_SESSION_CLOSE`.

### 2.2 Derived table status

Computed in the read query (no stored `status` column):

| Status | Rule |
|--------|------|
| `available` | no open session, or the open session is past `settled_at + table_idle_close_minutes` (closed lazily, below) |
| `occupied` | open session with at least one order not yet paid or not yet served |
| `settled` | open session with `settled_at` set, auto-close countdown running |

Auto-close is **lazy**: any read or scan of the table closes a session whose countdown has passed
(`close_reason = 'auto'`, actor NULL) inside the same tenant transaction. No background worker and no new
cross-tenant function are needed; the board shows the right state because status is derived at read
time. `settled_at` is set (and cleared again if a new order arrives) in the transaction that pays or
serves the last order of the session.

### 2.3 QR token (decision QR1, supersedes Q5 once approved)

- Token: 32 random bytes from `crypto/rand`, base64url (43 chars, 256 bit), stored as is. It is printed
  publicly on the table, so it identifies a table rather than authenticating a person; hashing it would
  prevent reprinting without rotating.
- Rotation: `POST /api/v1/pos/tables/:id/rotate-qr` (owner, audited `TABLE_QR_ROTATE`) replaces the token;
  the old sticker stops resolving immediately. Open sessions and their orders are unaffected.
- Pause: `store_settings.qr_ordering_paused` (already exists) blocks new guest sessions and orders store-wide;
  `tables.is_active = false` blocks a single table.
- Why it replaces Q5 (HMAC with key id + `qr_version`): an opaque random token gives the same revocation
  (rotate = new token), needs no signing-key management or key rotation, and resolving it is one indexed
  lookup. What is lost: offline verification of a token without the database, which no flow needs.

### 2.4 Endpoints (5a)

| Method & path | Who | Notes |
|---------------|-----|-------|
| `GET /api/v1/pos/tables` | staff | tables with derived status, open session, unpaid total |
| `POST /api/v1/pos/tables` | owner (new permission `table.manage`) | standard 422/409 contract |
| `PATCH /api/v1/pos/tables/:id` | owner | number, label, section, capacity, `is_active` |
| `POST /api/v1/pos/tables/:id/archive` | owner | refused with 409 while a session is open |
| `POST /api/v1/pos/tables/:id/rotate-qr` | owner | returns the new token once for printing |
| `POST /api/v1/pos/table-sessions/:id/close` | cashier/owner | "Kosongkan meja"; refused while an order is unpaid (409), audited |
| `POST /api/v1/pos/tables/:id/sessions` | cashier/owner | seat guests from the POS (`opened_via=staff`); returns the open session if one exists |
| `GET /api/v1/public/qr/:token` | public | resolves the table: store name/slug, table number, `ordering_open`; no ids beyond what the guest flow needs |

Resolve uses `resolve_table_qr(p_token)` (SECURITY DEFINER, §8) returning only
`(store_id, table_id, is_active)`; everything else runs in `WithTenantTx` with that store id.

## 3. Phase 5b: guest session, QR pay-first order, tracker (migration 000017)

### 3.1 Guest session (Q6)

```sql
CREATE TABLE IF NOT EXISTS guest_sessions (
    id               UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    store_id         UUID NOT NULL REFERENCES stores(id) ON DELETE RESTRICT,
    table_session_id UUID NOT NULL,
    token_hash       BYTEA NOT NULL,              -- SHA-256 of the 256-bit cookie secret
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at       TIMESTAMPTZ NOT NULL,        -- created_at + 3 h
    ended_at         TIMESTAMPTZ,
    CONSTRAINT uq_guest_sessions_store_id UNIQUE (store_id, id),
    CONSTRAINT uq_guest_sessions_token UNIQUE (token_hash),
    CONSTRAINT fk_guest_sessions_table_session FOREIGN KEY (store_id, table_session_id)
        REFERENCES table_sessions(store_id, id) ON DELETE RESTRICT
);
ALTER TABLE orders ADD COLUMN IF NOT EXISTS guest_session_id UUID;
ALTER TABLE orders ADD CONSTRAINT fk_orders_guest_session
    FOREIGN KEY (store_id, guest_session_id) REFERENCES guest_sessions(store_id, id) ON DELETE RESTRICT;
-- RLS + GRANT SELECT, INSERT, UPDATE as in 5a.
```

- Credential: HttpOnly cookie `__Secure-mc_guest = <store_id>.<session_id>.<secret>` (`SameSite=Strict`,
  `Path=/api/v1/public`), the same shape as the staff refresh token, so the store id is known before the
  tenant transaction without a new definer function. The store id in the cookie is a lookup key only;
  the session row must match `(store_id, id, token_hash)`. Mutating guest requests require
  `X-CSRF-Protection: 1`. A guest credential can never call staff routes (different cookie, different
  middleware), which satisfies Q6's "distinct from staff tokens".
- Lifetime: 3 h from creation, or ended when all of its orders are paid and completed; ended/expired
  sessions get 401 `GUEST_SESSION_EXPIRED` and the device rescans.

### 3.2 Endpoints (5b)

| Method & path | Notes |
|---------------|-------|
| `POST /api/v1/public/qr/:token/session` | refuses when the store/table is inactive or `qr_ordering_paused` (409 `QR_ORDERING_PAUSED`) or the store is closed (§7); opens the table session if none is open (`opened_via='qr'`), creates the guest session, sets the cookie |
| `GET /api/v1/public/guest/session` | table, store, expiry, ordering_open |
| ~~`POST /api/v1/public/guest/orders/quote`~~ | not added: guests use the existing `POST /api/v1/public/stores/{slug}/orders/quote` (same engine), the store slug comes from `GET /api/v1/public/qr/:token` |
| `POST /api/v1/public/guest/orders` | `idempotency_key`, items, `expected_total` (409 `QUOTE_CHANGED`), optional `customer_name` (email + E4 consent deferred to the receipts iteration). Creates a `pending_payment` order with stock reserved and `expires_at`, bound to the table, table session and guest session |
| `POST /api/v1/public/guest/orders/:id/payments` | dynamic QRIS only (Midtrans), idempotent; only orders of this guest session |
| `GET /api/v1/public/guest/orders` | orders of this guest session only (never the table's other devices) |
| `GET /api/v1/public/orders/track/:tracking_token` | public tracker (§3.3) |

The contained `POST /api/v1/orders` (503 in staging/production) and its handler are **deleted** in 5b;
its replacement is `POST /api/v1/public/guest/orders`.

### 3.3 Order tracker

- `orders.tracking_token_hash BYTEA` (unique): a 256-bit random token is issued with the order and shown
  as a link/QR; only its SHA-256 is stored.
- `track_order_by_token(p_token_hash BYTEA)` (SECURITY DEFINER, §8) returns only `store_name,
  order_number, queue_number, status, payment_status, updated_at` and nothing for orders older than the
  retention (proposal: 24 h after completion; the R1 digital receipt keeps its own 90-day token).

## 4. Phase 5c: outbox relay and WebSocket tickets (migration 000018, risk R2)

### 4.1 Ordering and replay

- `outbox_events` gains `store_seq BIGINT` with `UNIQUE (store_id, store_seq)`. A `BEFORE INSERT`
  trigger takes the next number from `store_event_sequences(store_id, last_seq)` with
  `UPDATE … RETURNING`; the row lock is held until commit, so per-store sequence numbers commit in order
  (acceptable contention for one cafe). The trigger also calls `pg_notify('outbox', store_id)`.
- Existing columns are reused: `processed_at` = published, `retry_count`, `last_error`.
- Replay: a client reconnects with `last_seq`; the server sends events with `store_seq > last_seq`
  (filtered by the channel's permissions) up to 500. If `last_seq` is older than the retained window
  (7 days of processed events), it sends `resync_required` and the client reloads its snapshot over REST.
  Delivery is at-least-once; clients deduplicate by `store_seq`.

### 4.2 Relay

- In-process worker (one API replica, ADR-002): waits on `LISTEN outbox` with a 2 s polling fallback.
  Stores with pending events come from `list_stores_with_pending_outbox()` (SECURITY DEFINER, §8, same
  shape as `list_stores_with_due_email`). Per store, inside `WithTenantTx`:
  `SELECT … WHERE processed_at IS NULL ORDER BY store_seq LIMIT 100 FOR UPDATE SKIP LOCKED`, publish to
  the hub, set `processed_at`; on failure increment `retry_count`, set `last_error`, back off.
- Event names are dot-case and listed in one Go file: `order.created`, `order.paid`, `order.cancelled`,
  `order.expired`, `order.round_submitted`, `payment.refund_required`,
  `payment.late_settlement_stock_exhausted`, `payment.split_settled`, `table.session_opened`, `table.session_closed`,
  `kds.ticket_created`, `kds.ticket_updated`.

### 4.3 WebSocket authentication

- Staff: `POST /api/v1/pos/ws-ticket` (bearer access token) returns a single-use ticket valid 30 s,
  `<store_id>.<ticket_id>.<secret>`, stored hashed in `ws_tickets(store_id, id, user_id, session_id,
  channels, expires_at, used_at)`. The browser opens `GET /ws?ticket=…`; the ticket is consumed
  (`used_at`) in the upgrade transaction. No JWT ever goes in the query string. The staff session must
  still be valid at redemption.
- Guests: `POST /api/v1/public/guest/ws-ticket` (guest cookie + CSRF) issues the same kind of ticket for
  channel `guest.<guest_session_id>` (the cookie path `/api/v1/public` does not reach `/ws`); the upgrade
  is checked against the `Origin` allow-list and guests receive only their own orders' public status.
- Channels: `pos` (cashier/owner), `kds.bar`, `kds.kitchen` (barista/kitchen; cashier when
  `cashier_handles_bar`, C3), `guest.<guest_session_id>`. The queue TV display needs a device credential
  and is out of scope for iteration 5.
- `/ws` is open since 5c; the old JWT-header WebSocket path and its staging/production 503 containment are removed.

## 5. Phase 5d: KDS tickets (migration 000019)

### 5.1 Ticket model

A ticket is one **(order or round) × station**. A mixed order (coffee + croissant) yields a `bar` and a
`kitchen` ticket; every tab round yields its own tickets; items with `prep_station = 'none'` create no
ticket.

```sql
ALTER TABLE order_items ADD COLUMN IF NOT EXISTS snapshot_prep_station VARCHAR(10) NOT NULL DEFAULT 'none';
-- backfilled from products.prep_station; CHECK IN ('bar', 'kitchen', 'none'), the values already in the DB

CREATE TABLE IF NOT EXISTS kds_tickets (
    id           UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    store_id     UUID NOT NULL REFERENCES stores(id) ON DELETE RESTRICT,
    order_id     UUID NOT NULL,
    round_id     UUID,                              -- NULL for non-tab orders
    station      VARCHAR(10) NOT NULL,
    status       VARCHAR(12) NOT NULL DEFAULT 'queued',
    late_payment BOOLEAN NOT NULL DEFAULT false,   -- Q7
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    started_at   TIMESTAMPTZ, ready_at TIMESTAMPTZ, served_at TIMESTAMPTZ, cancelled_at TIMESTAMPTZ,
    updated_by_user_id UUID,
    CONSTRAINT uq_kds_tickets_store_id UNIQUE (store_id, id),
    CONSTRAINT uq_kds_tickets_unit UNIQUE NULLS NOT DISTINCT (store_id, order_id, round_id, station),
    CONSTRAINT fk_kds_tickets_order FOREIGN KEY (store_id, order_id) REFERENCES orders(store_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_kds_tickets_round FOREIGN KEY (store_id, round_id) REFERENCES order_rounds(store_id, id) ON DELETE RESTRICT,
    CONSTRAINT chk_kds_tickets_station CHECK (station IN ('bar', 'kitchen')),
    CONSTRAINT chk_kds_tickets_status CHECK (status IN ('queued', 'preparing', 'ready', 'served', 'cancelled'))
);
CREATE INDEX IF NOT EXISTS idx_kds_tickets_active ON kds_tickets(store_id, station, status, created_at)
    WHERE status IN ('queued', 'preparing', 'ready');
-- RLS + GRANT SELECT, INSERT, UPDATE as in 5a.
```

Ticket lines are the order items with the same `(order_id, round_id, snapshot_prep_station)`; no line
table is needed.

### 5.2 Lifecycle

- Created in the transaction that makes the work real: pay-first order → on settlement (`order.paid`);
  POS pay-after order → on creation; tab round → on round submit. A late settlement revived by Q7 sets
  `late_payment = true`.
- Transitions: `queued → preparing → ready → served`, `ready → preparing` (recall), any open status →
  `cancelled` (void). `POST /api/v1/kds/tickets/:id/transition {to, expected_status}`; a stale
  `expected_status` returns 409 so two screens never overwrite each other. Station permissions follow the
  existing RBAC: `kds.ticket.*.barista` covers station `bar`, `kds.ticket.*.kitchen` covers `kitchen`
  (cashier gets the barista set with the C3 toggle; owner has both).
- Order and round status are derived in the same transaction: all tickets served → `served`; all
  ready-or-served → `ready`; any preparing → `preparing`; otherwise `queued`. An order without tickets
  (only `none` items) goes straight to `ready`.
- Void (P4/HF2): items whose ticket is still `queued` are restocked; items whose ticket is `preparing` or
  later are recorded as waste instead. This refines the v0.4.3 helper and is part of 5d.

## 6. Phasing

| Phase | Scope | Migration | Depends on |
|-------|-------|-----------|------------|
| 5a | tables CRUD, QR rotate, table sessions, derived status, lazy auto-close, QR resolve | 000016 | **implemented** (PR for branch `feature/iteration-5a-tables`) |
| 5b | guest session cookie, guest quote/order/QRIS payment, own-orders list, tracker, delete `POST /api/v1/orders` | 000017 | **implemented** (branch `feature/iteration-5b-guest-orders`); `settled_at` upkeep and "session ends when all orders are completed" wait for KDS serve events (5d) |
| 5c | `store_seq`, relay worker, replay, WS tickets, channels, `/ws` opened | 000018 | **implemented** (branch `feature/iteration-5c-outbox-relay`); guests get a ticket too (`POST /api/v1/public/guest/ws-ticket`) instead of a cookie on the upgrade |
| 5d | KDS tickets, transitions, derived order/round status, void waste vs restock | 000019 | 5c |

## 7. Public anti-abuse

- Rate limits: Nginx zone `qr_limit` on `/api/v1/public/` per client IP (proposal 30 req/min, burst 20)
  plus an in-app limiter per guest session (10 order attempts / 10 min) and per IP for session creation
  (10 / 10 min).
- Caps: at most 2 unpaid orders per guest session and 6 per table session; reservations expire through
  the existing payment-expiry worker, which releases stock.
- Refusals: store inactive, table inactive/archived, `qr_ordering_paused`, or store closed. Proposal for
  "closed" until opening hours exist: no shift is open on any drawer → 409 `STORE_CLOSED`.
- Inputs: item count ≤ 30 per order, quantity ≤ 20 per line, `customer_name` ≤ 60 chars, body cap 64 KiB
  on guest routes, email validated and stored only with E4 consent rules.
- Logs never contain guest cookies, tracking tokens or full QR tokens (first 6 chars only).

## 8. Approvals (granted 2026-11-01 as QR1–QR7)

| Item | Why it needs approval |
|------|----------------------|
| **QR1** opaque rotatable token replaces Q5 (HMAC + key id + `qr_version`) | changes a recorded decision |
| New SECURITY DEFINER functions `resolve_table_qr`, `track_order_by_token`, `list_stores_with_pending_outbox` | extends the allow-list in `AGENTS.md` |
| New RBAC permission `table.manage` (owner only) | changes the permission matrix |
| "Store closed" = no open shift | business rule |
| Limits in §7 and tracker retention (24 h) | business numbers |
| Lazy auto-close instead of a worker | behaviour visible to staff |

## 9. Corrections to the previous draft

| Previous draft | Problem | Now |
|----------------|---------|-----|
| `pay_after` for QR | contradicts T2, lets anonymous orders reach the kitchen unpaid | QR is pay-first only |
| `tables.status` set manually | contradicts T1 | derived from `table_sessions` |
| no guest/table sessions | contradicts Q6/T1 | 5a/5b |
| emergency pause ignored | `qr_ordering_paused` exists | enforced on session and order |
| `store_settings.billing_mode` in the resolve function | column does not exist; function creation fails | not referenced |
| `ADD COLUMN IF NOT EXISTS table_id … REFERENCES` | column exists (000011), so the FK was silently skipped | separate composite FK |
| `ADD COLUMN queue_number` | already exists | not added |
| token unique per store but resolved globally | ambiguous lookup, duplicate index | one global unique index |
| inline `current_setting` in policies | regression of fix A7 | `current_store_id()` |
| `gen_random_uuid()`, `ON DELETE CASCADE` from stores | inconsistent with schema conventions | `uuid_generate_v4()`, `RESTRICT` |
| `prep_station` value `barista` | DB values are `bar`, `kitchen`, `none` | DB values |
| KDS status on the order | cannot represent mixed bar+kitchen orders or tab rounds | ticket per (order/round × station) |
| relay unspecified, `KDS_STATUS_UPDATED` | no polling/marking/replay/WS auth, inconsistent naming | §4, dot-case events |
