package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/domain"
)

var (
	ErrStoreInactive             = errors.New("store is inactive or not found")
	ErrInvalidOrderInput         = errors.New("invalid order input")
	ErrProductNotFound           = errors.New("product not found in store catalog")
	ErrProductUnavailable        = errors.New("product is currently unavailable")
	ErrInvalidModifierSelection  = errors.New("invalid modifier selection for product")
	ErrInvalidSubAddonSelection  = errors.New("invalid nested sub-addon selection for modifier option")
	ErrInsufficientStock         = errors.New("insufficient stock for requested item or modifier")
	ErrIntegerOverflow           = errors.New("integer overflow during financial calculation")
	ErrIdempotencyConflict       = errors.New("idempotency_key already used with a different order payload or unverifiable legacy hash")
	ErrCrossTenantAccess         = errors.New("store not found or cross-tenant access denied")
	ErrUnsupportedBillingMode    = errors.New("split_bill billing mode is not enabled for direct order creation")
	ErrUnauthorizedBillingMode   = errors.New("open_tab billing mode requires an authorized staff user")
	ErrOrderAlreadyPaid          = errors.New("order is already paid and cannot be expired or cancelled without refund workflow")
	ErrLatePaymentStockExhausted = errors.New("late payment arrived after reservation release and stock is exhausted")
)

type SelectedSubAddonInput struct {
	OptionID uuid.UUID `json:"option_id"`
	Quantity int       `json:"quantity"`
}

type SelectedModifierInput struct {
	OptionID  uuid.UUID               `json:"option_id"`
	Quantity  int                     `json:"quantity"`
	SubAddons []SelectedSubAddonInput `json:"sub_addons,omitempty"`
}

type OrderItemInput struct {
	ProductID uuid.UUID               `json:"product_id"`
	VariantID *uuid.UUID              `json:"variant_id,omitempty"`
	Quantity  int                     `json:"quantity"`
	Modifiers []SelectedModifierInput `json:"modifiers,omitempty"`
	Notes     string                  `json:"notes,omitempty"`
	// BundleItems choose one product per slot when ProductID is a bundle (X4, PR 11.2).
	BundleItems []BundleChoiceInput `json:"bundle_items,omitempty"`
}

// BundleChoiceInput is the product chosen for one bundle slot, with its own variant, add-ons and notes.
type BundleChoiceInput struct {
	SlotID    uuid.UUID               `json:"slot_id"`
	ProductID uuid.UUID               `json:"product_id"`
	VariantID *uuid.UUID              `json:"variant_id,omitempty"`
	Modifiers []SelectedModifierInput `json:"modifiers,omitempty"`
	Notes     string                  `json:"notes,omitempty"`
}

type OrderCreationInput struct {
	IdempotencyKey           string             `json:"idempotency_key,omitempty"`
	TableNumber              string             `json:"table_number,omitempty"`
	CustomerName             string             `json:"customer_name"`
	CustomerPhone            string             `json:"customer_phone,omitempty"`
	OrderType                domain.OrderType   `json:"order_type"`
	BillingMode              domain.BillingMode `json:"billing_mode,omitempty"`
	ActorUserID              *uuid.UUID         `json:"actor_user_id,omitempty"`
	ExpectedTotal            *int64             `json:"expected_total,omitempty"`
	ExpectedQuoteFingerprint string             `json:"expected_quote_fingerprint,omitempty"`
	Notes                    string             `json:"notes,omitempty"`
	Items                    []OrderItemInput   `json:"items"`
	// SalesChannelID prices a delivery-app order at its channel prices (X12-X15); POS only.
	SalesChannelID *uuid.UUID `json:"sales_channel_id,omitempty"`
	// CustomerID links a verified customer for the stamp card; RewardItemIndex makes one unit of that item
	// the stamp card reward (X16, X17). POS only.
	CustomerID      *uuid.UUID `json:"customer_id,omitempty"`
	RewardItemIndex *int       `json:"reward_item_index,omitempty"`
	// Guest binds a QR order to its table, table session and guest session (iteration 5b); never from JSON.
	Guest *GuestOrderBinding `json:"-"`
	// Offline re-prices a synced offline order from its catalog snapshot (ADR-005 OF3); never from JSON.
	Offline *offlinePricing `json:"-"`
}

