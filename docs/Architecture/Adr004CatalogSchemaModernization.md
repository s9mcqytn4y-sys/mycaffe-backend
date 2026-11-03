# ADR-004: Catalog Cleanup — Variant-Only Pricing, Modifier Bounds, Product-Level Stock

- **Status**: Accepted (owner, 2026-11-01) — implemented in migration 000015, before Iteration 5
- **Deciders**: Product owner, backend
- **Supersedes**: the earlier draft of this ADR (drop `base_price`, per-variant stock)

## 1. Context

Iteration 3 added `product_variants` (decision K1) but pricing still fell back to `products.base_price`
when a product had no default variant, so two columns could disagree about the price. Modifier groups
enforced `min_select <= max_select` in the database, while "a single-choice group allows at most one
option" lived only in the service. Iteration 4d (void/refund, hotfix v0.4.3) restores stock at product
level, and Iteration 5 (QR guest orders) prices through the same engine, so both depend on these rules
being settled first. The cleanup was agreed before 4d and skipped; this ADR records doing it now.

## 2. Decisions

| ID | Decision | Rationale |
|----|----------|-----------|
| D1 | **Pricing reads only `product_variants.price`.** A product without an available default variant cannot be sold (`ErrProductUnavailable`, fail closed). | One price source; the catalog API already creates every product with at least one variant. |
| D2 | **`products.base_price` is kept as a deprecated mirror** of the default variant price (catalog service keeps it in sync; column comment marks it deprecated). It is not dropped. | Dropping a column is a destructive migration and breaks legacy readers; nothing reads it for money any more. Drop later in its own approved migration. |
| D3 | **Unit stock stays on `products`** (`track_stock`, `stock_quantity`); all variants of a product share it. | Owner decision. Prepared drinks will consume ingredients through per-variant recipes/BOM (K2, inventory iteration), so unit stock only matters for ready-to-serve items. The void/refund stock helper fixed in v0.4.3 stays unchanged. |
| D4 | **Database CHECK `chk_modifier_groups_single_max`**: `selection_type <> 'single' OR max_select = 1`, next to the existing `chk_modifier_groups_bounds` (`min_select >= 0 AND max_select >= 1 AND min_select <= max_select`). | The service rule now also holds for rows written outside the API. |
| D5 | **Status semantics unchanged**: `is_available` = temporarily sold out (shown with a badge); `archived_at` = retired from the menu, kept for order history. | Already implemented in iteration 3. |

## 3. Migration 000015

1. Products with active variants but no default: the first variant (by `display_order`, `created_at`) becomes default.
2. Products with no active variant: a default `Reguler` variant at the legacy `base_price` is created.
3. `COMMENT ON COLUMN products.base_price` marks it deprecated.
4. Refuses to run while a single-choice group has `max_select <> 1`; then adds the CHECK.

Down removes the CHECK and the comment; backfilled variants are kept (valid catalog data that orders may
reference).

## 4. Consequences

- Positive: one price path in `pricing.go`; quote, POS order, tab round and (Iteration 5) QR order all
  price the same way; modifier bounds hold at the database.
- Negative: rows inserted directly with SQL must also insert a default variant (tests updated).
- Open for later: per-variant recipe consumption (K2/C5); dropping `products.base_price` once no reader
  remains.

## 5. Verification

- `pricing_test.go`: product without default variant is rejected even with a `base_price`.
- `internal/handler/catalog_adr004_integration_test.go`: order subtotal follows the variant when
  `base_price` differs; the CHECK rejects a single-choice group with `max_select = 2`.
