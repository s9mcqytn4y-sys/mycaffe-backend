package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/domain"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/database"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/repository/postgres"
)

// ---------- Order Quotes & Expected Total (Iteration 4a, decision P8) ----------

type OrderQuoteInput struct {
	OrderType                domain.OrderType   `json:"order_type"`
	BillingMode              domain.BillingMode `json:"billing_mode,omitempty"`
	ExpectedTotal            *int64             `json:"expected_total,omitempty"`
	ExpectedQuoteFingerprint string             `json:"expected_quote_fingerprint,omitempty"`
	ActorUserID              *uuid.UUID         `json:"actor_user_id,omitempty"`
	SalesChannelID           *uuid.UUID         `json:"sales_channel_id,omitempty"`
	CustomerID               *uuid.UUID         `json:"customer_id,omitempty"`
	RewardItemIndex          *int               `json:"reward_item_index,omitempty"`
	Items                    []OrderItemInput   `json:"items"`
}

type PricedQuoteLine struct {
	ProductID          uuid.UUID  `json:"product_id"`
	ProductName        string     `json:"product_name"`
	VariantID          *uuid.UUID `json:"variant_id,omitempty"`
	VariantName        string     `json:"variant_name,omitempty"`
	SKU                string     `json:"sku,omitempty"`
	UnitBasePrice      int64      `json:"unit_base_price"`
	ModifierTotalPrice int64      `json:"modifier_total_price"`
	UnitSnapshotPrice  int64      `json:"unit_snapshot_price"`
	Quantity           int        `json:"quantity"`
	SubtotalAmount     int64      `json:"subtotal_amount"`
	SelectedOptions    string     `json:"selected_options"`
}

type OrderQuoteResult struct {
	SubtotalAmount      int64             `json:"subtotal_amount"`
	DiscountAmount      int64             `json:"discount_amount"`
	ServiceChargeAmount int64             `json:"service_charge_amount"`
	TaxAmount           int64             `json:"tax_amount"`
	TotalAmount         int64             `json:"total_amount"`
	Currency            string            `json:"currency"`
	QuoteFingerprint    string            `json:"quote_fingerprint"`
	Lines               []PricedQuoteLine `json:"lines"`
}

type QuoteChangedError struct {
	ExpectedTotal       int64             `json:"expected_total"`
	ActualTotal         int64             `json:"actual_total"`
	ExpectedFingerprint string            `json:"expected_fingerprint,omitempty"`
	ActualFingerprint   string            `json:"actual_fingerprint,omitempty"`
	Quote               *OrderQuoteResult `json:"quote"`
}

func (e *QuoteChangedError) Error() string {
	if e.ExpectedFingerprint != "" && e.ExpectedFingerprint != e.ActualFingerprint {
		return fmt.Sprintf("rincian/total harga quote telah berubah: fingerprint %s -> %s (Rp%d -> Rp%d)",
			e.ExpectedFingerprint, e.ActualFingerprint, e.ExpectedTotal, e.ActualTotal)
	}
	return fmt.Sprintf("total harga telah berubah: expected Rp%d, aktual Rp%d", e.ExpectedTotal, e.ActualTotal)
}

// ComputeQuoteFingerprint generates a deterministic SHA-256 fingerprint hex of the priced order lines and totals.
// This prevents silent price acceptance when compensating price changes occur (e.g. item A +Rp2.000, item B -Rp2.000).
func ComputeQuoteFingerprint(store domain.Store, priced *PricedOrderResult) string {
	h := sha256.New()
	fmt.Fprintf(h, "store=%s|curr=%s|sub=%d|disc=%d|sc=%d|tax=%d|tot=%d|lines=%d\n",
		store.ID.String(), store.Currency, priced.SubtotalAmount, priced.DiscountAmount,
		priced.ServiceChargeAmount, priced.TaxAmount, priced.TotalAmount, len(priced.Items))
	for _, item := range priced.Items {
		var prodIDStr, varIDStr string
		if item.Item.ProductID != nil {
			prodIDStr = item.Item.ProductID.String()
		}
		if item.Item.VariantID != nil {
			varIDStr = item.Item.VariantID.String()
		}
		fmt.Fprintf(h, "prod=%s|var=%s|base=%d|mod=%d|snap=%d|qty=%d|sub=%d|opt=%s\n",
			prodIDStr, varIDStr, item.Item.UnitBasePrice, item.Item.ModifierTotalPrice,
			item.Item.SnapshotPrice, item.Item.Quantity, item.Item.SubtotalAmount, item.Item.SelectedOptions)
	}
	return "qf_" + hex.EncodeToString(h.Sum(nil))[:32]
}