type CatalogSnapshot struct {
	Products          map[uuid.UUID]domain.Product
	ModifierGroups    map[uuid.UUID]domain.ModifierGroup
	ModifierOptions   map[uuid.UUID]domain.ModifierOption
	ProductGroups     map[uuid.UUID][]domain.ProductModifierGroup
	OptionChildGroups map[uuid.UUID][]domain.OptionModifierGroup
	Variants          map[uuid.UUID]domain.ProductVariant
	// Promotions run at PromoAt (K7, PR 11.1); IgnorePromoQuota replays an offline order at its own price (X3).
	BundleSlots      map[uuid.UUID][]domain.BundleSlot
	Promotions       []domain.Promotion
	PromoAt          time.Time
	IgnorePromoQuota bool
	// RewardItem is the index of the item whose one unit is free as a stamp card reward (X17).
	RewardItem *int
}

type PricedItemOption struct {
	Option   domain.OrderItemOption
	Children []domain.OrderItemOption
}

type PricedOrderItem struct {
	Item    domain.OrderItem
	Options []PricedItemOption
}

type PricedOrderResult struct {
	RequestHash         string
	SubtotalAmount      int64
	DiscountAmount      int64
	ServiceChargeAmount int64
	TaxAmount           int64
	TotalAmount         int64
	Items               []PricedOrderItem
	ProductStockDeducts map[uuid.UUID]int
	OptionStockDeducts  map[uuid.UUID]int
}

type selectedOptionSnapshotJSON struct {
	GroupID    uuid.UUID                    `json:"group_id"`
	GroupName  string                       `json:"group_name"`
	OptionID   uuid.UUID                    `json:"option_id"`
	OptionName string                       `json:"option_name"`
	PriceDelta int64                        `json:"price_delta"`
	Quantity   int                          `json:"quantity"`
	SubAddons  []selectedOptionSnapshotJSON `json:"sub_addons,omitempty"`
}

// CalculateRoundHalfUpBPS computes floor((amount * bps + 5000) / 10000) using integer arithmetic.
func CalculateRoundHalfUpBPS(amount, bps int64) (int64, error) {
	if amount < 0 || bps < 0 || bps > 10000 {
		return 0, fmt.Errorf("%w: invalid bps inputs amount=%d bps=%d", ErrInvalidOrderInput, amount, bps)
	}
	if amount == 0 || bps == 0 {
		return 0, nil
	}
	prod, err := safeMulInt64(amount, bps)
	if err != nil {
		return 0, err
	}
	withHalf, err := safeAddInt64(prod, 5000)
	if err != nil {
		return 0, err
	}
	return withHalf / 10000, nil
}

