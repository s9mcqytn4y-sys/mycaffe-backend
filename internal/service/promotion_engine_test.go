package service

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/domain"
)

func ptr[T any](v T) *T { return &v }

func TestPromoRunsAt(t *testing.T) {
	wib := time.FixedZone("WIB", 7*3600)
	at := func(day, hour, minute int) time.Time { return time.Date(2026, 11, day, hour, minute, 0, 0, wib) }           // 2026-11-01 is a Sunday
	happy := domain.Promotion{IsActive: true, DaysMask: 0b0111110, StartMinute: ptr(15 * 60), EndMinute: ptr(17 * 60)} // Mon-Fri 15-17
	late := domain.Promotion{IsActive: true, DaysMask: 1 << 5, StartMinute: ptr(22 * 60), EndMinute: ptr(2 * 60)}      // Friday 22-02
	period := domain.Promotion{IsActive: true, DaysMask: 127, StartsAt: ptr(at(3, 0, 0)), EndsAt: ptr(at(5, 0, 0))}
	cases := []struct {
		name string
		p    domain.Promotion
		at   time.Time
		want bool
	}{
		{"weekday inside happy hour", happy, at(2, 15, 30), true},
		{"end minute excluded", happy, at(2, 17, 0), false},
		{"sunday is off", happy, at(1, 15, 30), false},
		{"friday night", late, at(6, 23, 0), true},
		{"after midnight counts for friday", late, at(7, 1, 30), true},
		{"after midnight on thursday's night is off", late, at(6, 1, 30), false},
		{"inside period", period, at(4, 12, 0), true},
		{"period end excluded", period, at(5, 0, 0), false},
		{"inactive", domain.Promotion{DaysMask: 127}, at(4, 12, 0), false},
	}
	for _, c := range cases {
		if got := promoRunsAt(c.p, c.at); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestBestPromotion(t *testing.T) {
	cat := uuid.New()
	product := domain.Product{ID: uuid.New(), CategoryID: &cat}
	now := time.Now()
	percent := domain.Promotion{ID: uuid.New(), IsActive: true, DaysMask: 127, Kind: domain.PromotionPercent, Value: 2000, CategoryIDs: domain.UUIDList{cat}}
	amount := domain.Promotion{ID: uuid.New(), IsActive: true, DaysMask: 127, Kind: domain.PromotionAmount, Value: 3000, ProductIDs: domain.UUIDList{product.ID}}
	other := domain.Promotion{ID: uuid.New(), IsActive: true, DaysMask: 127, Kind: domain.PromotionPercent, Value: 9000, ProductIDs: domain.UUIDList{uuid.New()}}

	// 2 x Rp25.000: 20% = Rp10.000 beats Rp3.000 x 2 = Rp6.000; a promotion for another product never applies.
	p, d := bestPromotion([]domain.Promotion{amount, other, percent}, product, 50000, 2, now, false)
	if p == nil || p.ID != percent.ID || d != 10000 {
		t.Fatalf("expected the 20%% promotion for Rp10.000, got %v %d", p, d)
	}
	usedUp := percent
	usedUp.Quota, usedUp.UsedCount = ptr(5), 5
	if p, d = bestPromotion([]domain.Promotion{amount, usedUp}, product, 50000, 2, now, false); p == nil || p.ID != amount.ID || d != 6000 {
		t.Fatalf("a used-up quota falls back to the next promotion, got %v %d", p, d)
	}
	if p, _ = bestPromotion([]domain.Promotion{usedUp}, product, 50000, 2, now, true); p == nil {
		t.Fatal("offline replay keeps a used-up promotion (X3)")
	}
	big := amount
	big.Value = 40000
	if _, d = bestPromotion([]domain.Promotion{big}, product, 50000, 2, now, false); d != 50000 {
		t.Fatalf("a discount never exceeds the line, got %d", d)
	}
}

func TestValidateAndPriceOrderAppliesPromotion(t *testing.T) {
	storeID := uuid.New()
	product := domain.Product{ID: uuid.New(), StoreID: storeID, Name: "Kopi", IsAvailable: true}
	variant := domain.ProductVariant{ID: uuid.New(), StoreID: storeID, ProductID: product.ID, Name: "Reguler", Price: 20000, IsDefault: true, IsAvailable: true}
	store := domain.Store{ID: storeID, IsActive: true, TaxBps: 1000, Timezone: "Asia/Jakarta"}
	promo := domain.Promotion{ID: uuid.New(), Name: "Happy", IsActive: true, DaysMask: 127, Kind: domain.PromotionPercent, Value: 2500, ProductIDs: domain.UUIDList{product.ID}}
	catalog := CatalogSnapshot{
		Products: map[uuid.UUID]domain.Product{product.ID: product}, Variants: map[uuid.UUID]domain.ProductVariant{variant.ID: variant},
		ModifierGroups: map[uuid.UUID]domain.ModifierGroup{}, ModifierOptions: map[uuid.UUID]domain.ModifierOption{},
		Promotions: []domain.Promotion{promo}, PromoAt: time.Now(),
	}
	priced, err := ValidateAndPriceOrder(store, OrderCreationInput{IdempotencyKey: "k", CustomerName: "A", OrderType: domain.OrderTypeTakeaway,
		Items: []OrderItemInput{{ProductID: product.ID, Quantity: 2}}}, catalog)
	if err != nil {
		t.Fatal(err)
	}
	line := priced.Items[0].Item
	// 2 x Rp20.000 = Rp40.000, 25% off = Rp30.000; tax 10% on the subtotal after the promotion (X2).
	if line.SubtotalAmount != 30000 || line.PromoDiscountAmount != 10000 || line.PromotionID == nil || *line.SnapshotPromoName != "Happy" {
		t.Fatalf("line must carry the promotion: %+v", line)
	}
	if priced.SubtotalAmount != 30000 || priced.TaxAmount != 3000 || priced.TotalAmount != 33000 {
		t.Fatalf("totals must use the promotional subtotal: %+v", priced)
	}
}
