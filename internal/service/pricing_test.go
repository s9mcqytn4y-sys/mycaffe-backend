package service_test

import (
	"errors"
	"math"
	"testing"

	"github.com/google/uuid"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/domain"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/service"
)

func TestValidateAndPriceOrder_PurePricingAndModifiers(t *testing.T) {
	storeID := uuid.MustParse("11111111-1111-4111-8111-111111111111")
	prodID := uuid.MustParse("aaaa0000-0000-4000-8000-000000000001")
	grpSugarID := uuid.MustParse("bbbb0000-0000-4000-8000-000000000001")
	grpMilkID := uuid.MustParse("bbbb0000-0000-4000-8000-000000000002")
	grpFoamSubID := uuid.MustParse("bbbb0000-0000-4000-8000-000000000003")

	optLessSugarID := uuid.MustParse("cccc0000-0000-4000-8000-000000000001")
	optOatMilkID := uuid.MustParse("cccc0000-0000-4000-8000-000000000002")
	optExtraFoamID := uuid.MustParse("cccc0000-0000-4000-8000-000000000003")

	store := domain.Store{
		ID:                 storeID,
		IsActive:           true,
		TaxBps:             1000, // 10.00%
		ServiceBps:         500,  // 5.00%
		DefaultBillingMode: domain.BillingModePayFirst,
	}

	buildCatalog := func() service.CatalogSnapshot {
		return service.CatalogSnapshot{
			Products: map[uuid.UUID]domain.Product{
				prodID: {
					ID:            prodID,
					StoreID:       storeID,
					Name:          "Signature Iced Latte",
					BasePrice:     28000,
					IsAvailable:   true,
					TrackStock:    true,
					StockQuantity: 10,
				},
			},
			ModifierGroups: map[uuid.UUID]domain.ModifierGroup{
				grpSugarID: {
					ID:            grpSugarID,
					StoreID:       storeID,
					Name:          "Sugar Level",
					SelectionType: "single",
					IsRequired:    true,
					MinSelect:     1,
					MaxSelect:     1,
					IsActive:      true,
				},
				grpMilkID: {
					ID:            grpMilkID,
					StoreID:       storeID,
					Name:          "Milk Option",
					SelectionType: "single",
					IsRequired:    false,
					MinSelect:     0,
					MaxSelect:     1,
					IsActive:      true,
				},
				grpFoamSubID: {
					ID:            grpFoamSubID,
					StoreID:       storeID,
					Name:          "Oat Milk Foam Level",
					SelectionType: "single",
					IsRequired:    true,
					MinSelect:     1,
					MaxSelect:     1,
					IsActive:      true,
				},
			},
			ModifierOptions: map[uuid.UUID]domain.ModifierOption{
				optLessSugarID: {
					ID:            optLessSugarID,
					StoreID:       storeID,
					GroupID:       grpSugarID,
					Name:          "Less Sugar (50%)",
					PriceDelta:    0,
					IsAvailable:   true,
					MaxQuantity:   1,
					StockQuantity: 100,
				},
				optOatMilkID: {
					ID:            optOatMilkID,
					StoreID:       storeID,
					GroupID:       grpMilkID,
					Name:          "Upgrade Oat Milk",
					PriceDelta:    8000,
					IsAvailable:   true,
					TrackStock:    true,
					StockQuantity: 5,
					MaxQuantity:   1,
				},
				optExtraFoamID: {
					ID:            optExtraFoamID,
					StoreID:       storeID,
					GroupID:       grpFoamSubID,
					Name:          "Extra Microfoam",
					PriceDelta:    2000,
					IsAvailable:   true,
					MaxQuantity:   1,
					StockQuantity: 100,
				},
			},
			ProductGroups: map[uuid.UUID][]domain.ProductModifierGroup{
				prodID: {
					{StoreID: storeID, ProductID: prodID, GroupID: grpSugarID},
					{StoreID: storeID, ProductID: prodID, GroupID: grpMilkID},
				},
			},
			OptionChildGroups: map[uuid.UUID][]domain.OptionModifierGroup{
				optOatMilkID: {
					{StoreID: storeID, ParentOptionID: optOatMilkID, ChildGroupID: grpFoamSubID},
				},
			},
		}
	}

	t.Run("exact IDR calculation with 1-level sub-addon and round-half-up BPS", func(t *testing.T) {
		cat := buildCatalog()
		input := service.OrderCreationInput{
			IdempotencyKey: "idem-test-001",
			TableNumber:    "T04",
			CustomerName:   "Budi Santoso",
			OrderType:      domain.OrderTypeDineIn,
			Items: []service.OrderItemInput{
				{
					ProductID: prodID,
					Quantity:  2,
					Modifiers: []service.SelectedModifierInput{
						{OptionID: optLessSugarID, Quantity: 1},
						{
							OptionID: optOatMilkID,
							Quantity: 1,
							SubAddons: []service.SelectedSubAddonInput{
								{OptionID: optExtraFoamID, Quantity: 1},
							},
						},
					},
				},
			},
		}

		res, err := service.ValidateAndPriceOrder(store, input, cat)
		if err != nil {
			t.Fatalf("expected valid pricing, got error: %v", err)
		}

		// Unit price = 28,000 + 0 (Less Sugar) + 8,000 (Oat Milk) + 2,000 (Extra Microfoam) = 38,000
		// Subtotal (2x) = 76,000
		// Service Charge (5% = 500 bps) = 3,800
		// Taxable Base = 76,000 + 3,800 = 79,800
		// PB1 Tax (10% = 1000 bps) = 7,980
		// Total = 76,000 + 3,800 + 7,980 = 87,780
		if res.SubtotalAmount != 76000 {
			t.Errorf("expected subtotal 76000, got %d", res.SubtotalAmount)
		}
		if res.ServiceChargeAmount != 3800 {
			t.Errorf("expected service charge 3800, got %d", res.ServiceChargeAmount)
		}
		if res.TaxAmount != 7980 {
			t.Errorf("expected tax 7980, got %d", res.TaxAmount)
		}
		if res.TotalAmount != 87780 {
			t.Errorf("expected total 87780, got %d", res.TotalAmount)
		}
		if res.ProductStockDeducts[prodID] != 2 {
			t.Errorf("expected product stock deduction 2, got %d", res.ProductStockDeducts[prodID])
		}
		if res.OptionStockDeducts[optOatMilkID] != 2 {
			t.Errorf("expected oat milk stock deduction 2, got %d", res.OptionStockDeducts[optOatMilkID])
		}
	})

	t.Run("missing required modifier group fails", func(t *testing.T) {
		cat := buildCatalog()
		input := service.OrderCreationInput{
			CustomerName: "Budi",
			OrderType:    domain.OrderTypeTakeaway,
			Items: []service.OrderItemInput{
				{ProductID: prodID, Quantity: 1}, // Missing required Sugar Level
			},
		}
		_, err := service.ValidateAndPriceOrder(store, input, cat)
		if !errors.Is(err, service.ErrInvalidModifierSelection) {
			t.Fatalf("expected ErrInvalidModifierSelection, got %v", err)
		}
	})

	t.Run("missing required child sub-addon when parent option is selected fails", func(t *testing.T) {
		cat := buildCatalog()
		input := service.OrderCreationInput{
			CustomerName: "Budi",
			OrderType:    domain.OrderTypeTakeaway,
			Items: []service.OrderItemInput{
				{
					ProductID: prodID,
					Quantity:  1,
					Modifiers: []service.SelectedModifierInput{
						{OptionID: optLessSugarID, Quantity: 1},
						{OptionID: optOatMilkID, Quantity: 1}, // Missing required Oat Milk Foam Level sub-addon
					},
				},
			},
		}
		_, err := service.ValidateAndPriceOrder(store, input, cat)
		if !errors.Is(err, service.ErrInvalidSubAddonSelection) {
			t.Fatalf("expected ErrInvalidSubAddonSelection, got %v", err)
		}
	})

	t.Run("unavailable product is rejected", func(t *testing.T) {
		cat := buildCatalog()
		p := cat.Products[prodID]
		p.IsAvailable = false
		cat.Products[prodID] = p

		input := service.OrderCreationInput{
			CustomerName: "Budi",
			OrderType:    domain.OrderTypeTakeaway,
			Items: []service.OrderItemInput{
				{
					ProductID: prodID,
					Quantity:  1,
					Modifiers: []service.SelectedModifierInput{{OptionID: optLessSugarID, Quantity: 1}},
				},
			},
		}
		_, err := service.ValidateAndPriceOrder(store, input, cat)
		if !errors.Is(err, service.ErrProductUnavailable) {
			t.Fatalf("expected ErrProductUnavailable, got %v", err)
		}
	})

	t.Run("insufficient product or modifier stock is rejected", func(t *testing.T) {
		cat := buildCatalog()
		input := service.OrderCreationInput{
			CustomerName: "Budi",
			OrderType:    domain.OrderTypeTakeaway,
			Items: []service.OrderItemInput{
				{
					ProductID: prodID,
					Quantity:  6, // Oat milk only has stock 5
					Modifiers: []service.SelectedModifierInput{
						{OptionID: optLessSugarID, Quantity: 1},
						{
							OptionID:  optOatMilkID,
							Quantity:  1,
							SubAddons: []service.SelectedSubAddonInput{{OptionID: optExtraFoamID, Quantity: 1}},
						},
					},
				},
			},
		}
		_, err := service.ValidateAndPriceOrder(store, input, cat)
		if !errors.Is(err, service.ErrInsufficientStock) {
			t.Fatalf("expected ErrInsufficientStock, got %v", err)
		}
	})

	t.Run("round-half-up BPS and integer overflow protection", func(t *testing.T) {
		// 15,005 IDR * 70 bps (0.70%) = 105.035 -> 105 IDR
		val, err := service.CalculateRoundHalfUpBPS(15005, 70)
		if err != nil || val != 105 {
			t.Fatalf("expected 105, got %d (err=%v)", val, err)
		}
		// 15,050 IDR * 100 bps (1.00%) = 150.50 -> rounds half up to 151 IDR
		val, err = service.CalculateRoundHalfUpBPS(15050, 100)
		if err != nil || val != 151 {
			t.Fatalf("expected 151 (round half up), got %d (err=%v)", val, err)
		}
		// Overflow guard
		_, err = service.CalculateRoundHalfUpBPS(math.MaxInt64, 1000)
		if !errors.Is(err, service.ErrIntegerOverflow) {
			t.Fatalf("expected ErrIntegerOverflow, got %v", err)
		}
	})
}

