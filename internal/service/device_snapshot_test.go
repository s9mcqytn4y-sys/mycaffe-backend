package service

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/domain"
)

// W20: the catalog version follows the pricing content only; stock and availability must not
// create new versions, a price change must.
func TestPricingSnapshotVersionInputs(t *testing.T) {
	stock := 5
	view := func(price int64, available bool, stock *int) *CatalogView {
		return &CatalogView{
			Store: CatalogStore{TaxBPS: 1100, ServiceBPS: 500},
			Products: []CatalogProduct{{
				ID: uuid.MustParse("d0000003-0000-4000-8000-000000000003"), Name: "Americano", Available: available, StockQuantity: stock,
				Variants: []CatalogVariant{{ID: uuid.MustParse("d0000013-0000-4000-8000-000000000013"), Name: "Reguler", Price: price, Available: available}},
			}},
		}
	}
	settings := domain.StoreSettings{CashRoundingStep: 100, CashierMaxDiscountBPS: 1000}
	encode := func(v *CatalogView) string {
		b, err := json.Marshal(pricingSnapshot(v, settings))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	base := encode(view(25000, true, nil))
	if encode(view(25000, false, &stock)) != base {
		t.Fatal("availability and stock must not change the snapshot")
	}
	if encode(view(26000, true, nil)) == base {
		t.Fatal("a price change must change the snapshot")
	}
	settings.CashRoundingStep = 500
	if encode(view(25000, true, nil)) == base {
		t.Fatal("a rounding change must change the snapshot")
	}
}
