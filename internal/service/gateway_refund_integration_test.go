package service_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/midtrans/midtrans-go/coreapi"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/config"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/domain"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/pkg/database"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/pkg/rbac"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/repository/postgres"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/service"
)

// TestGatewayRefunds covers RF1-a/b/f (PR 8.5b): a Direct Refund request leaves money untouched until
// the bank confirms, the confirming notification (and not the first one) applies it once, a denied
// request falls back to manual refunds, partial gateway refunds stay off by default, an unanswered
// request is settled by the reconciliation status check, and non-gateway payments are refused.
func TestGatewayRefunds(t *testing.T) {
	migratorDSN := strings.TrimSpace(os.Getenv("TEST_MIGRATOR_DATABASE_URL"))
	if migratorDSN == "" {
		t.Skip("TEST_MIGRATOR_DATABASE_URL not set; skipping gateway refund integration test")
	}
	db := openIntegrationAppDB(t)
	migratorDB, err := database.NewPostgresDB(&config.Config{DatabaseURL: migratorDSN})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = migratorDB.Close() })
	ctx := context.Background()

	slug := "gwrefund-" + uuid.NewString()[:8]
	storeID, ownerID, err := service.BootstrapStore(ctx, migratorDB, service.BootstrapStoreInput{
		Slug: slug, StoreName: "Kafe Refund QRIS", OwnerName: "Pemilik QRIS", OwnerEmail: "o@" + slug + ".test", PIN: "731904",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := migratorDB.ExecContext(ctx, `UPDATE stores SET tax_bps = 0, service_bps = 0, enabled_payment_methods = '["cash","qris"]'::jsonb WHERE id = $1`, storeID); err != nil {
		t.Fatal(err)
	}
	productID, drawerID := uuid.New(), uuid.New()
	shiftRepo := postgres.NewShiftRepository()
	now := time.Now().UTC()
	if err := database.WithTenantTx(ctx, db, storeID, func(tx *sqlx.Tx) error {
		if _, err := tx.ExecContext(ctx, `WITH p AS (INSERT INTO products (id, store_id, name, base_price, is_available) VALUES ($1, $2, 'Kopi', 0, true) RETURNING store_id, id)
			INSERT INTO product_variants (store_id, product_id, name, price, is_default) SELECT store_id, id, 'Reguler', 10000, true FROM p`, productID, storeID); err != nil {
			return err
		}
		if err := shiftRepo.InsertCashDrawer(ctx, tx, &domain.CashDrawer{ID: drawerID, StoreID: storeID, Name: "Laci", IsActive: true, CreatedAt: now, UpdatedAt: now}); err != nil {
			return err
		}
		return shiftRepo.InsertShift(ctx, tx, &domain.Shift{ID: uuid.New(), StoreID: storeID, DrawerID: drawerID, OpenedBy: ownerID,
			OpeningFloat: 100000, OpenedAt: now, Status: domain.ShiftStatusOpen, CreatedAt: now, UpdatedAt: now})
	}); err != nil {
		t.Fatal(err)
	}

	orderRepo, paymentRepo := postgres.NewOrderRepository(), postgres.NewPaymentRepository()
	orderSvc := service.NewOrderService(db, orderRepo)
	gw := &fakeGatewayClient{}
	paySvc := service.NewPaymentService(db, paymentRepo, orderRepo, gw, service.WithShiftRepository(shiftRepo))
	ops := service.NewOperationalService(db, orderRepo, paymentRepo, shiftRepo, nil, nil)
	ops.SetGateway(gw)
	owner := &service.AuthenticatedStaff{
		Staff:       service.StaffIdentity{UserID: ownerID, FullName: "Pemilik QRIS", Role: domain.RoleOwner, Store: service.StaffStore{ID: storeID}},
		Permissions: rbac.Permissions(domain.RoleOwner, rbac.Options{}),
	}
	restock := true

	notify := func(p *domain.Payment, status string, refunds string) *service.PaymentWebhookResult {
		t.Helper()
		res, err := paySvc.ProcessMidtransWebhook(ctx, service.MidtransWebhookInput{
			OrderID: p.ProviderOrderID, TransactionID: p.TransactionID, StatusCode: "200", TransactionStatus: status,
			GrossAmount: fmt.Sprintf("%d.00", p.Amount), FraudStatus: "accept", PaymentType: "qris", Currency: "IDR",
		}, `{"refunds":`+refunds+`}`)
		if err != nil {
			t.Fatalf("webhook %s: %v", status, err)
		}
		return res
	}
	paidQRIS := func(qty int) (*domain.Order, *domain.Payment) {
		t.Helper()
		created, err := orderSvc.CreateOrder(ctx, storeID, service.OrderCreationInput{IdempotencyKey: "gw-" + uuid.NewString(), CustomerName: "Tamu",
			OrderType: domain.OrderTypeTakeaway, BillingMode: domain.BillingModePayFirst, Items: []service.OrderItemInput{{ProductID: productID, Quantity: qty}}})
		if err != nil {
			t.Fatal(err)
		}
		init, err := paySvc.InitiatePayment(ctx, storeID, service.InitiatePaymentInput{OrderID: created.Order.ID, IdempotencyKey: "gw-q-" + uuid.NewString(), Method: domain.PaymentMethodQRIS})
		if err != nil {
			t.Fatal(err)
		}
		notify(init.Payment, "settlement", "[]")
		return &created.Order, init.Payment
	}
	refund := func(orderID, paymentID uuid.UUID, method string, extra func(*service.RefundPaymentInput)) (*service.RefundPaymentResult, error) {
		in := service.RefundPaymentInput{Reason: "Pelanggan komplain", PIN: "731904", RestoreStock: &restock, RefundMethod: method}
		if extra != nil {
			extra(&in)
		}
		return ops.RefundPayment(ctx, owner, orderID, paymentID, in)
	}
	state := func(paymentID uuid.UUID) (status string, refunded int64, orderStatus string, gatewayStatus string) {
		_ = migratorDB.QueryRowxContext(ctx, `SELECT p.status, p.refunded_amount, o.payment_status,
			COALESCE((SELECT status FROM gateway_refunds g WHERE g.payment_id = p.id ORDER BY created_at DESC LIMIT 1), '')
			FROM payments p JOIN orders o ON o.id = p.order_id WHERE p.id = $1`, paymentID).Scan(&status, &refunded, &orderStatus, &gatewayStatus)
		return
	}
	refundKey := func(paymentID uuid.UUID) string {
		var id string
		_ = migratorDB.GetContext(ctx, &id, `SELECT id FROM gateway_refunds WHERE payment_id = $1 ORDER BY created_at DESC LIMIT 1`, paymentID)
		return id
	}

	t.Run("BankConfirmationAppliesTheRefundOnce", func(t *testing.T) {
		order, p := paidQRIS(1)
		res, err := refund(order.ID, p.ID, "gateway", nil)
		if err != nil || res.RefundStatus != "requested" {
			t.Fatalf("gateway refund request: %+v %v", res, err)
		}
		if status, refunded, orderStatus, gs := state(p.ID); status != "paid" || refunded != 0 || orderStatus != "paid" || gs != "requested" {
			t.Fatalf("nothing changes before the bank confirms: %s %d %s %s", status, refunded, orderStatus, gs)
		}
		if _, err := refund(order.ID, p.ID, "cash", nil); !errors.Is(err, service.ErrGatewayRefundPending) {
			t.Fatalf("an open gateway refund must block a manual one: %v", err)
		}
		key := refundKey(p.ID)
		notify(p, "refund", `[{"refund_key":"`+key+`","refund_amount":"10000.00"}]`)
		if status, _, _, gs := state(p.ID); status != "paid" || gs != "requested" {
			t.Fatalf("the notification without bank_confirmed_at must not apply the refund: %s %s", status, gs)
		}
		confirmed := `[{"refund_key":"` + key + `","refund_amount":"10000.00","bank_confirmed_at":"2026-11-04 22:00:00"}]`
		if res := notify(p, "refund", confirmed); res.Duplicate {
			t.Fatal("the bank-confirmed notification must not be dropped as a duplicate of the first one")
		}
		if status, refunded, orderStatus, gs := state(p.ID); status != "refunded" || refunded != 10000 || orderStatus != "refunded" || gs != "confirmed" {
			t.Fatalf("after bank confirmation: %s %d %s %s", status, refunded, orderStatus, gs)
		}
		notify(p, "refund", confirmed)
		var ledgerRows int
		_ = migratorDB.GetContext(ctx, &ledgerRows, `SELECT COUNT(*) FROM payment_refunds WHERE payment_id = $1 AND method = 'gateway'`, p.ID)
		if _, refunded, _, _ := state(p.ID); refunded != 10000 || ledgerRows != 1 {
			t.Fatalf("a repeated notification must not refund twice: refunded %d ledger rows %d", refunded, ledgerRows)
		}
	})

	t.Run("DeniedRefundFallsBackToManual", func(t *testing.T) {
		order, p := paidQRIS(1)
		gw.mu.Lock()
		gw.refundFunc = func(_ context.Context, orderID, key string, _ int64) (*coreapi.RefundResponse, error) {
			return &coreapi.RefundResponse{StatusCode: "202", StatusMessage: "Refund denied by the bank", RefundKey: key, RefundAmount: "0.00"}, nil
		}
		gw.mu.Unlock()
		defer func() { gw.mu.Lock(); gw.refundFunc = nil; gw.mu.Unlock() }()
		if _, err := refund(order.ID, p.ID, "gateway", nil); !errors.Is(err, service.ErrGatewayRefundDenied) {
			t.Fatalf("202 is a denial: %v", err)
		}
		if _, _, _, gs := state(p.ID); gs != "failed" {
			t.Fatalf("denied request must be failed: %s", gs)
		}
		res, err := refund(order.ID, p.ID, "cash", nil)
		if err != nil || res.Payment.Status != domain.PaymentStatusRefunded {
			t.Fatalf("manual fallback after a denial: %+v %v", res, err)
		}
	})

	t.Run("PartialGatewayRefundsOffAndUnsupportedPayments", func(t *testing.T) {
		order, p := paidQRIS(2)
		if _, err := refund(order.ID, p.ID, "gateway", func(in *service.RefundPaymentInput) {
			in.IdempotencyKey = uuid.NewString()
			in.Lines = []service.RefundLineInput{{OrderItemID: order.Items[0].ID, Quantity: 1}}
		}); !errors.Is(err, service.ErrGatewayPartialDisabled) {
			t.Fatalf("partial gateway refunds are off by default (RF1-b): %v", err)
		}
		cashOrder, err := orderSvc.CreateOrder(ctx, storeID, service.OrderCreationInput{IdempotencyKey: "gw-c-" + uuid.NewString(), CustomerName: "Tamu",
			OrderType: domain.OrderTypeTakeaway, BillingMode: domain.BillingModePayFirst, Items: []service.OrderItemInput{{ProductID: productID, Quantity: 1}}})
		if err != nil {
			t.Fatal(err)
		}
		cash, err := paySvc.InitiatePayment(ctx, storeID, service.InitiatePaymentInput{OrderID: cashOrder.Order.ID, IdempotencyKey: "gw-cp-" + uuid.NewString(),
			Method: domain.PaymentMethodCash, CashTenderedAmount: 10000, ActorUserID: &ownerID, DrawerID: &drawerID})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := refund(cashOrder.Order.ID, cash.Payment.ID, "gateway", nil); !errors.Is(err, service.ErrGatewayRefundUnsupported) {
			t.Fatalf("a cash payment cannot be refunded through the gateway: %v", err)
		}
	})

	t.Run("ReconciliationConfirmsAnUnansweredRequest", func(t *testing.T) {
		order, p := paidQRIS(1)
		gw.mu.Lock()
		gw.refundFunc = func(context.Context, string, string, int64) (*coreapi.RefundResponse, error) {
			return nil, errors.New("timeout")
		}
		gw.mu.Unlock()
		res, err := refund(order.ID, p.ID, "gateway", nil)
		gw.mu.Lock()
		gw.refundFunc = nil
		gw.mu.Unlock()
		if err != nil || res.RefundStatus != "pending" {
			t.Fatalf("an unanswered call stays pending: %+v %v", res, err)
		}
		key := refundKey(p.ID)
		if _, err := migratorDB.ExecContext(ctx, `UPDATE gateway_refunds SET updated_at = NOW() - interval '10 minutes' WHERE id = $1`, key); err != nil {
			t.Fatal(err)
		}
		gw.mu.Lock()
		gw.checkFunc = func(_ context.Context, orderID string) (*coreapi.TransactionStatusResponse, error) {
			if orderID != p.ProviderOrderID {
				return nil, errors.New("other order")
			}
			return &coreapi.TransactionStatusResponse{StatusCode: "200", TransactionStatus: "refund",
				Refunds: []coreapi.RefundDetails{{RefundKey: key, RefundAmount: "10000.00", BankConfirmedAt: "2026-11-04 22:30:00"}}}, nil
		}
		gw.mu.Unlock()
		defer func() { gw.mu.Lock(); gw.checkFunc = nil; gw.mu.Unlock() }()
		if err := paySvc.ReconcileGatewayRefunds(ctx, 2*time.Minute); err != nil {
			t.Fatal(err)
		}
		if status, refunded, _, gs := state(p.ID); status != "refunded" || refunded != 10000 || gs != "confirmed" {
			t.Fatalf("reconciliation must confirm the refund: %s %d %s", status, refunded, gs)
		}
	})
}
