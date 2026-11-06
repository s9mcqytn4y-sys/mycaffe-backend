package service

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/domain"
)

var errUnknownCatalogVersion = errors.New("catalog_version tidak dikenal server")

// offlinePricing re-prices an offline order from the catalog snapshot the device sold it with (ADR-005
// OF3, W20): prices, tax and service come from the snapshot; items missing from it are unknown. Today's
// availability does not apply because the goods were already handed over. Stock is still checked until
// negative stock arrives with PR 10.3.
type offlinePricing struct {
	snap         catalogSnapshot
	liveSubtotal *int64 // today's subtotal for the same items, for price_delta; nil when today's catalog refuses them
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
	for id, prod := range cat.Products {
		if !products[id] {
			delete(cat.Products, id)
			continue
		}
		prod.IsAvailable = true
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
		o.PriceDelta, o.IsAvailable = delta, true
		cat.ModifierOptions[id] = o
	}
	return nil
}