// ValidateAndPriceOrder validates all requested items, modifiers, 1-level sub-addons, and stock
// against the server-side CatalogSnapshot and calculates exact integer Rupiah totals.
func ValidateAndPriceOrder(store domain.Store, input OrderCreationInput, catalog CatalogSnapshot) (*PricedOrderResult, error) {
	if !store.IsActive || store.DeletedAt != nil {
		return nil, ErrStoreInactive
	}
	if strings.TrimSpace(input.CustomerName) == "" {
		return nil, fmt.Errorf("%w: customer_name is required", ErrInvalidOrderInput)
	}
	if input.OrderType != domain.OrderTypeDineIn && input.OrderType != domain.OrderTypeTakeaway {
		return nil, fmt.Errorf("%w: unsupported order_type %q", ErrInvalidOrderInput, input.OrderType)
	}
	billingMode := input.BillingMode
	if billingMode == "" {
		billingMode = store.DefaultBillingMode
		if billingMode == "" {
			billingMode = domain.BillingModePayFirst
		}
	}
	if billingMode != domain.BillingModePayFirst && billingMode != domain.BillingModeOpenTab && billingMode != domain.BillingModeSplitBill {
		return nil, fmt.Errorf("%w: unsupported billing_mode %q", ErrInvalidOrderInput, billingMode)
	}
	if billingMode == domain.BillingModeSplitBill {
		return nil, ErrUnsupportedBillingMode
	}
	if billingMode == domain.BillingModeOpenTab && (input.ActorUserID == nil || *input.ActorUserID == uuid.Nil) {
		return nil, ErrUnauthorizedBillingMode
	}
	trimmedIdemKey := strings.TrimSpace(input.IdempotencyKey)
	if trimmedIdemKey == "" {
		return nil, fmt.Errorf("%w: idempotency_key is required", ErrInvalidOrderInput)
	}
	if len(trimmedIdemKey) > 100 {
		return nil, fmt.Errorf("%w: idempotency_key exceeds 100 characters", ErrInvalidOrderInput)
	}
	if len(input.Items) == 0 {
		return nil, fmt.Errorf("%w: order must contain at least one item", ErrInvalidOrderInput)
	}

	productDeducts := make(map[uuid.UUID]int)
	optionDeducts := make(map[uuid.UUID]int)
	pricedItems := make([]PricedOrderItem, 0, len(input.Items))
	var orderSubtotal int64

	for itemIdx, reqItem := range input.Items {
		if reqItem.ProductID == uuid.Nil {
			return nil, fmt.Errorf("%w: item #%d has nil product_id", ErrInvalidOrderInput, itemIdx+1)
		}
		if reqItem.Quantity <= 0 || reqItem.Quantity > 1000 {
			return nil, fmt.Errorf("%w: item #%d quantity (%d) must be between 1 and 1000", ErrInvalidOrderInput, itemIdx+1, reqItem.Quantity)
		}

		product, exists := catalog.Products[reqItem.ProductID]
		if !exists || product.StoreID != store.ID {
			return nil, fmt.Errorf("%w: product %s", ErrProductNotFound, reqItem.ProductID)
		}
		if !product.IsAvailable {
			return nil, fmt.Errorf("%w: product %q is unavailable", ErrProductUnavailable, product.Name)
		}
		if product.ProductType == domain.ProductTypeBundle {
			if catalog.RewardItem != nil && *catalog.RewardItem == itemIdx {
				return nil, fmt.Errorf("%w: a bundle cannot be a stamp card reward", ErrInvalidOrderInput)
			}
			lines, err := priceBundleLine(store.ID, product, reqItem, catalog, productDeducts, optionDeducts)
			if err != nil {
				return nil, err
			}
			for _, l := range lines {
				if orderSubtotal, err = safeAddInt64(orderSubtotal, l.Item.SubtotalAmount); err != nil {
					return nil, err
				}
			}
			pricedItems = append(pricedItems, lines...)
			continue
		}
		if len(reqItem.BundleItems) > 0 {
			return nil, fmt.Errorf("%w: bundle_items are only for bundle products", ErrInvalidOrderInput)
		}
		if product.TrackStock {
			productDeducts[product.ID] += reqItem.Quantity
			if productDeducts[product.ID] > product.StockQuantity {
				return nil, fmt.Errorf("%w: product %q requested %d, available %d",
					ErrInsufficientStock, product.Name, productDeducts[product.ID], product.StockQuantity)
			}
		}

		variant, err := resolveVariant(product, reqItem.VariantID, catalog)
		if err != nil {
			return nil, err
		}
		pricedItem, err := priceSingleOrderItem(store.ID, product, variant, reqItem, catalog, optionDeducts)
		if err != nil {
			return nil, err
		}
		if catalog.RewardItem != nil && *catalog.RewardItem == itemIdx {
			// Stamp card reward: one unit free, in place of any promotion (X17).
			name := stampRewardName
			pricedItem.Item.SubtotalAmount -= pricedItem.Item.SnapshotPrice
			pricedItem.Item.PromoDiscountAmount, pricedItem.Item.SnapshotPromoName = pricedItem.Item.SnapshotPrice, &name
		} else if p, d := bestPromotion(catalog.Promotions, product, pricedItem.Item.SubtotalAmount, reqItem.Quantity,
			storeLocal(catalog.PromoAt, store.Timezone), catalog.IgnorePromoQuota); p != nil {
			name := p.Name
			pricedItem.Item.SubtotalAmount -= d
			pricedItem.Item.PromotionID, pricedItem.Item.PromoDiscountAmount, pricedItem.Item.SnapshotPromoName = &p.ID, d, &name
		}

		orderSubtotal, err = safeAddInt64(orderSubtotal, pricedItem.Item.SubtotalAmount)
		if err != nil {
			return nil, err
		}
		pricedItems = append(pricedItems, *pricedItem)
	}

	var discountAmount int64 = 0
	netSubtotal := orderSubtotal - discountAmount

	serviceChargeAmount, err := CalculateRoundHalfUpBPS(netSubtotal, store.ServiceBps)
	if err != nil {
		return nil, err
	}

	taxableBase, err := safeAddInt64(netSubtotal, serviceChargeAmount)
	if err != nil {
		return nil, err
	}

	taxAmount, err := CalculateRoundHalfUpBPS(taxableBase, store.TaxBps)
	if err != nil {
		return nil, err
	}

	totalAmount, err := safeAddInt64(taxableBase, taxAmount)
	if err != nil {
		return nil, err
	}

	reqHash := ComputeInputRequestHash(store.ID, billingMode, input)

	return &PricedOrderResult{
		RequestHash:         reqHash,
		SubtotalAmount:      orderSubtotal,
		DiscountAmount:      discountAmount,
		ServiceChargeAmount: serviceChargeAmount,
		TaxAmount:           taxAmount,
		TotalAmount:         totalAmount,
		Items:               pricedItems,
		ProductStockDeducts: productDeducts,
		OptionStockDeducts:  optionDeducts,
	}, nil
}

