package service

import (
	"fmt"

	"github.com/google/uuid"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/domain"
)

// priceBundleLine prices one fixed-price bundle line (X4, PR 11.2). Each chosen component becomes its own
// order line (stock, recipes, KDS station, refunds and reports keep working per component). The bundle price
// times the quantity is allocated over the components in proportion to their normal variant prices, the
// rounding remainder going to the last component; paid add-ons of a component are charged on top.
// Promotions never apply to bundle lines (no stacking).
func priceBundleLine(storeID uuid.UUID, bundle domain.Product, req OrderItemInput, catalog CatalogSnapshot,
	productDeducts, optionDeducts map[uuid.UUID]int) ([]PricedOrderItem, error) {
	bundleVariant, err := resolveVariant(bundle, req.VariantID, catalog)
	if err != nil {
		return nil, err
	}
	slots := catalog.BundleSlots[bundle.ID]
	if len(slots) == 0 {
		return nil, fmt.Errorf("%w: bundle %q has no slots", ErrInvalidOrderInput, bundle.Name)
	}
	chosen := make(map[uuid.UUID]BundleChoiceInput, len(req.BundleItems))
	for _, c := range req.BundleItems {
		if _, dup := chosen[c.SlotID]; dup {
			return nil, fmt.Errorf("%w: bundle %q has two choices for one slot", ErrInvalidOrderInput, bundle.Name)
		}
		chosen[c.SlotID] = c
	}
	if len(chosen) != len(slots) {
		return nil, fmt.Errorf("%w: bundle %q needs exactly one choice per slot", ErrInvalidOrderInput, bundle.Name)
	}

	lines := make([]PricedOrderItem, 0, len(slots))
	weights := make([]int64, 0, len(slots))
	var totalWeight int64
	for _, slot := range slots {
		c, ok := chosen[slot.ID]
		if !ok {
			return nil, fmt.Errorf("%w: bundle %q misses slot %q", ErrInvalidOrderInput, bundle.Name, slot.Name)
		}
		if !slot.ProductIDs.Contains(c.ProductID) {
			return nil, fmt.Errorf("%w: product %s is not a choice of slot %q", ErrInvalidOrderInput, c.ProductID, slot.Name)
		}
		p, ok := catalog.Products[c.ProductID]
		if !ok || p.StoreID != storeID || p.ProductType == domain.ProductTypeBundle {
			return nil, fmt.Errorf("%w: product %s", ErrProductNotFound, c.ProductID)
		}
		if !p.IsAvailable {
			return nil, fmt.Errorf("%w: product %q is unavailable", ErrProductUnavailable, p.Name)
		}
		units := req.Quantity * slot.Quantity
		if p.TrackStock {
			productDeducts[p.ID] += units
			if productDeducts[p.ID] > p.StockQuantity {
				return nil, fmt.Errorf("%w: product %q requested %d, available %d", ErrInsufficientStock, p.Name, productDeducts[p.ID], p.StockQuantity)
			}
		}
		variant, err := resolveVariant(p, c.VariantID, catalog)
		if err != nil {
			return nil, err
		}
		priced, err := priceSingleOrderItem(storeID, p, variant, OrderItemInput{ProductID: p.ID, VariantID: c.VariantID, Quantity: units,
			Modifiers: c.Modifiers, Notes: c.Notes}, catalog, optionDeducts)
		if err != nil {
			return nil, err
		}
		w := variant.Price * int64(slot.Quantity)
		weights = append(weights, w)
		totalWeight += w
		lines = append(lines, *priced)
	}

	bundleTotal := bundleVariant.Price * int64(req.Quantity)
	group, bundleID, name := uuid.New(), bundle.ID, bundle.Name
	var allocated int64
	for i := range lines {
		share := bundleTotal - allocated // the last component takes the rounding remainder
		if i < len(lines)-1 {
			if totalWeight > 0 {
				share = bundleTotal * weights[i] / totalWeight
			} else {
				share = bundleTotal / int64(len(lines))
			}
		}
		allocated += share
		it := &lines[i].Item
		it.SubtotalAmount = share + it.ModifierTotalPrice*int64(it.Quantity)
		it.SnapshotPrice = it.SubtotalAmount / int64(it.Quantity)
		it.BundleProductID, it.BundleGroupID, it.SnapshotBundleName = &bundleID, &group, &name
	}
	return lines, nil
}
