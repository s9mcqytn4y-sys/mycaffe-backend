package service

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/s9mcqytn4y-sys/cassy-backend/internal/domain"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/repository/postgres"
)

// Promotion evaluation (K7, PR 11.1). Pure functions so the same rules price online orders, quotes, tab
// rounds and offline orders replayed from their snapshot (X3).

// promoRunsAt reports whether p is running at local (the store's wall clock).
func promoRunsAt(p domain.Promotion, local time.Time) bool {
	if !p.IsActive || (p.StartsAt != nil && local.Before(*p.StartsAt)) || (p.EndsAt != nil && !local.Before(*p.EndsAt)) {
		return false
	}
	if p.StartMinute == nil {
		return p.DaysMask&(1<<int(local.Weekday())) != 0
	}
	minute := local.Hour()*60 + local.Minute()
	start, end := *p.StartMinute, *p.EndMinute
	if start < end {
		return p.DaysMask&(1<<int(local.Weekday())) != 0 && minute >= start && minute < end
	}
	// Past midnight (e.g. 22:00-02:00): the early part belongs to the previous day's happy hour.
	if minute >= start {
		return p.DaysMask&(1<<int(local.Weekday())) != 0
	}
	return minute < end && p.DaysMask&(1<<int(local.Add(-24*time.Hour).Weekday())) != 0
}

// promoDiscount is what p takes off a line of qty units costing lineSubtotal, never more than the line.
func promoDiscount(p domain.Promotion, lineSubtotal int64, qty int) int64 {
	var d int64
	if p.Kind == domain.PromotionPercent {
		d, _ = CalculateRoundHalfUpBPS(lineSubtotal, p.Value)
	} else {
		d = p.Value * int64(qty)
	}
	return min(d, lineSubtotal)
}

// bestPromotion picks the single best running promotion for a product line (no stacking). A used-up quota
// excludes a promotion unless ignoreQuota (offline replay keeps the price the customer paid, X3).
func bestPromotion(promos []domain.Promotion, product domain.Product, lineSubtotal int64, qty int, local time.Time, ignoreQuota bool) (*domain.Promotion, int64) {
	var best *domain.Promotion
	var bestDiscount int64
	for i := range promos {
		p := &promos[i]
		if !p.ProductIDs.Contains(product.ID) && (product.CategoryID == nil || !p.CategoryIDs.Contains(*product.CategoryID)) {
			continue
		}
		if !ignoreQuota && p.Quota != nil && p.UsedCount >= *p.Quota {
			continue
		}
		if !promoRunsAt(*p, local) {
			continue
		}
		if d := promoDiscount(*p, lineSubtotal, qty); d > bestDiscount {
			best, bestDiscount = p, d
		}
	}
	return best, bestDiscount
}

// storeLocal converts t to the store's time zone (Asia/Jakarta when unknown).
func storeLocal(t time.Time, timezone string) time.Time {
	loc, err := time.LoadLocation(timezone)
	if err != nil || timezone == "" {
		loc = time.FixedZone("WIB", 7*3600) // same fallback as the order-number business day
	}
	return t.In(loc)
}

// catalogSnapshotOf turns loaded catalog data into the pricing input, with the promotions running now.
func catalogSnapshotOf(cd *postgres.CatalogData, now time.Time) CatalogSnapshot {
	return CatalogSnapshot{
		Products:          cd.Products,
		ModifierGroups:    cd.ModifierGroups,
		ModifierOptions:   cd.ModifierOptions,
		ProductGroups:     cd.ProductModifierGroups,
		OptionChildGroups: cd.OptionModifierGroups,
		Variants:          cd.Variants,
		BundleSlots:       cd.BundleSlots,
		Promotions:        cd.Promotions,
		PromoAt:           now,
	}
}

// consumePromotionsTx counts each promotion of a paid order once against its quota. An order whose promotion
// was already used up keeps its price and is flagged promo_over_quota for the owner (X3).
func consumePromotionsTx(ctx context.Context, tx *sqlx.Tx, storeID, orderID uuid.UUID) error {
	var ids []uuid.UUID
	if err := tx.SelectContext(ctx, &ids, `SELECT DISTINCT promotion_id FROM order_items
		WHERE store_id = $1 AND order_id = $2 AND promotion_id IS NOT NULL ORDER BY promotion_id`, storeID, orderID); err != nil {
		return fmt.Errorf("failed to read order promotions: %w", err)
	}
	over := false
	for _, id := range ids {
		var row struct {
			Quota *int `db:"quota"`
			Used  int  `db:"used_count"`
		}
		if err := tx.GetContext(ctx, &row, `UPDATE promotions SET used_count = used_count + 1, updated_at = NOW()
			WHERE store_id = $1 AND id = $2 RETURNING quota, used_count`, storeID, id); err != nil {
			return fmt.Errorf("failed to count promotion use: %w", err)
		}
		over = over || (row.Quota != nil && row.Used > *row.Quota)
	}
	if !over {
		return nil
	}
	_, err := tx.ExecContext(ctx, `UPDATE orders SET promo_over_quota = true WHERE store_id = $1 AND id = $2`, storeID, orderID)
	return err
}

// orderProductIDs lists the products an order needs loaded: its lines and the components chosen for bundles.
func orderProductIDs(items []OrderItemInput) []uuid.UUID {
	ids := make([]uuid.UUID, 0, len(items))
	for _, it := range items {
		ids = append(ids, it.ProductID)
		for _, bc := range it.BundleItems {
			ids = append(ids, bc.ProductID)
		}
	}
	return ids
}