func priceSingleOrderItem(
	storeID uuid.UUID,
	product domain.Product,
	variant *domain.ProductVariant,
	reqItem OrderItemInput,
	catalog CatalogSnapshot,
	optionDeducts map[uuid.UUID]int,
) (*PricedOrderItem, error) {
	unitBase := variant.Price // ADR-004: the variant is the only price source; products.base_price is a legacy mirror.
	attachedLinks := catalog.ProductGroups[product.ID]
	allowedGroups := make(map[uuid.UUID]domain.ProductModifierGroup, len(attachedLinks))
	for _, link := range attachedLinks {
		allowedGroups[link.GroupID] = link
	}

	groupSelectedCount := make(map[uuid.UUID]int)
	seenParentOptions := make(map[uuid.UUID]bool)
	pricedOptions := make([]PricedItemOption, 0, len(reqItem.Modifiers))
	jsonSnapshots := make([]selectedOptionSnapshotJSON, 0, len(reqItem.Modifiers))
	var modifierUnitTotal int64

	for _, modReq := range reqItem.Modifiers {
		if modReq.OptionID == uuid.Nil {
			return nil, fmt.Errorf("%w: modifier option_id cannot be nil", ErrInvalidModifierSelection)
		}
		if modReq.Quantity <= 0 {
			return nil, fmt.Errorf("%w: modifier quantity must be positive", ErrInvalidModifierSelection)
		}
		if seenParentOptions[modReq.OptionID] {
			return nil, fmt.Errorf("%w: duplicate modifier option_id %s on same item", ErrInvalidModifierSelection, modReq.OptionID)
		}
		seenParentOptions[modReq.OptionID] = true

		opt, exists := catalog.ModifierOptions[modReq.OptionID]
		if !exists || opt.StoreID != storeID {
			return nil, fmt.Errorf("%w: option %s not found", ErrInvalidModifierSelection, modReq.OptionID)
		}
		if !opt.IsAvailable {
			return nil, fmt.Errorf("%w: option %q is unavailable", ErrInvalidModifierSelection, opt.Name)
		}
		if modReq.Quantity > opt.MaxQuantity {
			return nil, fmt.Errorf("%w: option %q quantity %d exceeds max_quantity %d",
				ErrInvalidModifierSelection, opt.Name, modReq.Quantity, opt.MaxQuantity)
		}

		if _, allowed := allowedGroups[opt.GroupID]; !allowed {
			return nil, fmt.Errorf("%w: option %q belongs to group %s not attached to product %q",
				ErrInvalidModifierSelection, opt.Name, opt.GroupID, product.Name)
		}

		grp, grpExists := catalog.ModifierGroups[opt.GroupID]
		if !grpExists || !grp.IsActive || grp.StoreID != storeID {
			return nil, fmt.Errorf("%w: modifier group %s is inactive or missing", ErrInvalidModifierSelection, opt.GroupID)
		}
		if grp.SelectionType == "single" && modReq.Quantity > 1 {
			return nil, fmt.Errorf("%w: single-selection group %q does not allow option quantity > 1 (got %d)",
				ErrInvalidModifierSelection, grp.Name, modReq.Quantity)
		}

		groupSelectedCount[grp.ID] += modReq.Quantity

		if opt.TrackStock {
			totalOptNeeded, err := safeMulInt64(int64(modReq.Quantity), int64(reqItem.Quantity))
			if err != nil {
				return nil, err
			}
			optionDeducts[opt.ID] += int(totalOptNeeded)
			if optionDeducts[opt.ID] > opt.StockQuantity {
				return nil, fmt.Errorf("%w: modifier %q requested %d, available %d",
					ErrInsufficientStock, opt.Name, optionDeducts[opt.ID], opt.StockQuantity)
			}
		}

		optCost, err := safeMulInt64(opt.PriceDelta, int64(modReq.Quantity))
		if err != nil {
			return nil, err
		}
		modifierUnitTotal, err = safeAddInt64(modifierUnitTotal, optCost)
		if err != nil {
			return nil, err
		}

		childrenPriced, childrenJSON, subCost, err := validateAndPriceSubAddons(
			storeID, opt, modReq.Quantity, modReq.SubAddons, reqItem.Quantity, catalog, optionDeducts,
		)
		if err != nil {
			return nil, err
		}
		modifierUnitTotal, err = safeAddInt64(modifierUnitTotal, subCost)
		if err != nil {
			return nil, err
		}

		grpIDCopy := grp.ID
		optIDCopy := opt.ID
		pricedOptions = append(pricedOptions, PricedItemOption{
			Option: domain.OrderItemOption{
				StoreID:            storeID,
				ModifierGroupID:    &grpIDCopy,
				ModifierOptionID:   &optIDCopy,
				SnapshotGroupName:  grp.Name,
				SnapshotOptionName: opt.Name,
				SnapshotPriceDelta: opt.PriceDelta,
				Quantity:           modReq.Quantity,
			},
			Children: childrenPriced,
		})

		jsonSnapshots = append(jsonSnapshots, selectedOptionSnapshotJSON{
			GroupID:    grp.ID,
			GroupName:  grp.Name,
			OptionID:   opt.ID,
			OptionName: opt.Name,
			PriceDelta: opt.PriceDelta,
			Quantity:   modReq.Quantity,
			SubAddons:  childrenJSON,
		})
	}

	for _, link := range attachedLinks {
		grp, exists := catalog.ModifierGroups[link.GroupID]
		if !exists || !grp.IsActive {
			continue
		}
		isReq := grp.IsRequired
		if link.OverrideRequired != nil {
			isReq = *link.OverrideRequired
		}
		minSelect := grp.MinSelect
		if isReq && minSelect < 1 {
			minSelect = 1
		}
		maxSelect := grp.MaxSelect
		if grp.SelectionType == "single" && maxSelect > 1 {
			maxSelect = 1
		}

		count := groupSelectedCount[grp.ID]
		if count < minSelect {
			return nil, fmt.Errorf("%w: group %q requires at least %d selection(s), got %d",
				ErrInvalidModifierSelection, grp.Name, minSelect, count)
		}
		if count > maxSelect {
			return nil, fmt.Errorf("%w: group %q allows at most %d selection(s), got %d",
				ErrInvalidModifierSelection, grp.Name, maxSelect, count)
		}
	}

	unitSnapshotPrice, err := safeAddInt64(unitBase, modifierUnitTotal)
	if err != nil {
		return nil, err
	}
	if unitSnapshotPrice < 0 {
		// Negative modifier deltas are allowed (owner decision P1) but never below a free item.
		return nil, fmt.Errorf("%w: item %q would cost %d after modifiers; the unit price cannot be negative",
			ErrInvalidModifierSelection, product.Name, unitSnapshotPrice)
	}
	lineSubtotal, err := safeMulInt64(unitSnapshotPrice, int64(reqItem.Quantity))
	if err != nil {
		return nil, err
	}

	selectedOptionsBytes, err := json.Marshal(jsonSnapshots)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal selected_options snapshot: %w", err)
	}

	cfgHash := computeItemConfigHash(product.ID, jsonSnapshots)
	prodIDCopy := product.ID
	var variantID *uuid.UUID
	var variantName, sku string
	id := variant.ID
	variantID, variantName, sku = &id, variant.Name, variant.SKU
	// Distinct variants of the same product must not share a line configuration.
	sum := sha256.Sum256([]byte(cfgHash + "|variant=" + id.String()))
	cfgHash = hex.EncodeToString(sum[:])

	return &PricedOrderItem{
		Item: domain.OrderItem{
			StoreID:             storeID,
			ProductID:           &prodIDCopy,
			ConfigHash:          cfgHash,
			SnapshotName:        product.Name,
			VariantID:           variantID,
			SnapshotVariant:     variantName,
			SnapshotSKU:         sku,
			SnapshotPrepStation: product.PrepStation,
			UnitBasePrice:       unitBase,
			ModifierTotalPrice:  modifierUnitTotal,
			SnapshotPrice:       unitSnapshotPrice,
			Quantity:            reqItem.Quantity,
			SubtotalAmount:      lineSubtotal,
			SelectedOptions:     string(selectedOptionsBytes),
			Notes:               strings.TrimSpace(reqItem.Notes),
		},
		Options: pricedOptions,
	}, nil
}

