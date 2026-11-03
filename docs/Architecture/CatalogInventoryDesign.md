# Catalog, Variants, BOM & Inventory Design

Owner decisions K1 (variants + SKU), K2 (ready-to-serve units + recipe/BOM for prepared items),
K7 (promo & bundles), K9 (R2 media). Status: **Planned** unless marked otherwise. Schema changes are
additive migrations; existing tables (`products`, `modifier_*`, `order_items`, `order_item_options`)
are extended, never duplicated.

## 1. Current state (Implemented & Tested)

| Table | What it holds | Gaps |
| ------- | --------------- | ------ |
| `categories` | name, order, active | no image, no parent, no availability schedule |
| `products` | name, description, `base_price`, `image_url`, available, `track_stock` + `stock_quantity` (finished goods) | no SKU, no variants, no unit, no station, no type, no cost/HPP |
| `modifier_groups` / `modifier_options` | single/multiple, min/max, price delta, optional stock | — (kept for add-ons) |
| `product_modifier_groups` / `option_modifier_groups` | product→group, 1-level nested sub-addons | — |
| `order_items` / `order_item_options` | **snapshots** of names and prices | add variant + recipe cost snapshot |

Server-side pricing (`internal/service/pricing.go`) already recomputes every total; the browser is never
trusted for prices.

## 2. Target model

```text
categories        + image_key, parent_id (1 level), schedule (optional)
products          + product_type ('prepared' | 'ready_to_serve' | 'bundle'),
                    prep_station ('bar' | 'kitchen' | 'none'), tags[], allergens[], is_featured
product_variants  (store_id, id, product_id, name, sku UNIQUE per store, price, is_default,
                   is_available, display_order)              -- "Regular", "Large", "Iced"
                   ready_to_serve: + stock_quantity (units); prepared: stock via recipe
units             fixed enum: g, kg, ml, l, pcs              -- conversions in code, not data
ingredients       (store_id, id, name, sku, base_unit, stock_on_hand NUMERIC(14,3), min_stock,
                   avg_unit_cost BIGINT per base unit, is_active)
recipe_lines      (store_id, variant_id | modifier_option_id, ingredient_id, quantity, unit)
stock_movements   (store_id, id, ingredient_id | variant_id, delta, reason
                   ['sale','refund','purchase','adjustment','opname','waste'], ref_type, ref_id,
                   unit_cost, actor_id, created_at)          -- append-only ledger
stock_opnames     (store_id, id, started_by, closed_by, status) + lines (counted vs system)
media_assets      (store_id, id, kind ['product','category','logo','static_qris'], object_key,
                   content_type, bytes, width, height, sha256, created_by)  -- R2, K9
promotions        (store_id, id, kind ['percent','amount','bundle_price'], scope, schedule,
                   starts_at, ends_at, quota, priority, is_active) + promotion_targets
```

Rules:

- **Variant = what you sell** (own SKU, price, recipe). **Modifier = what you add** (extra shot, oat milk).
  An order line references `variant_id`; modifiers stay as today.
- **Prepared items** deduct ingredients from `recipe_lines` (variant + chosen modifiers) when the order is
  paid (same transaction as `commitPaidOrderTx`); **ready-to-serve** items deduct units, as today's
  reservation flow does. Insufficient ingredient stock never blocks a paid sale (it goes negative and
  alerts the owner); ready-to-serve stock does block (it is physically countable).
- **HPP** of a line = Σ(recipe quantity × ingredient `avg_unit_cost`) snapshotted on `order_items`
  (`snapshot_unit_cost`), so later price changes never rewrite history. Gross profit is only reported
  for lines with a recipe; others show "HPP belum diatur".
- `avg_unit_cost` is a moving average updated on each `purchase` movement (integer Rupiah per base unit,
  with 3-decimal quantities to support grams/ml).
- Quantities use `NUMERIC(14,3)`; money stays `BIGINT` Rupiah.

## 3. Menu display: catalog payload for the PWA

