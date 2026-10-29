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

	"github.com/google/uuid"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/domain"
)

var (
	ErrStoreInactive            = errors.New("store is inactive or not found")
	ErrInvalidOrderInput        = errors.New("invalid order input")
	ErrProductNotFound          = errors.New("product not found in store catalog")
	ErrProductUnavailable       = errors.New("product is currently unavailable")
	ErrInvalidModifierSelection = errors.New("invalid modifier selection for product")
	ErrInvalidSubAddonSelection = errors.New("invalid nested sub-addon selection for modifier option")
	ErrInsufficientStock        = errors.New("insufficient stock for requested item or modifier")
	ErrIntegerOverflow          = errors.New("integer overflow during financial calculation")
	ErrIdempotencyConflict      = errors.New("idempotency_key already used with a different order payload")
	ErrCrossTenantAccess        = errors.New("store not found or cross-tenant access denied")
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
	Quantity  int                     `json:"quantity"`
	Modifiers []SelectedModifierInput `json:"modifiers,omitempty"`
	Notes     string                  `json:"notes,omitempty"`
}

type OrderCreationInput struct {
	IdempotencyKey string             `json:"idempotency_key,omitempty"`
	TableNumber    string             `json:"table_number,omitempty"`
	CustomerName   string             `json:"customer_name"`
	CustomerPhone  string             `json:"customer_phone,omitempty"`
	OrderType      domain.OrderType   `json:"order_type"`
	BillingMode    domain.BillingMode `json:"billing_mode,omitempty"`
	Notes          string             `json:"notes,omitempty"`
	Items          []OrderItemInput   `json:"items"`
}

type CatalogSnapshot struct {
	Products          map[uuid.UUID]domain.Product
	ModifierGroups    map[uuid.UUID]domain.ModifierGroup
	ModifierOptions   map[uuid.UUID]domain.ModifierOption
	ProductGroups     map[uuid.UUID][]domain.ProductModifierGroup
	OptionChildGroups map[uuid.UUID][]domain.OptionModifierGroup
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
	if len(input.IdempotencyKey) > 100 {
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
		if product.BasePrice < 0 {
			return nil, fmt.Errorf("%w: product %q has negative base_price", ErrInvalidOrderInput, product.Name)
		}

		if product.TrackStock {
			productDeducts[product.ID] += reqItem.Quantity
			if productDeducts[product.ID] > product.StockQuantity {
				return nil, fmt.Errorf("%w: product %q requested %d, available %d",
					ErrInsufficientStock, product.Name, productDeducts[product.ID], product.StockQuantity)
			}
		}

		pricedItem, err := priceSingleOrderItem(store.ID, product, reqItem, catalog, optionDeducts)
		if err != nil {
			return nil, err
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
	reqItem OrderItemInput,
	catalog CatalogSnapshot,
	optionDeducts map[uuid.UUID]int,
) (*PricedOrderItem, error) {
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
			storeID, opt, modReq.SubAddons, reqItem.Quantity, catalog, optionDeducts,
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

	unitSnapshotPrice, err := safeAddInt64(product.BasePrice, modifierUnitTotal)
	if err != nil {
		return nil, err
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

	return &PricedOrderItem{
		Item: domain.OrderItem{
			StoreID:            storeID,
			ProductID:          &prodIDCopy,
			ConfigHash:         cfgHash,
			SnapshotName:       product.Name,
			UnitBasePrice:      product.BasePrice,
			ModifierTotalPrice: modifierUnitTotal,
			SnapshotPrice:      unitSnapshotPrice,
			Quantity:           reqItem.Quantity,
			SubtotalAmount:     lineSubtotal,
			SelectedOptions:    string(selectedOptionsBytes),
			Notes:              strings.TrimSpace(reqItem.Notes),
		},
		Options: pricedOptions,
	}, nil
}

func validateAndPriceSubAddons(
	storeID uuid.UUID,
	parentOption domain.ModifierOption,
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

		childGroupCounts[childGrp.ID] += subReq.Quantity

		if subOpt.TrackStock {
			totalSubNeeded, err := safeMulInt64(int64(subReq.Quantity), int64(itemQuantity))
			if err != nil {
				return nil, nil, 0, err
			}
			optionDeducts[subOpt.ID] += int(totalSubNeeded)
			if optionDeducts[subOpt.ID] > subOpt.StockQuantity {
				return nil, nil, 0, fmt.Errorf("%w: sub-addon %q requested %d, available %d",
					ErrInsufficientStock, subOpt.Name, optionDeducts[subOpt.ID], subOpt.StockQuantity)
			}
		}

		subCost, err := safeMulInt64(subOpt.PriceDelta, int64(subReq.Quantity))
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
			Quantity:           subReq.Quantity,
		})

		childrenJSON = append(childrenJSON, selectedOptionSnapshotJSON{
			GroupID:    childGrp.ID,
			GroupName:  childGrp.Name,
			OptionID:   subOpt.ID,
			OptionName: subOpt.Name,
			PriceDelta: subOpt.PriceDelta,
			Quantity:   subReq.Quantity,
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
	fmt.Fprintf(&b, "store=%s|type=%s|billing=%s|table=%s|name=%s|phone=%s|notes=%s",
		storeID.String(),
		input.OrderType,
		billingMode,
		strings.TrimSpace(input.TableNumber),
		strings.TrimSpace(input.CustomerName),
		strings.TrimSpace(input.CustomerPhone),
		strings.TrimSpace(input.Notes),
	)
	for _, item := range input.Items {
		cfgHash := computeItemConfigHashFromModifiers(item.ProductID, item.Modifiers)
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