func validateAndPriceSubAddons(
	storeID uuid.UUID,
	parentOption domain.ModifierOption,
	parentOptionQuantity int,
	subAddons []SelectedSubAddonInput,
	itemQuantity int,
	catalog CatalogSnapshot,
	optionDeducts map[uuid.UUID]int,
) ([]domain.OrderItemOption, []selectedOptionSnapshotJSON, int64, error) {
	childLinks := catalog.OptionChildGroups[parentOption.ID]
	allowedChildGroups := make(map[uuid.UUID]domain.OptionModifierGroup, len(childLinks))
	for _, link := range childLinks {
		allowedChildGroups[link.ChildGroupID] = link
	}

	if len(subAddons) > 0 && len(allowedChildGroups) == 0 {
		return nil, nil, 0, fmt.Errorf("%w: option %q does not accept sub-addons", ErrInvalidSubAddonSelection, parentOption.Name)
	}

	childGroupCounts := make(map[uuid.UUID]int)
	seenSubOptions := make(map[uuid.UUID]bool)
	var children []domain.OrderItemOption
	var childrenJSON []selectedOptionSnapshotJSON
	var subUnitTotal int64

	for _, subReq := range subAddons {
		if subReq.OptionID == uuid.Nil || subReq.Quantity <= 0 {
			return nil, nil, 0, fmt.Errorf("%w: invalid sub-addon option or quantity", ErrInvalidSubAddonSelection)
		}
		if seenSubOptions[subReq.OptionID] {
			return nil, nil, 0, fmt.Errorf("%w: duplicate sub-addon option %s", ErrInvalidSubAddonSelection, subReq.OptionID)
		}
		seenSubOptions[subReq.OptionID] = true

		subOpt, exists := catalog.ModifierOptions[subReq.OptionID]
		if !exists || subOpt.StoreID != storeID {
			return nil, nil, 0, fmt.Errorf("%w: sub-addon option %s not found", ErrInvalidSubAddonSelection, subReq.OptionID)
		}
		if !subOpt.IsAvailable {
			return nil, nil, 0, fmt.Errorf("%w: sub-addon option %q is unavailable", ErrInvalidSubAddonSelection, subOpt.Name)
		}
		if subReq.Quantity > subOpt.MaxQuantity {
			return nil, nil, 0, fmt.Errorf("%w: sub-addon %q quantity %d exceeds max_quantity %d",
				ErrInvalidSubAddonSelection, subOpt.Name, subReq.Quantity, subOpt.MaxQuantity)
		}

		if _, allowed := allowedChildGroups[subOpt.GroupID]; !allowed {
			return nil, nil, 0, fmt.Errorf("%w: sub-addon %q does not belong to a child group of %q",
				ErrInvalidSubAddonSelection, subOpt.Name, parentOption.Name)
		}

		childGrp, grpExists := catalog.ModifierGroups[subOpt.GroupID]
		if !grpExists || !childGrp.IsActive || childGrp.StoreID != storeID {
			return nil, nil, 0, fmt.Errorf("%w: child modifier group %s is inactive", ErrInvalidSubAddonSelection, subOpt.GroupID)
		}
		if childGrp.SelectionType == "single" && subReq.Quantity > 1 {
			return nil, nil, 0, fmt.Errorf("%w: single-selection child group %q does not allow sub-addon quantity > 1 (got %d)",
				ErrInvalidSubAddonSelection, childGrp.Name, subReq.Quantity)
		}

		childGroupCounts[childGrp.ID] += subReq.Quantity

		effectiveSubQtyPerItem, err := safeMulInt64(int64(subReq.Quantity), int64(parentOptionQuantity))
		if err != nil {
			return nil, nil, 0, err
		}

		if subOpt.TrackStock {
			totalSubNeeded, err := safeMulInt64(effectiveSubQtyPerItem, int64(itemQuantity))
			if err != nil {
				return nil, nil, 0, err
			}
			optionDeducts[subOpt.ID] += int(totalSubNeeded)
			if optionDeducts[subOpt.ID] > subOpt.StockQuantity {
				return nil, nil, 0, fmt.Errorf("%w: sub-addon %q requested %d, available %d",
					ErrInsufficientStock, subOpt.Name, optionDeducts[subOpt.ID], subOpt.StockQuantity)
			}
		}

		subCost, err := safeMulInt64(subOpt.PriceDelta, effectiveSubQtyPerItem)
		if err != nil {
			return nil, nil, 0, err
		}
		subUnitTotal, err = safeAddInt64(subUnitTotal, subCost)
		if err != nil {
			return nil, nil, 0, err
		}

		grpIDCopy := childGrp.ID
		subOptIDCopy := subOpt.ID
		parentOptIDCopy := parentOption.ID
		children = append(children, domain.OrderItemOption{
			StoreID:            storeID,
			ModifierGroupID:    &grpIDCopy,
			ModifierOptionID:   &subOptIDCopy,
			ParentOptionID:     &parentOptIDCopy,
			SnapshotGroupName:  childGrp.Name,
			SnapshotOptionName: subOpt.Name,
			SnapshotPriceDelta: subOpt.PriceDelta,
			Quantity:           int(effectiveSubQtyPerItem),
		})

		childrenJSON = append(childrenJSON, selectedOptionSnapshotJSON{
			GroupID:    childGrp.ID,
			GroupName:  childGrp.Name,
			OptionID:   subOpt.ID,
			OptionName: subOpt.Name,
			PriceDelta: subOpt.PriceDelta,
			Quantity:   int(effectiveSubQtyPerItem),
		})
	}

	for _, link := range childLinks {
		childGrp, exists := catalog.ModifierGroups[link.ChildGroupID]
		if !exists || !childGrp.IsActive {
			continue
		}
		minSelect := childGrp.MinSelect
		if childGrp.IsRequired && minSelect < 1 {
			minSelect = 1
		}
		maxSelect := childGrp.MaxSelect
		if childGrp.SelectionType == "single" && maxSelect > 1 {
			maxSelect = 1
		}
		count := childGroupCounts[childGrp.ID]
		if count < minSelect {
			return nil, nil, 0, fmt.Errorf("%w: child group %q of option %q requires at least %d selection(s), got %d",
				ErrInvalidSubAddonSelection, childGrp.Name, parentOption.Name, minSelect, count)
		}
		if count > maxSelect {
			return nil, nil, 0, fmt.Errorf("%w: child group %q of option %q allows at most %d selection(s), got %d",
				ErrInvalidSubAddonSelection, childGrp.Name, parentOption.Name, maxSelect, count)
		}
	}

	return children, childrenJSON, subUnitTotal, nil
}