`GET /api/v1/public/stores/{slug}/catalog` (guest, cacheable, ETag) and `GET /api/v1/pos/catalog` (staff,
includes cost when `catalog.cost.read`):

```json
{
  "store": {"name": "...", "logo": {"url_200": "...", "url_600": "..."}, "currency": "IDR",
            "tax_bps": 1000, "service_bps": 500, "takeaway_service_bps": 0,
            "is_open": true, "qr_ordering_paused": false, "timezone": "Asia/Jakarta"},
  "categories": [{"id": "...", "name": "Kopi", "display_order": 1, "image": {...}}],
  "products": [{
    "id": "...", "category_id": "...", "name": "Caffe Latte", "description": "...",
    "type": "prepared", "prep_station": "bar", "tags": ["best-seller"], "allergens": ["susu"],
    "image": {"url_200": "...", "url_600": "...", "url_1200": "...", "blurhash": "...", "alt": "Caffe Latte"},
    "variants": [{"id": "...", "name": "Regular", "sku": "LAT-R", "price": 26000, "is_default": true,
                  "available": true, "sold_out_reason": null}],
    "modifier_groups": [{"id": "...", "name": "Susu", "selection": "single", "min": 1, "max": 1,
                         "options": [{"id": "...", "name": "Oat", "price_delta": 5000, "available": true,
                                      "sub_groups": []}]}],
    "promotion": {"label": "Happy Hour 20%", "price_after": 20800, "ends_at": "..."}
  }],
  "bundles": [{"id": "...", "name": "Kopi + Croissant", "price": 45000, "slots": [...]}],
  "generated_at": "...", "etag": "..."
}
```

Metadata the frontend needs from the server:

| Need | Field(s) |
| ------ | ---------- |
| Layout & navigation | category order, featured flag, tags |
| Image performance | 3 WebP sizes, blurhash placeholder, `alt` text (a11y) |
| Price display | variant price, promo label + price after promo (display only; server recomputes) |
| Validation hints | modifier `min/max`, required groups, sub-groups |
| Availability | `available` + reason (sold out, outside schedule, store closed, QR paused) |
| Kitchen routing (POS/KDS) | `prep_station`, `type` |
| Totals preview | tax/service bps (display only) |
| Caching | `etag`, `generated_at`; WebSocket `catalog.changed` event invalidates |

## 4. Media handling (K9)

1. Owner requests `POST /api/v1/media/uploads` with `kind`, `content_type`, `bytes` → server checks
   permission (`catalog.manage` or `store.settings.manage`), size ≤ 5 MB, type ∈ JPEG/PNG/WebP, and returns
   a presigned R2 PUT URL (5 min) for key `stores/{store_id}/{kind}/{uuid}`.
2. Browser uploads directly to R2 (no API bandwidth).
3. `POST /api/v1/media/uploads/{id}/complete` → server downloads the object, verifies magic bytes and
   dimensions (≤ 6000 px), strips EXIF/GPS, writes WebP 200/600/1200 variants, computes blurhash + SHA-256,
   deletes the original, records `media_assets`. Rejected files are deleted.
4. Public URLs go through the R2 custom domain / CDN; the static QRIS image is served only via a signed,
   short-lived URL during checkout.

## 5. Delivery order

| Step | Content | Depends on |
| ------ | --------- | ------------ |
| C1 | Catalog read API (current schema) + ETag, staff/guest views | ✅ iteration 3 |
| C2 | Variants + SKU + product type + prep station (migration 000008, backfill one default variant per product) | ✅ iteration 3 |
| C3 | Catalog management API (CRUD, availability toggles per station permission) | ✅ categories/products/variants; modifiers C3b in iteration 4 |
| C4 | Media uploads (R2) | C3, R2 bucket |
| C5 | Ingredients, recipes, stock movements, opname, low-stock alert email | C2 |
| C6 | Promotions + bundles in pricing engine (single best promo per line, snapshot on order) | C2 |
| C7 | HPP snapshot + gross-profit report | C5 |
