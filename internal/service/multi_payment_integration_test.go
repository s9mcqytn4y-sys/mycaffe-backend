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
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/config"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/domain"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/pkg/database"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/repository/postgres"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/service"
)

// TestMultiPaymentAndRounding covers decisions V11/V12: several direct tenders on one bill, a pending
// gateway attempt locking partial tenders, dynamic QRIS only for exactly the remaining balance (and
// the webhook settling that remainder), and cash rounding of the closing tender with the drawer ledger
// recording the cash that physically moved.
func TestMultiPaymentAndRounding(t *testing.T) {
	migratorDSN := strings.TrimSpace(os.Getenv("TEST_MIGRATOR_DATABASE_URL"))
	if migratorDSN == "" {
		t.Skip("TEST_MIGRATOR_DATABASE_URL not set; skipping multi-payment integration test")
	}
	db := openIntegrationAppDB(t)
	migratorDB, err := database.NewPostgresDB(&config.Config{DatabaseURL: migratorDSN})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = migratorDB.Close() })
	ctx := context.Background()

	slug := "multipay-" + uuid.NewString()[:8]
	storeID, ownerID, err := service.BootstrapStore(ctx, migratorDB, service.BootstrapStoreInput{
		Slug: slug, StoreName: "Kafe Multi", OwnerName: "Pemilik Multi", OwnerEmail: "o@" + slug + ".test", PIN: "731904",
	})
	if err != nil {
		t.Fatal(err)
	}
	// Tax and service off so totals equal the price and the rounding is easy to read.
	if _, err := migratorDB.ExecContext(ctx, `UPDATE stores SET tax_bps = 0, service_bps = 0,
		enabled_payment_methods = '["cash","edc","qris"]'::jsonb WHERE id = $1`, storeID); err != nil {
		t.Fatal(err)
	}
	productID := uuid.New()
	drawerID, shiftID := uuid.New(), uuid.New()
	now := time.Now().UTC()
	shiftRepo := postgres.NewShiftRepository()
	if err := database.WithTenantTx(ctx, db, storeID, func(tx *sqlx.Tx) error {
		if _, err := tx.ExecContext(ctx, `WITH p AS (INSERT INTO products (id, store_id, name, base_price, is_available) VALUES ($1, $2, 'Paket', 0, true) RETURNING store_id, id)
			INSERT INTO product_variants (store_id, product_id, name, price, is_default) SELECT store_id, id, 'Reguler', 10260, true FROM p`, productID, storeID); err != nil {
			return err
		}
		if err := shiftRepo.InsertCashDrawer(ctx, tx, &domain.CashDrawer{ID: drawerID, StoreID: storeID, Name: "Laci Multi", IsActive: true, CreatedAt: now, UpdatedAt: now}); err != nil {
			return err
		}
		return shiftRepo.InsertShift(ctx, tx, &domain.Shift{ID: shiftID, StoreID: storeID, DrawerID: drawerID, OpenedBy: ownerID,
			OpeningFloat: 100000, OpenedAt: now, Status: domain.ShiftStatusOpen, CreatedAt: now, UpdatedAt: now})
	}); err != nil {
		t.Fatal(err)
	}

	orderRepo, paymentRepo := postgres.NewOrderRepository(), postgres.NewPaymentRepository()
	orderSvc := service.NewOrderService(db, orderRepo)
	gw := &fakeGatewayClient{}
	paySvc := service.NewPaymentService(db, paymentRepo, orderRepo, gw, service.WithShiftRepository(shiftRepo))

	newOrder := func(qty int) *domain.Order {
		t.Helper()
		res, err := orderSvc.CreateOrder(ctx, storeID, service.OrderCreationInput{
			IdempotencyKey: "mp-" + uuid.NewString(), CustomerName: "Pelanggan Multi", OrderType: domain.OrderTypeTakeaway, BillingMode: domain.BillingModePayFirst,
			Items: []service.OrderItemInput{{ProductID: productID, Quantity: qty}},
		})
		if err != nil {
			t.Fatalf("create order: %v", err)
		}
		return &res.Order
	}
	amt := func(v int64) *int64 { return &v }
	direct := func(orderID uuid.UUID, method domain.PaymentMethod, amount *int64, tendered int64) (*service.InitiatePaymentResult, error) {
		in := service.InitiatePaymentInput{OrderID: orderID, IdempotencyKey: "mp-pay-" + uuid.NewString(), Method: method,
			Amount: amount, CashTenderedAmount: tendered, ActorUserID: &ownerID, DrawerID: &drawerID}
		if method == domain.PaymentMethodEDC {
			in.ReconciliationRef = "TRACE-" + uuid.NewString()[:6]
		}
		return paySvc.InitiatePayment(ctx, storeID, in)
	}
	setRounding := func(step int) {
		t.Helper()
		if _, err := migratorDB.ExecContext(ctx, `INSERT INTO store_settings (store_id, cash_rounding_step) VALUES ($1, $2)
			ON CONFLICT (store_id) DO UPDATE SET cash_rounding_step = EXCLUDED.cash_rounding_step`, storeID, step); err != nil {
			t.Fatal(err)
		}
	}
	cashIn := func(paymentID uuid.UUID) int64 {
		var v int64
		_ = migratorDB.GetContext(ctx, &v, `SELECT amount FROM cash_movements WHERE payment_id = $1`, paymentID)
		return v
	}

	t.Run("SeveralDirectTendersCloseTheBill", func(t *testing.T) {
		order := newOrder(2) // 20.520
		if _, err := direct(order.ID, domain.PaymentMethodEDC, amt(0), 0); !errors.Is(err, service.ErrInvalidTenderAmount) {
			t.Fatalf("zero amount must be refused: %v", err)
		}
		if _, err := direct(order.ID, domain.PaymentMethodEDC, amt(order.TotalAmount+1), 0); !errors.Is(err, service.ErrInvalidTenderAmount) {
			t.Fatalf("more than the balance must be refused: %v", err)
		}
		first, err := direct(order.ID, domain.PaymentMethodEDC, amt(5000), 0)
		if err != nil {
			t.Fatal(err)
		}
		if first.Order.PaymentStatus != domain.OrderPaymentStatusPartiallyPaid || first.Order.Status != domain.OrderStatusPendingPayment ||
			first.Split.Status != domain.SplitBillStatusPartiallyPaid || first.Order.PaidAmount != 5000 || first.Order.ExpiresAt != nil {
			t.Fatalf("after a partial tender: order %s/%s paid %d expires %v, split %s", first.Order.Status, first.Order.PaymentStatus,
				first.Order.PaidAmount, first.Order.ExpiresAt, first.Split.Status)
		}
		second, err := direct(order.ID, domain.PaymentMethodCash, amt(6000), 10000)
		if err != nil || second.Payment.ChangeGivenAmount != 4000 || second.Payment.RoundingAmount != 0 {
			t.Fatalf("partial cash: %+v %v", second, err)
		}
		last, err := direct(order.ID, domain.PaymentMethodCash, nil, 20000)
		if err != nil {
			t.Fatal(err)
		}
		if last.Payment.Amount != order.TotalAmount-11000 || last.Order.PaymentStatus != domain.OrderPaymentStatusPaid ||
			last.Order.Status == domain.OrderStatusPendingPayment || last.Order.PaidAmount != order.TotalAmount {
			t.Fatalf("closing tender: amount %d, order %s/%s paid %d", last.Payment.Amount, last.Order.Status, last.Order.PaymentStatus, last.Order.PaidAmount)
		}
		if _, err := direct(order.ID, domain.PaymentMethodCash, nil, 20000); !errors.Is(err, service.ErrSplitAlreadyPaid) {
			t.Fatalf("a paid bill takes no more tenders: %v", err)
		}
	})

	t.Run("PendingGatewayLocksPartialTenders", func(t *testing.T) {
		order := newOrder(1) // 10.260
		if _, err := direct(order.ID, domain.PaymentMethodEDC, amt(3000), 0); err != nil {
			t.Fatal(err)
		}
		if _, err := paySvc.InitiatePayment(ctx, storeID, service.InitiatePaymentInput{OrderID: order.ID, IdempotencyKey: "mp-q-" + uuid.NewString(),
			Method: domain.PaymentMethodQRIS, Amount: amt(1000)}); !errors.Is(err, service.ErrGatewayMustPayRemaining) {
			t.Fatalf("QRIS for less than the remainder must be refused: %v", err)
		}
		qris, err := paySvc.InitiatePayment(ctx, storeID, service.InitiatePaymentInput{OrderID: order.ID, IdempotencyKey: "mp-q-" + uuid.NewString(),
			Method: domain.PaymentMethodQRIS})
		if err != nil || qris.Payment.Amount != order.TotalAmount-3000 {
			t.Fatalf("QRIS must charge exactly the remainder: %+v %v", qris, err)
		}
		if _, err := direct(order.ID, domain.PaymentMethodCash, amt(1000), 1000); !errors.Is(err, service.ErrSplitPaymentPending) {
			t.Fatalf("a pending QRIS must lock further tenders: %v", err)
		}
		res, err := paySvc.ProcessMidtransWebhook(ctx, service.MidtransWebhookInput{
			OrderID: qris.Payment.ProviderOrderID, TransactionID: qris.Payment.TransactionID, StatusCode: "200",
			TransactionStatus: "settlement", GrossAmount: formatGross(qris.Payment.Amount), FraudStatus: "accept", PaymentType: "qris", Currency: "IDR",
		}, `{}`)
		if err != nil || res.Anomaly || res.Status != domain.PaymentStatusPaid {
			t.Fatalf("webhook for the remainder must settle without anomaly: %+v %v", res, err)
		}
		var status string
		var paid int64
		_ = migratorDB.QueryRowxContext(ctx, `SELECT payment_status, paid_amount FROM orders WHERE id = $1`, order.ID).Scan(&status, &paid)
		if status != "paid" || paid != order.TotalAmount {
			t.Fatalf("order after QRIS remainder: %s %d", status, paid)
		}
	})

	t.Run("ClosingCashTenderIsRounded", func(t *testing.T) {
		setRounding(500)
		order := newOrder(1) // 10.260 → 10.500 in cash
		if _, err := direct(order.ID, domain.PaymentMethodCash, nil, 10400); !errors.Is(err, service.ErrInsufficientCashTendered) {
			t.Fatalf("the rounded amount is what must be tendered: %v", err)
		}
		res, err := direct(order.ID, domain.PaymentMethodCash, nil, 20000)
		if err != nil {
			t.Fatal(err)
		}
		if res.Payment.Amount != 10260 || res.Payment.RoundingAmount != 240 || res.Payment.ChangeGivenAmount != 9500 || cashIn(res.Payment.ID) != 10500 {
			t.Fatalf("rounded cash: amount %d rounding %d change %d drawer %d", res.Payment.Amount, res.Payment.RoundingAmount,
				res.Payment.ChangeGivenAmount, cashIn(res.Payment.ID))
		}

		setRounding(100)
		split := newOrder(1) // partial cash is exact; the closing 5.260 rounds to the nearest 100: 5.300
		if res, err := direct(split.ID, domain.PaymentMethodCash, amt(5000), 5000); err != nil || res.Payment.RoundingAmount != 0 {
			t.Fatalf("partial cash must not be rounded: %+v %v", res, err)
		}
		res, err = direct(split.ID, domain.PaymentMethodCash, nil, 5300)
		if err != nil || res.Payment.RoundingAmount != 40 || res.Payment.ChangeGivenAmount != 0 || cashIn(res.Payment.ID) != 5300 {
			t.Fatalf("closing cash with step 100: %+v %v", res, err)
		}

		edcOnly := newOrder(1)
		res, err = direct(edcOnly.ID, domain.PaymentMethodEDC, nil, 0)
		if err != nil || res.Payment.RoundingAmount != 0 || res.Payment.Amount != 10260 {
			t.Fatalf("non-cash is never rounded: %+v %v", res, err)
		}
		setRounding(0)
	})
}

func formatGross(v int64) string { return fmt.Sprintf("%d.00", v) }