func computeItemConfigHash(productID uuid.UUID, options []selectedOptionSnapshotJSON) string {
	parts := make([]string, 0, len(options)+1)
	parts = append(parts, productID.String())

	sortedOpts := make([]string, 0, len(options))
	for _, opt := range options {
		subParts := make([]string, 0, len(opt.SubAddons))
		for _, sub := range opt.SubAddons {
			subParts = append(subParts, fmt.Sprintf("%s:%d", sub.OptionID.String(), sub.Quantity))
		}
		sort.Strings(subParts)
		sortedOpts = append(sortedOpts, fmt.Sprintf("%s:%d[%s]", opt.OptionID.String(), opt.Quantity, strings.Join(subParts, ",")))
	}
	sort.Strings(sortedOpts)
	parts = append(parts, sortedOpts...)

	sum := sha256.Sum256([]byte(strings.Join(parts, "|")))
	return hex.EncodeToString(sum[:])
}

func computeItemConfigHashFromModifiers(productID uuid.UUID, modifiers []SelectedModifierInput) string {
	parts := make([]string, 0, len(modifiers)+1)
	parts = append(parts, productID.String())

	sortedOpts := make([]string, 0, len(modifiers))
	for _, opt := range modifiers {
		subParts := make([]string, 0, len(opt.SubAddons))
		for _, sub := range opt.SubAddons {
			subParts = append(subParts, fmt.Sprintf("%s:%d", sub.OptionID.String(), sub.Quantity))
		}
		sort.Strings(subParts)
		sortedOpts = append(sortedOpts, fmt.Sprintf("%s:%d[%s]", opt.OptionID.String(), opt.Quantity, strings.Join(subParts, ",")))
	}
	sort.Strings(sortedOpts)
	parts = append(parts, sortedOpts...)

	sum := sha256.Sum256([]byte(strings.Join(parts, "|")))
	return hex.EncodeToString(sum[:])
}

