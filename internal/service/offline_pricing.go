package service

import (
	"context"
	"math"
	"time"

	"encoding/json"
	"errors"
	"fmt"
	"github.com/jmoiron/sqlx"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/repository/postgres"

	"github.com/google/uuid"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/domain"
)

var errUnknownCatalogVersion = errors.New("catalog_version tidak dikenal server")

// offlinePricing re-prices an offline order from the catalog snapshot the device sold it with (ADR-005
// OF3, W20): prices, tax and service come from the snapshot; items missing from it are unknown. Today's
// availability and stock do not apply because the goods were already handed over (OF5, W17): stock is
// deducted without the guard and lines that drove a balance below zero are flagged oversold.
type offlinePricing struct {
	snap                              catalogSnapshot
	liveSubtotal                      *int64 // today's subtotal for the same items, for price_delta; nil when today's catalog refuses them
	oversoldProducts, oversoldOptions []uuid.UUID
	at                                time.Time // when the device took the order: promotions and happy hours run at this time (X3)
}

func newOfflinePricing(raw []byte) (*offlinePricing, error) {
	p := &offlinePricing{}
	if err := json.Unmarshal(raw, &p.snap); err != nil {
		return nil, fmt.Errorf("stored catalog snapshot is unreadable: %w", err)
	}
	return p, nil
}

// apply records today's subtotal, then rewrites the loaded live catalog with the snapshot's prices.
func (p *offlinePricing) apply(store *domain.Store, cat *CatalogSnapshot, input OrderCreationInput) error {
	input.Offline = nil
	if live, err := ValidateAndPriceOrder(*store, input, *cat); err == nil {
		p.liveSubtotal = &live.SubtotalAmount
	}

	products, variants, options := map[uuid.UUID]bool{}, map[uuid.UUID]int64{}, map[uuid.UUID]int64{}
	var walk func([]snapshotGroup)
	walk = func(groups []snapshotGroup) {
		for _, g := range groups {
			for _, o := range g.Options {
				options[o.ID] = o.PriceDelta
				walk(o.SubGroups)
			}
		}
	}
	for _, sp := range p.snap.Products {
		products[sp.ID] = true
		for _, v := range sp.Variants {
			variants[v.ID] = v.Price
		}
		walk(sp.ModifierGroups)
	}

	store.TaxBps, store.ServiceBps = p.snap.TaxBPS, p.snap.ServiceBPS
	cat.Promotions = make([]domain.Promotion, len(p.snap.Promotions))
	for i, sp := range p.snap.Promotions {
		cat.Promotions[i] = sp.promotion()
	}
	cat.PromoAt, cat.IgnorePromoQuota = p.at, true
	for id, prod := range cat.Products {
		if !products[id] {
			delete(cat.Products, id)
			continue
		}
		prod.IsAvailable = true
		prod.StockQuantity = math.MaxInt32 // stock is never a reason to refuse offline (W17); deductStock records the real balance
		cat.Products[id] = prod
	}
	for id, v := range cat.Variants {
		price, ok := variants[id]
		if !ok {
			delete(cat.Variants, id)
			continue
		}
		v.Price, v.IsAvailable = price, true
		cat.Variants[id] = v
	}
	for id, o := range cat.ModifierOptions {
		delta, ok := options[id]
		if !ok {
			delete(cat.ModifierOptions, id)
			continue
		}
		o.PriceDelta, o.IsAvailable, o.StockQuantity = delta, true, math.MaxInt32
		cat.ModifierOptions[id] = o
	}
	return nil
}

// deductStock takes the order's tracked stock without the >= guard and remembers what went negative.
func (p *offlinePricing) deductStock(ctx context.Context, tx *sqlx.Tx, repo *postgres.OrderRepository, storeID uuid.UUID, priced *PricedOrderResult) error {
	for _, id := range sortedUUIDKeysFromIntMap(priced.ProductStockDeducts) {
		balance, ok, err := repo.DeductStockAllowNegative(ctx, tx, "products", storeID, id, priced.ProductStockDeducts[id])
		if err != nil {
			return err
		}
		if ok && balance < 0 {
			p.oversoldProducts = append(p.oversoldProducts, id)
		}
	}
	for _, id := range sortedUUIDKeysFromIntMap(priced.OptionStockDeducts) {
		balance, ok, err := repo.DeductStockAllowNegative(ctx, tx, "modifier_options", storeID, id, priced.OptionStockDeducts[id])
		if err != nil {
			return err
		}
		if ok && balance < 0 {
			p.oversoldOptions = append(p.oversoldOptions, id)
		}
	}
	return nil
}

// flagOversold marks the order's lines that sold an oversold product or modifier option.
func (p *offlinePricing) flagOversold(ctx context.Context, tx *sqlx.Tx, storeID, orderID uuid.UUID) error {
	for _, id := range p.oversoldProducts {
		if _, err := tx.ExecContext(ctx, `UPDATE order_items SET oversold = true WHERE store_id = $1 AND order_id = $2 AND product_id = $3`,
			storeID, orderID, id); err != nil {
			return err
		}
	}
	for _, id := range p.oversoldOptions {
		if _, err := tx.ExecContext(ctx, `UPDATE order_items i SET oversold = true WHERE i.store_id = $1 AND i.order_id = $2
			AND EXISTS (SELECT 1 FROM order_item_options o WHERE o.store_id = i.store_id AND o.order_item_id = i.id AND o.modifier_option_id = $3)`,
			storeID, orderID, id); err != nil {
			return err
		}
	}
	return nil
}
