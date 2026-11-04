package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/midtrans/midtrans-go/coreapi"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/domain"
)

func isDirectCashierMethod(method domain.PaymentMethod) bool {
	switch method {
	case domain.PaymentMethodCash, domain.PaymentMethodStaticQRIS, domain.PaymentMethodEDC:
		return true
	default:
		return false
	}
}

func isMidtransGatewayMethod(method domain.PaymentMethod) bool {
	switch method {
	case domain.PaymentMethodQRIS, domain.PaymentMethodGoPay, domain.PaymentMethodShopeePay, domain.PaymentMethodBankTransfer:
		return true
	default:
		return false
	}
}

func isPaymentMethodEnabled(enabledJSON string, method domain.PaymentMethod) bool {
	trimmed := strings.TrimSpace(enabledJSON)
	if trimmed == "" || trimmed == "[]" {
		return false
	}
	var methods []string
	if err := json.Unmarshal([]byte(trimmed), &methods); err != nil {
		return false
	}
	for _, m := range methods {
		if strings.EqualFold(strings.TrimSpace(m), string(method)) {
			return true
		}
	}
	return false
}

func resolveOrderSplit(requestedSplitID *uuid.UUID, splits []domain.PaymentSplit) (*domain.PaymentSplit, error) {
	if len(splits) == 0 {
		return nil, errors.New("order has no payment splits")
	}
	if requestedSplitID == nil || *requestedSplitID == uuid.Nil {
		if len(splits) == 1 {
			cp := splits[0]
			return &cp, nil
		}
		return nil, errors.New("split_id is required when an order has multiple payment splits")
	}
	for _, sp := range splits {
		if sp.ID == *requestedSplitID {
			cp := sp
			return &cp, nil
		}
	}
	return nil, fmt.Errorf("payment split %s not found on order", *requestedSplitID)
}

func computePaymentInitiationHash(storeID uuid.UUID, input InitiatePaymentInput) string {
	var splitIDStr, actorIDStr, drawerIDStr string
	if input.SplitID != nil && *input.SplitID != uuid.Nil {
		splitIDStr = input.SplitID.String()
	}
	if input.ActorUserID != nil && *input.ActorUserID != uuid.Nil {
		actorIDStr = input.ActorUserID.String()
	}
	if input.DrawerID != nil && *input.DrawerID != uuid.Nil {
		drawerIDStr = input.DrawerID.String()
	}
	canonical, _ := json.Marshal(struct {
		StoreID            string               `json:"store_id"`
		OrderID            string               `json:"order_id"`
		SplitID            string               `json:"split_id"`
		Method             domain.PaymentMethod `json:"method"`
		Bank               string               `json:"bank"`
		CashTenderedAmount int64                `json:"cash_tendered_amount"`
		Amount             *int64               `json:"amount"`
		ReconciliationRef  string               `json:"reconciliation_ref"`
		ActorUserID        string               `json:"actor_user_id"`
		DrawerID           string               `json:"drawer_id"`
	}{
		StoreID:            storeID.String(),
		OrderID:            input.OrderID.String(),
		SplitID:            splitIDStr,
		Method:             input.Method,
		Bank:               input.Bank,
		CashTenderedAmount: input.CashTenderedAmount,
		Amount:             input.Amount,
		ReconciliationRef:  input.ReconciliationRef,
		ActorUserID:        actorIDStr,
		DrawerID:           drawerIDStr,
	})
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])
}

func directCashierAuditDetails(method domain.PaymentMethod, orderNumber, reconciliationRef string) (string, string) {
	switch method {
	case domain.PaymentMethodCash:
		return "CASH_PAYMENT_RECORDED", fmt.Sprintf("Cash payment settled by cashier for order %s", orderNumber)
	case domain.PaymentMethodStaticQRIS:
		return "STATIC_QRIS_RECONCILE", fmt.Sprintf("Static QRIS manually reconciled for order %s (ref: %s)", orderNumber, reconciliationRef)
	case domain.PaymentMethodEDC:
		return "EDC_RECONCILE", fmt.Sprintf("EDC terminal payment reconciled for order %s (ref: %s)", orderNumber, reconciliationRef)
	default:
		return "STATIC_QRIS_RECONCILE", fmt.Sprintf("Direct cashier tender for order %s", orderNumber)
	}
}

func hasGatewayChargeDetails(p *domain.Payment) bool {
	if p == nil {
		return false
	}
	return p.TransactionID != "" || p.QRString != "" || p.VANumber != "" || p.DeeplinkURL != "" || p.BillKey != ""
}