func buildQuoteResult(store domain.Store, priced *PricedOrderResult) *OrderQuoteResult {
	lines := make([]PricedQuoteLine, 0, len(priced.Items))
	for _, item := range priced.Items {
		prodID := uuid.Nil
		if item.Item.ProductID != nil {
			prodID = *item.Item.ProductID
		}
		lines = append(lines, PricedQuoteLine{
			ProductID:          prodID,
			ProductName:        item.Item.SnapshotName,
			VariantID:          item.Item.VariantID,
			VariantName:        item.Item.SnapshotVariant,
			SKU:                item.Item.SnapshotSKU,
			UnitBasePrice:      item.Item.UnitBasePrice,
			ModifierTotalPrice: item.Item.ModifierTotalPrice,
			UnitSnapshotPrice:  item.Item.SnapshotPrice,
			Quantity:           item.Item.Quantity,
			SubtotalAmount:     item.Item.SubtotalAmount,
			SelectedOptions:    item.Item.SelectedOptions,
		})
	}
	return &OrderQuoteResult{
		SubtotalAmount:      priced.SubtotalAmount,
		DiscountAmount:      priced.DiscountAmount,
		ServiceChargeAmount: priced.ServiceChargeAmount,
		TaxAmount:           priced.TaxAmount,
		TotalAmount:         priced.TotalAmount,
		Currency:            store.Currency,
		QuoteFingerprint:    ComputeQuoteFingerprint(store, priced),
		Lines:               lines,
	}
}

// ResolveStoreBySlug looks up a store ID and active status using the sanctioned security-definer resolver.
func (s *OrderService) ResolveStoreBySlug(ctx context.Context, slug string) (uuid.UUID, bool, error) {
	return postgres.NewAuthRepository().ResolveStoreForLogin(ctx, s.db, strings.ToLower(strings.TrimSpace(slug)))
}

// QuoteOrder computes priced lines, taxes, service fees, and totals without locking rows or reserving stock.
func (s *OrderService) QuoteOrder(ctx context.Context, trustedStoreID uuid.UUID, input OrderQuoteInput) (*OrderQuoteResult, error) {
	if trustedStoreID == uuid.Nil {
		return nil, postgres.ErrEmptyTenantContext
	}
	if len(input.Items) == 0 {
		return nil, fmt.Errorf("%w: order must contain at least one item", ErrInvalidOrderInput)
	}

	var result *OrderQuoteResult
	err := database.WithTenantTx(ctx, s.db, trustedStoreID, func(tx *sqlx.Tx) error {
		productIDs := orderProductIDs(input.Items)

		catalogData, err := s.repo.LoadCatalogForQuote(ctx, tx, trustedStoreID, productIDs)
		if err != nil {
			return err
		}

		snapshot := catalogSnapshotOf(catalogData, s.nowFunc())
		if input.SalesChannelID != nil {
			if _, err := applySalesChannelTx(ctx, tx, trustedStoreID, *input.SalesChannelID, &catalogData.Store, &snapshot); err != nil {
				return err
			}
		}

		billingMode := input.BillingMode
		if billingMode == "" {
			billingMode = catalogData.Store.DefaultBillingMode
		}

		creationInput := OrderCreationInput{
			IdempotencyKey:  "quote-" + uuid.NewString(),
			CustomerName:    "Quote",
			OrderType:       input.OrderType,
			BillingMode:     billingMode,
			ActorUserID:     input.ActorUserID,
			Items:           input.Items,
			SalesChannelID:  input.SalesChannelID,
			CustomerID:      input.CustomerID,
			RewardItemIndex: input.RewardItemIndex,
		}
		if input.RewardItemIndex != nil {
			if input.CustomerID == nil {
				return fmt.Errorf("%w: a stamp card reward needs customer_id", ErrInvalidOrderInput)
			}
			if err := applyCustomerTx(ctx, tx, trustedStoreID, creationInput, &snapshot, s.nowFunc().UTC()); err != nil {
				return err
			}
		}

		priced, err := ValidateAndPriceOrder(catalogData.Store, creationInput, snapshot)
		if err != nil {
			return err
		}

		result = buildQuoteResult(catalogData.Store, priced)

		totalMismatch := input.ExpectedTotal != nil && *input.ExpectedTotal != priced.TotalAmount
		fingerprintMismatch := input.ExpectedQuoteFingerprint != "" && input.ExpectedQuoteFingerprint != result.QuoteFingerprint

		if totalMismatch || fingerprintMismatch {
			var expTotal int64
			if input.ExpectedTotal != nil {
				expTotal = *input.ExpectedTotal
			}
			return &QuoteChangedError{
				ExpectedTotal:       expTotal,
				ActualTotal:         priced.TotalAmount,
				ExpectedFingerprint: input.ExpectedQuoteFingerprint,
				ActualFingerprint:   result.QuoteFingerprint,
				Quote:               result,
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}