func ComputeInputRequestHash(
	storeID uuid.UUID,
	billingMode domain.BillingMode,
	input OrderCreationInput,
) string {
	var b strings.Builder
	actorStr := ""
	if input.ActorUserID != nil && *input.ActorUserID != uuid.Nil {
		actorStr = input.ActorUserID.String()
	}
	fmt.Fprintf(&b, "store=%s|type=%s|billing=%s|actor=%s|table=%s|name=%s|phone=%s|notes=%s",
		storeID.String(),
		input.OrderType,
		billingMode,
		actorStr,
		strings.TrimSpace(input.TableNumber),
		strings.TrimSpace(input.CustomerName),
		strings.TrimSpace(input.CustomerPhone),
		strings.TrimSpace(input.Notes),
	)
	if input.SalesChannelID != nil { // appended only when set so hashes of earlier orders stay valid
		fmt.Fprintf(&b, "|channel=%s", input.SalesChannelID.String())
	}
	if input.CustomerID != nil {
		fmt.Fprintf(&b, "|customer=%s", input.CustomerID.String())
	}
	if input.RewardItemIndex != nil {
		fmt.Fprintf(&b, "|reward=%d", *input.RewardItemIndex)
	}
	for _, item := range input.Items {
		cfgHash := computeItemConfigHashFromModifiers(item.ProductID, item.Modifiers)
		if item.VariantID != nil && *item.VariantID != uuid.Nil {
			cfgHash += ":v=" + item.VariantID.String()
		}
		for _, bc := range item.BundleItems {
			cfgHash += "|b=" + bc.SlotID.String() + ":" + computeItemConfigHashFromModifiers(bc.ProductID, bc.Modifiers) + ":" + strings.TrimSpace(bc.Notes)
			if bc.VariantID != nil {
				cfgHash += ":v=" + bc.VariantID.String()
			}
		}
		fmt.Fprintf(&b, "|item=%s:%d:%s:%s",
			item.ProductID.String(),
			item.Quantity,
			cfgHash,
			strings.TrimSpace(item.Notes),
		)
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

func safeAddInt64(a, b int64) (int64, error) {
	if (b > 0 && a > math.MaxInt64-b) || (b < 0 && a < math.MinInt64-b) {
		return 0, ErrIntegerOverflow
	}
	return a + b, nil
}

func safeMulInt64(a, b int64) (int64, error) {
	if a == 0 || b == 0 {
		return 0, nil
	}
	if a > 0 && b > 0 && a > math.MaxInt64/b {
		return 0, ErrIntegerOverflow
	}
	if a < 0 && b < 0 && a < math.MaxInt64/b {
		return 0, ErrIntegerOverflow
	}
	if (a > 0 && b < 0 && b < math.MinInt64/a) || (a < 0 && b > 0 && a < math.MinInt64/b) {
		return 0, ErrIntegerOverflow
	}
	return a * b, nil
}

// resolveVariant picks the requested variant, or the product's default variant when none is
// requested. A product without an available default variant cannot be sold (ADR-004, fail closed).
func resolveVariant(product domain.Product, requested *uuid.UUID, catalog CatalogSnapshot) (*domain.ProductVariant, error) {
	if requested != nil && *requested != uuid.Nil {
		v, ok := catalog.Variants[*requested]
		if !ok || v.ProductID != product.ID {
			return nil, fmt.Errorf("%w: variant %s does not belong to product %q", ErrInvalidOrderInput, *requested, product.Name)
		}
		if !v.IsAvailable {
			return nil, fmt.Errorf("%w: variant %q of %q is unavailable", ErrProductUnavailable, v.Name, product.Name)
		}
		return &v, nil
	}
	for _, v := range catalog.Variants {
		if v.ProductID == product.ID && v.IsDefault {
			if !v.IsAvailable {
				return nil, fmt.Errorf("%w: default variant of %q is unavailable", ErrProductUnavailable, product.Name)
			}
			return &v, nil
		}
	}
	return nil, fmt.Errorf("%w: product %q has no default variant", ErrProductUnavailable, product.Name)
}
