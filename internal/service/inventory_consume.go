package service

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/domain"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/repository/postgres"
)

// Ingredient deduction and cost of goods sold at payment (K2, PR 11.3). Recipes of the sold variants and of
// their modifier options (incl. sub-addons, X6) are summed per line; ingredient balances may go negative and
// never block a sale (X5); the line's cost of goods is stored now so later price changes do not move margins
// (X10). Ready-to-serve products add their own unit cost (X9).

type lineUsage struct {
	LineID       uuid.UUID `db:"line_id"`
	IngredientID uuid.UUID `db:"ingredient_id"`
	Units        int64     `db:"units"`
}

// consumeIngredientsTx runs once when an order becomes paid.
func consumeIngredientsTx(ctx context.Context, tx *sqlx.Tx, storeID, orderID uuid.UUID, now time.Time) error {
	var usage []lineUsage
	if err := tx.SelectContext(ctx, &usage, `
		SELECT line_id, ingredient_id, SUM(units)::bigint AS units FROM (
			SELECT oi.id AS line_id, r.ingredient_id, r.quantity * oi.quantity AS units
			FROM order_items oi JOIN recipe_items r ON r.store_id = oi.store_id AND r.variant_id = oi.variant_id
			WHERE oi.store_id = $1 AND oi.order_id = $2
			UNION ALL
			SELECT oi.id, r.ingredient_id, r.quantity * oio.quantity * oi.quantity
			FROM order_item_options oio
			JOIN order_items oi ON oi.store_id = oio.store_id AND oi.id = oio.order_item_id
			JOIN recipe_items r ON r.store_id = oio.store_id AND r.modifier_option_id = oio.modifier_option_id
			WHERE oi.store_id = $1 AND oi.order_id = $2
		) u GROUP BY line_id, ingredient_id HAVING SUM(units) <> 0 ORDER BY ingredient_id, line_id`, storeID, orderID); err != nil {
		return fmt.Errorf("failed to compute ingredient usage: %w", err)
	}

	totals := map[uuid.UUID]int64{}
	var order []uuid.UUID // deterministic lock order
	for _, u := range usage {
		if _, seen := totals[u.IngredientID]; !seen {
			order = append(order, u.IngredientID)
		}
		totals[u.IngredientID] += u.Units
	}
	costs := make(map[uuid.UUID]int64, len(order))
	for _, id := range order {
		var row struct {
			Name   string `db:"name"`
			Before int64  `db:"before"`
			After  int64  `db:"stock_quantity"`
			Min    int64  `db:"min_stock"`
			Cost   int64  `db:"cost_per_1000"`
			Unit   string `db:"unit"`
		}
		if err := tx.GetContext(ctx, &row, `UPDATE ingredients SET stock_quantity = stock_quantity - $3, updated_at = $4
			WHERE store_id = $1 AND id = $2
			RETURNING name, stock_quantity + $3 AS before, stock_quantity, min_stock, cost_per_1000, unit`, storeID, id, totals[id], now); err != nil {
			return fmt.Errorf("failed to deduct ingredient: %w", err)
		}
		costs[id] = row.Cost
		if _, err := tx.ExecContext(ctx, `INSERT INTO ingredient_movements (store_id, ingredient_id, kind, quantity, cost_per_1000, order_id, created_at)
			VALUES ($1, $2, 'sale', $3, $4, $5, $6)`, storeID, id, -totals[id], row.Cost, orderID, now); err != nil {
			return fmt.Errorf("failed to record ingredient movement: %w", err)
		}
		if row.Before >= row.Min && row.After < row.Min {
			// X5: low or negative stock never blocks a sale; the owner is told once when it crosses the minimum.
			payload, _ := json.Marshal(map[string]any{"ingredient_id": id, "name": row.Name, "stock_quantity": row.After,
				"min_stock": row.Min, "unit": row.Unit})
			if err := postgres.NewOrderRepository().InsertOutboxEvent(ctx, tx, &domain.OutboxEvent{ID: uuid.New(), StoreID: storeID,
				EventType: "inventory.low_stock", AggregateType: "ingredient", AggregateID: id.String(), Payload: string(payload), CreatedAt: now}); err != nil {
				return fmt.Errorf("failed to queue low stock alert: %w", err)
			}
		}
	}

	lineCost := map[uuid.UUID]int64{}
	for _, u := range usage {
		lineCost[u.LineID] += u.Units * costs[u.IngredientID]
	}
	var lines []struct {
		ID       uuid.UUID `db:"id"`
		Quantity int64     `db:"quantity"`
		UnitCost int64     `db:"unit_cost"`
	}
	if err := tx.SelectContext(ctx, &lines, `SELECT oi.id, oi.quantity, COALESCE(p.unit_cost, 0) AS unit_cost
		FROM order_items oi LEFT JOIN products p ON p.store_id = oi.store_id AND p.id = oi.product_id
		WHERE oi.store_id = $1 AND oi.order_id = $2`, storeID, orderID); err != nil {
		return fmt.Errorf("failed to load order lines for cost: %w", err)
	}
	for _, l := range lines {
		cogs := roundHalfUpDiv(lineCost[l.ID], 1000) + l.UnitCost*l.Quantity
		if _, err := tx.ExecContext(ctx, `UPDATE order_items SET cogs_amount = $3 WHERE store_id = $1 AND id = $2`, storeID, l.ID, max(cogs, 0)); err != nil {
			return fmt.Errorf("failed to store line cost: %w", err)
		}
	}
	return nil
}

// roundHalfUpDiv divides a by b (b > 0) rounding half away from zero, in integers.
func roundHalfUpDiv(a, b int64) int64 {
	if a < 0 {
		return -roundHalfUpDiv(-a, b)
	}
	return (a + b/2) / b
}