func isPermanentGatewayRejection(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "status 400") ||
		strings.Contains(msg, "status 401") ||
		strings.Contains(msg, "status 403") ||
		strings.Contains(msg, "status 404") ||
		strings.Contains(msg, "status 406") ||
		strings.Contains(msg, "unsupported")
}

func applyMidtransChargeResponse(payment *domain.Payment, resp *coreapi.ChargeResponse) {
	if payment == nil || resp == nil {
		return
	}
	payment.TransactionID = strings.TrimSpace(resp.TransactionID)
	payment.PaymentType = strings.TrimSpace(resp.PaymentType)
	payment.Acquirer = strings.TrimSpace(resp.Acquirer)
	payment.QRString = strings.TrimSpace(resp.QRString)
	payment.RawProviderStatus = strings.TrimSpace(resp.TransactionStatus)
	payment.FraudStatus = strings.TrimSpace(resp.FraudStatus)
	payment.BillerCode = strings.TrimSpace(resp.BillerCode)
	payment.BillKey = strings.TrimSpace(resp.BillKey)
	payment.PaymentCode = strings.TrimSpace(resp.PaymentCode)

	if len(resp.VaNumbers) > 0 {
		payment.VANumber = strings.TrimSpace(resp.VaNumbers[0].VANumber)
		if payment.Bank == "" {
			payment.Bank = strings.ToLower(strings.TrimSpace(resp.VaNumbers[0].Bank))
		}
	} else if resp.PermataVaNumber != "" {
		payment.VANumber = strings.TrimSpace(resp.PermataVaNumber)
		if payment.Bank == "" {
			payment.Bank = "permata"
		}
	}

	for _, action := range resp.Actions {
		if action.URL == "" {
			continue
		}
		name := strings.ToLower(strings.TrimSpace(action.Name))
		if name == "deeplink-redirect" || payment.DeeplinkURL == "" {
			payment.DeeplinkURL = strings.TrimSpace(action.URL)
		}
	}

	if parsedExpiry := parseMidtransExpiryTime(resp.ExpiryTime); parsedExpiry != nil {
		payment.ExpiresAt = parsedExpiry
	}
}

func applyMidtransStatusResponse(payment *domain.Payment, resp *coreapi.TransactionStatusResponse) {
	if payment == nil || resp == nil {
		return
	}
	if resp.TransactionID != "" {
		payment.TransactionID = strings.TrimSpace(resp.TransactionID)
	}
	if resp.PaymentType != "" {
		payment.PaymentType = strings.TrimSpace(resp.PaymentType)
	}
	if resp.Acquirer != "" {
		payment.Acquirer = strings.TrimSpace(resp.Acquirer)
	}
	if resp.TransactionStatus != "" {
		payment.RawProviderStatus = strings.TrimSpace(resp.TransactionStatus)
	}
	if resp.FraudStatus != "" {
		payment.FraudStatus = strings.TrimSpace(resp.FraudStatus)
	}
	if resp.BillerCode != "" {
		payment.BillerCode = strings.TrimSpace(resp.BillerCode)
	}
	if resp.BillKey != "" {
		payment.BillKey = strings.TrimSpace(resp.BillKey)
	}
	if resp.PaymentCode != "" {
		payment.PaymentCode = strings.TrimSpace(resp.PaymentCode)
	}
	if len(resp.VaNumbers) > 0 {
		payment.VANumber = strings.TrimSpace(resp.VaNumbers[0].VANumber)
		if payment.Bank == "" {
			payment.Bank = strings.ToLower(strings.TrimSpace(resp.VaNumbers[0].Bank))
		}
	} else if resp.PermataVaNumber != "" {
		payment.VANumber = strings.TrimSpace(resp.PermataVaNumber)
		if payment.Bank == "" {
			payment.Bank = "permata"
		}
	}
	if parsedExpiry := parseMidtransExpiryTime(resp.ExpiryTime); parsedExpiry != nil {
		payment.ExpiresAt = parsedExpiry
	}
}

func parseMidtransExpiryTime(raw string) *time.Time {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}
	if t, err := time.Parse(time.RFC3339, trimmed); err == nil {
		utc := t.UTC()
		return &utc
	}
	wib := time.FixedZone("WIB", 7*3600)
	if t, err := time.ParseInLocation("2006-01-02 15:04:05", trimmed, wib); err == nil {
		utc := t.UTC()
		return &utc
	}
	return nil
}
