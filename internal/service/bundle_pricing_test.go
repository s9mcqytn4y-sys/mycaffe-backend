package service

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/domain"
)

// X4: a bundle sells as component lines whose subtotals add up to the bundle price, allocated in
// proportion to the components' normal prices; component stock is deducted; promotions never apply.
func TestValidateAndPriceOrderBundle(t *testing.T) {
	storeID := uuid.New()
	product := func(name string, price int64, station string, stock int) (domain.Product, domain.ProductVariant) {
		p := domain.Product{ID: uuid.New(), StoreID: storeID, Name: name, IsAvailable: true, PrepStation: station,
			TrackStock: stock >= 0, StockQuantity: stock}
		return p, domain.ProductVariant{ID: uuid.New(), StoreID: storeID, ProductID: p.ID, Name: "Reguler", Price: price, IsDefault: true, IsAvailable: true}
	}
	coffee, coffeeV := product("Kopi Susu", 25000, "bar", -1)
	latte, latteV := product("Latte", 30000, "bar", -1)
	pastry, pastryV := product("Croissant", 20000, "kitchen", 3)
	bundle, bundleV := product("Paket Hemat", 35000, "none", -1)
	bundle.ProductType = domain.ProductTypeBundle
	drink := domain.BundleSlot{ID: uuid.New(), Name: "Kopi", Quantity: 1, ProductIDs: domain.UUIDList{coffee.ID, latte.ID}}
	food := domain.BundleSlot{ID: uuid.New(), Name: "Pastry", Quantity: 1, ProductIDs: domain.UUIDList{pastry.ID}}
	catalog := CatalogSnapshot{
		Products:       map[uuid.UUID]domain.Product{coffee.ID: coffee, latte.ID: latte, pastry.ID: pastry, bundle.ID: bundle},
		Variants:       map[uuid.UUID]domain.ProductVariant{coffeeV.ID: coffeeV, latteV.ID: latteV, pastryV.ID: pastryV, bundleV.ID: bundleV},
		ModifierGroups: map[uuid.UUID]domain.ModifierGroup{}, ModifierOptions: map[uuid.UUID]domain.ModifierOption{},
		BundleSlots: map[uuid.UUID][]domain.BundleSlot{bundle.ID: {drink, food}},
		Promotions: []domain.Promotion{{ID: uuid.New(), IsActive: true, DaysMask: 127, Kind: domain.PromotionPercent, Value: 5000,
			ProductIDs: domain.UUIDList{coffee.ID, pastry.ID}}},
		PromoAt: time.Now(),
	}
	store := domain.Store{ID: storeID, IsActive: true, Timezone: "Asia/Jakarta"}
	order := func(choices ...BundleChoiceInput) (*PricedOrderResult, error) {
		return ValidateAndPriceOrder(store, OrderCreationInput{IdempotencyKey: "k", CustomerName: "A", OrderType: domain.OrderTypeTakeaway,
			Items: []OrderItemInput{{ProductID: bundle.ID, Quantity: 2, BundleItems: choices}}}, catalog)
	}

	priced, err := order(BundleChoiceInput{SlotID: drink.ID, ProductID: coffee.ID}, BundleChoiceInput{SlotID: food.ID, ProductID: pastry.ID})
	if err != nil {
		t.Fatal(err)
	}
	// 2 bundles = Rp70.000 over coffee 25.000 and croissant 20.000: 70.000 x 25/45 = 38.888, remainder 31.112.
	if len(priced.Items) != 2 || priced.SubtotalAmount != 70000 {
		t.Fatalf("two component lines adding up to the bundle price, got %d lines and %d", len(priced.Items), priced.SubtotalAmount)
	}
	c, p := priced.Items[0].Item, priced.Items[1].Item
	if c.SubtotalAmount != 38888 || p.SubtotalAmount != 31112 || c.Quantity != 2 || p.SnapshotPrepStation != "kitchen" {
		t.Fatalf("allocation must follow the normal prices: coffee %+v croissant %+v", c, p)
	}
	if c.BundleGroupID == nil || p.BundleGroupID == nil || *c.BundleGroupID != *p.BundleGroupID || *c.SnapshotBundleName != "Paket Hemat" {
		t.Fatal("component lines share one bundle group and name")
	}
	if c.PromotionID != nil || p.PromotionID != nil {
		t.Fatal("promotions never apply to bundle lines")
	}
	if priced.ProductStockDeducts[pastry.ID] != 2 {
		t.Fatalf("component stock is deducted, got %v", priced.ProductStockDeducts)
	}

	for name, choices := range map[string][]BundleChoiceInput{
		"missing slot":     {{SlotID: drink.ID, ProductID: coffee.ID}},
		"not a choice":     {{SlotID: drink.ID, ProductID: pastry.ID}, {SlotID: food.ID, ProductID: pastry.ID}},
		"same slot twice":  {{SlotID: drink.ID, ProductID: coffee.ID}, {SlotID: drink.ID, ProductID: latte.ID}},
		"bundle as choice": {{SlotID: drink.ID, ProductID: bundle.ID}, {SlotID: food.ID, ProductID: pastry.ID}},
	} {
		if _, err := order(choices...); !errors.Is(err, ErrInvalidOrderInput) {
			t.Errorf("%s must be refused, got %v", name, err)
		}
	}
	short := catalog
	short.Products = map[uuid.UUID]domain.Product{coffee.ID: coffee, pastry.ID: pastry, bundle.ID: bundle}
	pastry.StockQuantity = 1
	short.Products[pastry.ID] = pastry
	catalog = short
	if _, err := order(BundleChoiceInput{SlotID: drink.ID, ProductID: coffee.ID}, BundleChoiceInput{SlotID: food.ID, ProductID: pastry.ID}); !errors.Is(err, ErrInsufficientStock) {
		t.Fatalf("short component stock must refuse online, got %v", err)
	}
}
