package service_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/midtrans/midtrans-go/coreapi"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/domain"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/database"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/midtrans"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/repository/postgres"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/service"
)

type fakeGatewayClient struct {
	mu          sync.Mutex
	chargeCalls int
	checkCalls  int
	chargeFunc  func(ctx context.Context, input midtrans.ChargeRequest) (*coreapi.ChargeResponse, error)
	checkFunc   func(ctx context.Context, providerOrderID string) (*coreapi.TransactionStatusResponse, error)
}

func (f *fakeGatewayClient) ChargeCoreAPI(ctx context.Context, input midtrans.ChargeRequest) (*coreapi.ChargeResponse, error) {
	f.mu.Lock()
	f.chargeCalls++
	fn := f.chargeFunc
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, input)
	}
	return &coreapi.ChargeResponse{
		StatusCode:        "201",
		StatusMessage:     "Success, QRIS transaction is created",
		TransactionID:     "midtrans-tx-" + input.OrderID[:8],
		OrderID:           input.OrderID,
		GrossAmount:       fmt.Sprintf("%d.00", input.Amount),
		PaymentType:       string(input.Method),
		TransactionStatus: "pending",
		FraudStatus:       "accept",
		Acquirer:          "gopay",
		QRString:          "00020101021226610014COM.GO-JEK.WWW011893600914" + input.OrderID[:8],
		ExpiryTime:        "2026-10-29 21:45:00",
	}, nil
}

func (f *fakeGatewayClient) CheckTransaction(ctx context.Context, providerOrderID string) (*coreapi.TransactionStatusResponse, error) {
	f.mu.Lock()
	f.checkCalls++
	fn := f.checkFunc
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, providerOrderID)
	}
	return nil, errors.New("midtrans status lookup failed (status 404): Transaction doesn't exist")
}

func TestPaymentService_UnitValidationGuards(t *testing.T) {
	ctx := context.Background()
	storeID := uuid.New()
	orderID := uuid.New()
	actorID := uuid.New()

	// Uninitialized DB guard
	nilSvc := service.NewPaymentService(nil, nil, nil, nil)
	if _, err := nilSvc.InitiatePayment(ctx, storeID, service.InitiatePaymentInput{
		OrderID:        orderID,
		IdempotencyKey: "idem-1",
		Method:         domain.PaymentMethodQRIS,
	}); err == nil {
		t.Fatal("expected error when PaymentService DB is nil")
	}

	// Create a dummy sqlx.DB pointer so input validation guards execute before any DB connection is used
	dummyDB := &sqlx.DB{}
	gw := &fakeGatewayClient{}
	svc := service.NewPaymentService(dummyDB, postgres.NewPaymentRepository(), postgres.NewOrderRepository(), gw)

	if _, err := svc.InitiatePayment(ctx, uuid.Nil, service.InitiatePaymentInput{
		OrderID:        orderID,
		IdempotencyKey: "idem-1",
		Method:         domain.PaymentMethodQRIS,
	}); !errors.Is(err, postgres.ErrEmptyTenantContext) {
		t.Fatalf("expected ErrEmptyTenantContext, got %v", err)
	}

	if _, err := svc.InitiatePayment(ctx, storeID, service.InitiatePaymentInput{
		OrderID:        orderID,
		IdempotencyKey: "   ",
		Method:         domain.PaymentMethodQRIS,
	}); !errors.Is(err, service.ErrPaymentIdempotencyKeyRequired) {
		t.Fatalf("expected ErrPaymentIdempotencyKeyRequired, got %v", err)
	}

	if _, err := svc.InitiatePayment(ctx, storeID, service.InitiatePaymentInput{
		OrderID:        orderID,
		IdempotencyKey: "idem-unsupported",
		Method:         domain.PaymentMethod("crypto"),
	}); !errors.Is(err, service.ErrUnsupportedPaymentMethod) {
		t.Fatalf("expected ErrUnsupportedPaymentMethod, got %v", err)
	}

	if _, err := svc.InitiatePayment(ctx, storeID, service.InitiatePaymentInput{
		OrderID:        orderID,
		IdempotencyKey: "idem-cash-no-actor",
		Method:         domain.PaymentMethodCash,
	}); !errors.Is(err, service.ErrUnauthorizedCashierTender) {
		t.Fatalf("expected ErrUnauthorizedCashierTender when ActorUserID is nil, got %v", err)
	}

	if _, err := svc.InitiatePayment(ctx, storeID, service.InitiatePaymentInput{
		OrderID:        orderID,
		IdempotencyKey: "idem-static-qris-no-ref",
		Method:         domain.PaymentMethodStaticQRIS,
		ActorUserID:    &actorID,
	}); !errors.Is(err, service.ErrReconciliationRefRequired) {
		t.Fatalf("expected ErrReconciliationRefRequired for static_qris without ref, got %v", err)
	}

	if _, err := svc.InitiatePayment(ctx, storeID, service.InitiatePaymentInput{
		OrderID:        orderID,
		IdempotencyKey: "idem-edc-no-ref",
		Method:         domain.PaymentMethodEDC,
		ActorUserID:    &actorID,
	}); !errors.Is(err, service.ErrReconciliationRefRequired) {
		t.Fatalf("expected ErrReconciliationRefRequired for edc without ref, got %v", err)
	}

	if _, err := svc.InitiatePayment(ctx, storeID, service.InitiatePaymentInput{
		OrderID:        orderID,
		IdempotencyKey: "idem-va-bad-bank",
		Method:         domain.PaymentMethodBankTransfer,
		Bank:           "unknown_bank",
	}); !errors.Is(err, service.ErrUnsupportedPaymentMethod) {
		t.Fatalf("expected ErrUnsupportedPaymentMethod for unsupported bank, got %v", err)
	}

	// Webhook validation guards
	if _, err := svc.ProcessMidtransWebhook(ctx, service.MidtransWebhookInput{
		OrderID:           uuid.NewString(),
		StatusCode:        "200",
		TransactionStatus: "settlement",
		GrossAmount:       "25000.00",
		Currency:          "USD",
	}, "{}"); !errors.Is(err, service.ErrInvalidMidtransWebhook) {
		t.Fatalf("expected ErrInvalidMidtransWebhook for USD currency, got %v", err)
	}

	if _, err := svc.ProcessMidtransWebhook(ctx, service.MidtransWebhookInput{
		OrderID:           uuid.NewString(),
		StatusCode:        "201",
		TransactionStatus: "settlement",
		GrossAmount:       "25000.00",
		Currency:          "IDR",
	}, "{}"); !errors.Is(err, service.ErrInvalidMidtransWebhook) {
		t.Fatalf("expected ErrInvalidMidtransWebhook for settlement with status_code 201, got %v", err)
	}
}

func TestPaymentServiceIntegration_EndToEnd(t *testing.T) {
	db := openIntegrationAppDB(t)
	ctx := context.Background()

	storeAID := uuid.MustParse("11111111-1111-4111-8111-111111111111")
	latteProdID := uuid.MustParse("d0000001-0000-4000-8000-000000000001")
	oatMilkOptID := uuid.MustParse("20000002-0000-4000-8000-000000000002")
	lessSugarOptID := uuid.MustParse("20000004-0000-4000-8000-000000000004")
	coldFoamSubOptID := uuid.MustParse("20000006-0000-4000-8000-000000000006")

	cashierID := uuid.MustParse("a0000003-0000-4000-8000-000000000003")
	baristaID := uuid.MustParse("a0000004-0000-4000-8000-000000000004")

	currentNow := time.Date(2026, 10, 29, 14, 30, 0, 0, time.UTC)
	clock := func() time.Time {
		return currentNow
	}

	orderRepo := postgres.NewOrderRepository()
	paymentRepo := postgres.NewPaymentRepository()
	orderSvc := service.NewOrderService(db, orderRepo, service.WithClock(clock))
	gw := &fakeGatewayClient{}
	shiftRepo := postgres.NewShiftRepository()
	paymentSvc := service.NewPaymentService(db, paymentRepo, orderRepo, gw, service.WithPaymentClock(clock), service.WithShiftRepository(shiftRepo))

	// Ensure an active shift exists for the test cashier in storeAID
	drawerID := uuid.New()
	shiftID := uuid.New()
	err := database.WithTenantTx(ctx, db, storeAID, func(tx *sqlx.Tx) error {
		_ = shiftRepo.InsertCashDrawer(ctx, tx, &domain.CashDrawer{
			ID:        drawerID,
			StoreID:   storeAID,
			Name:      "Integration Test Cash Drawer",
			IsActive:  true,
			CreatedAt: currentNow,
			UpdatedAt: currentNow,
		})
		return shiftRepo.InsertShift(ctx, tx, &domain.Shift{
			ID:           shiftID,
			StoreID:      storeAID,
			DrawerID:     drawerID,
			OpenedBy:     cashierID,
			OpeningFloat: 100000,
			OpenedAt:     currentNow,
			Status:       domain.ShiftStatusOpen,
			CreatedAt:    currentNow,
			UpdatedAt:    currentNow,
		})
	})
	if err != nil {
		t.Fatalf("failed to setup test shift: %v", err)
	}

	createStandardOrder := func(t *testing.T, suffix string) *service.CreateOrderResult {
		t.Helper()
		res, err := orderSvc.CreateOrder(ctx, storeAID, service.OrderCreationInput{
			IdempotencyKey: "idem-pay-ord-" + suffix + "-" + uuid.NewString()[:8],
			TableNumber:    "Meja 05",
			CustomerName:   "Pelanggan " + suffix,
			OrderType:      domain.OrderTypeDineIn,
			BillingMode:    domain.BillingModePayFirst,
			Items: []service.OrderItemInput{
				{
					ProductID: latteProdID,
					Quantity:  1,
					Modifiers: []service.SelectedModifierInput{
						{
							OptionID:  oatMilkOptID,
							Quantity:  1,
							SubAddons: []service.SelectedSubAddonInput{{OptionID: coldFoamSubOptID, Quantity: 1}},
						},
						{OptionID: lessSugarOptID, Quantity: 1},
					},
				},
			},
		})
		if err != nil {
			t.Fatalf("failed to create test order (%s): %v", suffix, err)
		}
		return res
	}

	t.Run("1_DirectCashierTender_Cash_StaticQRIS_EDC_RBAC_And_AuditLog", func(t *testing.T) {
		ord := createStandardOrder(t, "cash-1")
		// TotalAmount for 1x Latte + Oat Milk + Cold Foam = 36000 + 5% service (1800) + 10% tax (3780) = 41580 IDR
		if ord.Order.TotalAmount != 41580 {
			t.Fatalf("expected order total 41580, got %d", ord.Order.TotalAmount)
		}

		// 1A. Barista attempting direct cashier tender must fail with ErrUnauthorizedCashierTender
		_, err := paymentSvc.InitiatePayment(ctx, storeAID, service.InitiatePaymentInput{
			OrderID:            ord.Order.ID,
			IdempotencyKey:     "idem-barista-cash-" + uuid.NewString()[:8],
			Method:             domain.PaymentMethodCash,
			CashTenderedAmount: 50000,
			ActorUserID:        &baristaID,
		})
		if !errors.Is(err, service.ErrUnauthorizedCashierTender) {
			t.Fatalf("expected ErrUnauthorizedCashierTender for barista actor, got: %v", err)
		}

		// 1B. Cashier attempting under-tender cash payment must fail with ErrInsufficientCashTendered
		_, err = paymentSvc.InitiatePayment(ctx, storeAID, service.InitiatePaymentInput{
			OrderID:            ord.Order.ID,
			IdempotencyKey:     "idem-undertender-" + uuid.NewString()[:8],
			Method:             domain.PaymentMethodCash,
			CashTenderedAmount: 40000,
			ActorUserID:        &cashierID,
		})
		if !errors.Is(err, service.ErrInsufficientCashTendered) {
			t.Fatalf("expected ErrInsufficientCashTendered, got: %v", err)
		}

		// 1C. Cashier settling cash payment with 50,000 IDR -> change = 8,420 IDR
		cashIdem := "idem-cash-ok-" + uuid.NewString()[:8]
		payRes, err := paymentSvc.InitiatePayment(ctx, storeAID, service.InitiatePaymentInput{
			OrderID:            ord.Order.ID,
			IdempotencyKey:     cashIdem,
			Method:             domain.PaymentMethodCash,
			CashTenderedAmount: 50000,
			ActorUserID:        &cashierID,
			DrawerID:           &drawerID,
		})
		if err != nil {
			t.Fatalf("InitiatePayment (cash) failed: %v", err)
		}
		if payRes.IsIdempotentReplay {
			t.Fatal("expected IsIdempotentReplay=false on first cash payment")
		}
		if payRes.Payment.Status != domain.PaymentStatusPaid || payRes.Payment.CashTenderedAmount != 50000 || payRes.Payment.ChangeGivenAmount != 8420 {
			t.Fatalf("unexpected cash payment state: status=%s tendered=%d change=%d",
				payRes.Payment.Status, payRes.Payment.CashTenderedAmount, payRes.Payment.ChangeGivenAmount)
		}
		if payRes.Order.PaymentStatus != domain.OrderPaymentStatusPaid || payRes.Order.Status != domain.OrderStatusQueued {
			t.Fatalf("expected order paid+queued, got payment=%s status=%s", payRes.Order.PaymentStatus, payRes.Order.Status)
		}
		if !payRes.OpenDrawer {
			t.Error("expected OpenDrawer=true on successful cash tender")
		}
		if payRes.Order.ShiftID == nil || *payRes.Order.ShiftID != shiftID {
			t.Errorf("expected order to be bound to active shift %s, got %v", shiftID, payRes.Order.ShiftID)
		}

		// Verify cash movement in cash_movements ledger
		var mvCount int
		_ = database.WithTenantTx(ctx, db, storeAID, func(tx *sqlx.Tx) error {
			return tx.GetContext(ctx, &mvCount, `
				SELECT COUNT(*) FROM cash_movements
				WHERE store_id = $1 AND shift_id = $2 AND movement_type = 'sale' AND payment_id = $3
			`, storeAID, shiftID, payRes.Payment.ID)
		})
		if mvCount != 1 {
			t.Fatalf("expected 1 cash_movements 'sale' record, got %d", mvCount)
		}

		// 1D. Idempotent replay with identical payload returns the same payment
		replayRes, err := paymentSvc.InitiatePayment(ctx, storeAID, service.InitiatePaymentInput{
			OrderID:            ord.Order.ID,
			IdempotencyKey:     cashIdem,
			Method:             domain.PaymentMethodCash,
			CashTenderedAmount: 50000,
			ActorUserID:        &cashierID,
			DrawerID:           &drawerID,
		})
		if err != nil {
			t.Fatalf("InitiatePayment replay failed: %v", err)
		}
		if !replayRes.IsIdempotentReplay || replayRes.Payment.ID != payRes.Payment.ID {
			t.Fatalf("expected idempotent replay of payment %s, got replay=%v id=%s",
				payRes.Payment.ID, replayRes.IsIdempotentReplay, replayRes.Payment.ID)
		}
		if !replayRes.OpenDrawer {
			t.Error("expected OpenDrawer=true on idempotent cash replay")
		}

		// 1E. Conflicting payload with same idempotency_key returns ErrPaymentIdempotencyConflict
		_, err = paymentSvc.InitiatePayment(ctx, storeAID, service.InitiatePaymentInput{
			OrderID:            ord.Order.ID,
			IdempotencyKey:     cashIdem,
			Method:             domain.PaymentMethodCash,
			CashTenderedAmount: 100000,
			ActorUserID:        &cashierID,
		})
		if !errors.Is(err, service.ErrPaymentIdempotencyConflict) {
			t.Fatalf("expected ErrPaymentIdempotencyConflict, got: %v", err)
		}

		// 1F. Verify audit_logs entry was written for CASH_PAYMENT_RECORDED
		var auditCount int
		_ = database.WithTenantTx(ctx, db, storeAID, func(tx *sqlx.Tx) error {
			return tx.GetContext(ctx, &auditCount, `
				SELECT COUNT(*) FROM audit_logs
				WHERE store_id = $1 AND reference_id = $2 AND action = 'CASH_PAYMENT_RECORDED'
			`, storeAID, ord.Order.OrderNumber)
		})
		if auditCount != 1 {
			t.Fatalf("expected 1 CASH_PAYMENT_RECORDED audit log row, got %d", auditCount)
		}

		// 1G. Cash tender without open shift fails with ErrNoOpenShift
		closedDrawerID := uuid.New()
		_ = database.WithTenantTx(ctx, db, storeAID, func(tx *sqlx.Tx) error {
			return shiftRepo.InsertCashDrawer(ctx, tx, &domain.CashDrawer{
				ID:        closedDrawerID,
				StoreID:   storeAID,
				Name:      "Laci Tertutup Tanpa Shift",
				IsActive:  true,
				CreatedAt: currentNow,
				UpdatedAt: currentNow,
			})
		})
		ordNoShift := createStandardOrder(t, "no-shift")
		_, err = paymentSvc.InitiatePayment(ctx, storeAID, service.InitiatePaymentInput{
			OrderID:            ordNoShift.Order.ID,
			IdempotencyKey:     "idem-no-shift-" + uuid.NewString()[:8],
			Method:             domain.PaymentMethodCash,
			CashTenderedAmount: 50000,
			ActorUserID:        &cashierID,
			DrawerID:           &closedDrawerID,
		})
		if !errors.Is(err, service.ErrNoOpenShift) {
			t.Fatalf("expected ErrNoOpenShift for drawer without open shift, got: %v", err)
		}

		// 1H. Static QRIS reconciliation with cashier actor and reconciliation_ref
		ordStatic := createStandardOrder(t, "static-qris")
		staticRes, err := paymentSvc.InitiatePayment(ctx, storeAID, service.InitiatePaymentInput{
			OrderID:           ordStatic.Order.ID,
			IdempotencyKey:    "idem-static-qris-" + uuid.NewString()[:8],
			Method:            domain.PaymentMethodStaticQRIS,
			ReconciliationRef: "RRN-9988776655",
			ActorUserID:       &cashierID,
		})
		if err != nil {
			t.Fatalf("InitiatePayment (static_qris) failed: %v", err)
		}
		if staticRes.Payment.Status != domain.PaymentStatusPaid || staticRes.Payment.ReconciliationRef != "RRN-9988776655" {
			t.Fatalf("unexpected static_qris payment: %+v", staticRes.Payment)
		}
	})

	t.Run("2_MidtransGateway_2PhaseCharge_Idempotency_And_TimeoutCheckTransactionRecovery", func(t *testing.T) {
		ord := createStandardOrder(t, "qris-gw")
		gw.mu.Lock()
		gw.chargeCalls = 0
		gw.checkCalls = 0
		gw.chargeFunc = nil
		gw.checkFunc = nil
		gw.mu.Unlock()

		qrisIdem := "idem-qris-gw-" + uuid.NewString()[:8]
		res1, err := paymentSvc.InitiatePayment(ctx, storeAID, service.InitiatePaymentInput{
			OrderID:        ord.Order.ID,
			IdempotencyKey: qrisIdem,
			Method:         domain.PaymentMethodQRIS,
		})
		if err != nil {
			t.Fatalf("InitiatePayment (qris) failed: %v", err)
		}
		if res1.Payment.Status != domain.PaymentStatusPending || res1.Payment.ProviderOrderID != res1.Payment.ID.String() {
			t.Fatalf("expected pending payment with ProviderOrderID == ID, got status=%s provider_order_id=%s",
				res1.Payment.Status, res1.Payment.ProviderOrderID)
		}
		if res1.Payment.QRString == "" || res1.Payment.TransactionID == "" {
			t.Fatalf("expected QRString and TransactionID to be populated, got %+v", res1.Payment)
		}

		// Replaying with same idempotency_key must NOT call ChargeCoreAPI again
		resReplay, err := paymentSvc.InitiatePayment(ctx, storeAID, service.InitiatePaymentInput{
			OrderID:        ord.Order.ID,
			IdempotencyKey: qrisIdem,
			Method:         domain.PaymentMethodQRIS,
		})
		if err != nil {
			t.Fatalf("InitiatePayment (qris replay) failed: %v", err)
		}
		if !resReplay.IsIdempotentReplay || resReplay.Payment.ID != res1.Payment.ID {
			t.Fatalf("expected replay of %s, got replay=%v id=%s", res1.Payment.ID, resReplay.IsIdempotentReplay, resReplay.Payment.ID)
		}
		if gw.chargeCalls != 1 {
			t.Fatalf("expected exactly 1 ChargeCoreAPI call after idempotent replay, got %d", gw.chargeCalls)
		}

		// Calling InitiatePayment with a DIFFERENT idempotency_key on the same split and same method (qris)
		// while the existing attempt is still active and unexpired must reuse the active attempt!
		resActiveReuse, err := paymentSvc.InitiatePayment(ctx, storeAID, service.InitiatePaymentInput{
			OrderID:        ord.Order.ID,
			IdempotencyKey: "idem-qris-second-click-" + uuid.NewString()[:8],
			Method:         domain.PaymentMethodQRIS,
		})
		if err != nil {
			t.Fatalf("InitiatePayment active attempt reuse failed: %v", err)
		}
		if !resActiveReuse.IsIdempotentReplay || resActiveReuse.Payment.ID != res1.Payment.ID {
			t.Fatalf("expected active attempt %s to be reused, got %s", res1.Payment.ID, resActiveReuse.Payment.ID)
		}

		// Simulate network timeout on ChargeCoreAPI where CheckTransaction confirms Midtrans created the VA charge
		ordTimeout := createStandardOrder(t, "va-timeout")
		defer func() {
			gw.mu.Lock()
			gw.chargeFunc = nil
			gw.checkFunc = nil
			gw.mu.Unlock()
		}()
		gw.mu.Lock()
		gw.chargeFunc = func(ctx context.Context, input midtrans.ChargeRequest) (*coreapi.ChargeResponse, error) {
			return nil, errors.New("Post \"https://api.sandbox.midtrans.com/v2/charge\": context deadline exceeded")
		}
		gw.checkFunc = func(ctx context.Context, providerOrderID string) (*coreapi.TransactionStatusResponse, error) {
			return &coreapi.TransactionStatusResponse{
				StatusCode:        "201",
				TransactionID:     "tx-recovered-" + providerOrderID[:8],
				OrderID:           providerOrderID,
				GrossAmount:       "41580.00",
				PaymentType:       "bank_transfer",
				TransactionStatus: "pending",
				FraudStatus:       "accept",
				VaNumbers:         []coreapi.VANumber{{Bank: "bca", VANumber: "12345678901"}},
				ExpiryTime:        "2026-10-30 14:30:00",
			}, nil
		}
		gw.mu.Unlock()

		recoveredRes, err := paymentSvc.InitiatePayment(ctx, storeAID, service.InitiatePaymentInput{
			OrderID:        ordTimeout.Order.ID,
			IdempotencyKey: "idem-va-timeout-recover-" + uuid.NewString()[:8],
			Method:         domain.PaymentMethodBankTransfer,
			Bank:           "bca",
		})
		if err != nil {
			t.Fatalf("expected ChargeCoreAPI timeout to recover via CheckTransaction, got: %v", err)
		}
		if recoveredRes.Payment.VANumber != "12345678901" || recoveredRes.Payment.TransactionID == "" {
			t.Fatalf("expected recovered VA number 12345678901, got %+v", recoveredRes.Payment)
		}

		// Reset fake gateway callbacks
		gw.mu.Lock()
		gw.chargeFunc = nil
		gw.checkFunc = nil
		gw.mu.Unlock()
	})

	t.Run("3_ProcessMidtransWebhook_Settlement_Duplicate_OutOfOrder_And_AnomalyQuarantine", func(t *testing.T) {
		ord := createStandardOrder(t, "webhook-1")
		payInit, err := paymentSvc.InitiatePayment(ctx, storeAID, service.InitiatePaymentInput{
			OrderID:        ord.Order.ID,
			IdempotencyKey: "idem-wh-init-" + uuid.NewString()[:8],
			Method:         domain.PaymentMethodQRIS,
		})
		if err != nil {
			t.Fatalf("InitiatePayment failed: %v", err)
		}
		providerOrderID := payInit.Payment.ProviderOrderID

		// 3A. Valid settlement webhook commits payment, split, order, stock, and outbox
		whInput := service.MidtransWebhookInput{
			OrderID:           providerOrderID,
			TransactionID:     payInit.Payment.TransactionID,
			StatusCode:        "200",
			TransactionStatus: "settlement",
			GrossAmount:       "41580.00",
			FraudStatus:       "accept",
			PaymentType:       "qris",
			Currency:          "IDR",
		}
		whRes1, err := paymentSvc.ProcessMidtransWebhook(ctx, whInput, `{"transaction_status":"settlement","status_code":"200"}`)
		if err != nil {
			t.Fatalf("ProcessMidtransWebhook settlement failed: %v", err)
		}
		if whRes1.Duplicate || whRes1.Ignored || whRes1.Anomaly || whRes1.Status != domain.PaymentStatusPaid {
			t.Fatalf("unexpected webhook result: %+v", whRes1)
		}

		// Verify DB state of order and payment
		var dbOrder domain.Order
		_ = database.WithTenantTx(ctx, db, storeAID, func(tx *sqlx.Tx) error {
			o, _, err := orderRepo.GetOrderByIDForUpdate(ctx, tx, storeAID, ord.Order.ID)
			if err != nil {
				return err
			}
			dbOrder = *o
			return nil
		})
		if dbOrder.PaymentStatus != domain.OrderPaymentStatusPaid || dbOrder.Status != domain.OrderStatusQueued {
			t.Fatalf("expected order paid+queued after settlement webhook, got payment=%s status=%s",
				dbOrder.PaymentStatus, dbOrder.Status)
		}

		// 3B. Duplicate webhook delivery is idempotently ignored
		whResDup, err := paymentSvc.ProcessMidtransWebhook(ctx, whInput, `{"transaction_status":"settlement","status_code":"200"}`)
		if err != nil {
			t.Fatalf("ProcessMidtransWebhook duplicate failed: %v", err)
		}
		if !whResDup.Duplicate {
			t.Fatalf("expected Duplicate=true on repeated webhook, got %+v", whResDup)
		}

		// 3C. Out-of-order "pending" or "expire" notification arriving AFTER settlement must consult CheckTransaction and be ignored
		gw.mu.Lock()
		gw.checkFunc = func(ctx context.Context, pOrderID string) (*coreapi.TransactionStatusResponse, error) {
			return &coreapi.TransactionStatusResponse{
				StatusCode:        "200",
				OrderID:           pOrderID,
				TransactionID:     payInit.Payment.TransactionID,
				GrossAmount:       "41580.00",
				TransactionStatus: "settlement",
				FraudStatus:       "accept",
			}, nil
		}
		gw.mu.Unlock()

		whOutOrder, err := paymentSvc.ProcessMidtransWebhook(ctx, service.MidtransWebhookInput{
			OrderID:           providerOrderID,
			TransactionID:     payInit.Payment.TransactionID,
			StatusCode:        "201",
			TransactionStatus: "pending",
			GrossAmount:       "41580.00",
			FraudStatus:       "accept",
			PaymentType:       "qris",
			Currency:          "IDR",
		}, `{"transaction_status":"pending","status_code":"201"}`)
		if err != nil {
			t.Fatalf("ProcessMidtransWebhook out-of-order pending failed: %v", err)
		}
		if !whOutOrder.Ignored || whOutOrder.Status != domain.PaymentStatusPaid {
			t.Fatalf("expected out-of-order pending webhook to be Ignored with status=paid, got %+v", whOutOrder)
		}
		var rawStatusAfter string
		_ = database.WithTenantTx(ctx, db, storeAID, func(tx *sqlx.Tx) error {
			return tx.GetContext(ctx, &rawStatusAfter, `SELECT raw_provider_status FROM payments WHERE store_id = $1 AND id = $2`,
				storeAID, payInit.Payment.ID)
		})
		if rawStatusAfter != "settlement" {
			t.Fatalf("out-of-order webhook overwrote settled payment raw_provider_status: got %q", rawStatusAfter)
		}

		// 3D. Gross amount mismatch on a new pending payment is quarantined as Anomaly without settling order
		ordAnomaly := createStandardOrder(t, "wh-anomaly")
		payAnomaly, err := paymentSvc.InitiatePayment(ctx, storeAID, service.InitiatePaymentInput{
			OrderID:        ordAnomaly.Order.ID,
			IdempotencyKey: "idem-wh-anomaly-" + uuid.NewString()[:8],
			Method:         domain.PaymentMethodQRIS,
		})
		if err != nil {
			t.Fatalf("InitiatePayment for anomaly test failed: %v", err)
		}

		whAnomalyRes, err := paymentSvc.ProcessMidtransWebhook(ctx, service.MidtransWebhookInput{
			OrderID:           payAnomaly.Payment.ProviderOrderID,
			TransactionID:     payAnomaly.Payment.TransactionID,
			StatusCode:        "200",
			TransactionStatus: "settlement",
			GrossAmount:       "10000.00", // Expected 41580.00
			FraudStatus:       "accept",
			PaymentType:       "qris",
			Currency:          "IDR",
		}, `{"transaction_status":"settlement","gross_amount":"10000.00"}`)
		if err != nil {
			t.Fatalf("ProcessMidtransWebhook amount mismatch returned error instead of quarantining anomaly: %v", err)
		}
		if !whAnomalyRes.Anomaly {
			t.Fatalf("expected Anomaly=true on gross_amount mismatch, got %+v", whAnomalyRes)
		}

		_ = database.WithTenantTx(ctx, db, storeAID, func(tx *sqlx.Tx) error {
			o, _, err := orderRepo.GetOrderByIDForUpdate(ctx, tx, storeAID, ordAnomaly.Order.ID)
			if err != nil {
				return err
			}
			if o.PaymentStatus == domain.OrderPaymentStatusPaid {
				t.Fatal("SECURITY FAILURE: order was marked paid despite gross_amount mismatch!")
			}
			return nil
		})
	})

	t.Run("4_ExpireOverdueOrders_And_LateSettlementStockExhaustedSavepoint", func(t *testing.T) {
		limitedProdID := uuid.New()
		err := database.WithTenantTx(ctx, db, storeAID, func(tx *sqlx.Tx) error {
			_, err := tx.ExecContext(ctx, `
				INSERT INTO products (id, store_id, name, base_price, is_available, track_stock, stock_quantity)
				VALUES ($1, $2, 'Single Origin Geisha Reserve', 50000, true, true, 2)
			`, limitedProdID, storeAID)
			return err
		})
		if err != nil {
			t.Fatalf("failed to seed limited stock product: %v", err)
		}

		// Order 1 reserves all 2 units of Single Origin Geisha Reserve (stock becomes 0)
		ord1, err := orderSvc.CreateOrder(ctx, storeAID, service.OrderCreationInput{
			IdempotencyKey: "idem-late-1-" + uuid.NewString()[:8],
			TableNumber:    "Meja 09",
			CustomerName:   "Tamu Geisha 1",
			OrderType:      domain.OrderTypeDineIn,
			BillingMode:    domain.BillingModePayFirst,
			Items:          []service.OrderItemInput{{ProductID: limitedProdID, Quantity: 2}},
		})
		if err != nil {
			t.Fatalf("CreateOrder 1 failed: %v", err)
		}
		pay1, err := paymentSvc.InitiatePayment(ctx, storeAID, service.InitiatePaymentInput{
			OrderID:        ord1.Order.ID,
			IdempotencyKey: "idem-pay-late-1-" + uuid.NewString()[:8],
			Method:         domain.PaymentMethodQRIS,
		})
		if err != nil {
			t.Fatalf("InitiatePayment 1 failed: %v", err)
		}

		// Advance clock by 20 minutes past Order 1's 15-minute reservation deadline
		currentNow = currentNow.Add(20 * time.Minute)
		expiredCount, err := paymentSvc.ExpireOverdueOrders(ctx, storeAID, 50)
		if err != nil {
			t.Fatalf("ExpireOverdueOrders failed: %v", err)
		}
		if expiredCount < 1 {
			t.Fatalf("expected at least 1 overdue order to be expired, got %d", expiredCount)
		}

		// Stock of limitedProdID is now restored to 2! Order 2 buys all 2 units (stock becomes 0 again)
		ord2, err := orderSvc.CreateOrder(ctx, storeAID, service.OrderCreationInput{
			IdempotencyKey: "idem-late-2-" + uuid.NewString()[:8],
			TableNumber:    "Meja 10",
			CustomerName:   "Tamu Geisha 2",
			OrderType:      domain.OrderTypeDineIn,
			BillingMode:    domain.BillingModePayFirst,
			Items:          []service.OrderItemInput{{ProductID: limitedProdID, Quantity: 2}},
		})
		if err != nil {
			t.Fatalf("CreateOrder 2 after stock release failed: %v", err)
		}
		_ = ord2

		// Now a late Midtrans settlement webhook arrives for Order 1 while stock is 0!
		// SAVEPOINT payment_stock_commit must roll back the stock deduction, record payment_status='paid',
		// keep status='cancelled' and stock_status='released', and emit payment.late_settlement_stock_exhausted.
		grossStr := fmt.Sprintf("%d.00", pay1.Payment.Amount)
		lateRes, err := paymentSvc.ProcessMidtransWebhook(ctx, service.MidtransWebhookInput{
			OrderID:           pay1.Payment.ProviderOrderID,
			TransactionID:     pay1.Payment.TransactionID,
			StatusCode:        "200",
			TransactionStatus: "settlement",
			GrossAmount:       grossStr,
			FraudStatus:       "accept",
			PaymentType:       "qris",
			Currency:          "IDR",
		}, `{"transaction_status":"settlement","status_code":"200"}`)
		if err != nil {
			t.Fatalf("ProcessMidtransWebhook on late settlement with exhausted stock failed: %v", err)
		}
		if lateRes.Status != domain.PaymentStatusPaid {
			t.Fatalf("expected late settlement status=paid, got %s", lateRes.Status)
		}

		var lateOrder domain.Order
		var outboxCount int
		_ = database.WithTenantTx(ctx, db, storeAID, func(tx *sqlx.Tx) error {
			o, _, err := orderRepo.GetOrderByIDForUpdate(ctx, tx, storeAID, ord1.Order.ID)
			if err != nil {
				return err
			}
			lateOrder = *o
			return tx.GetContext(ctx, &outboxCount, `
				SELECT COUNT(*) FROM outbox_events
				WHERE store_id = $1 AND aggregate_id = $2 AND event_type = 'payment.late_settlement_stock_exhausted'
			`, storeAID, ord1.Order.ID.String())
		})
		if lateOrder.PaymentStatus != domain.OrderPaymentStatusPaid ||
			lateOrder.Status != domain.OrderStatusCancelled ||
			lateOrder.StockStatus != domain.OrderStockStatusReleased {
			t.Fatalf("expected lateOrder payment=paid, status=cancelled, stock=released; got payment=%s status=%s stock=%s",
				lateOrder.PaymentStatus, lateOrder.Status, lateOrder.StockStatus)
		}
		if outboxCount != 1 {
			t.Fatalf("expected 1 payment.late_settlement_stock_exhausted outbox event, got %d", outboxCount)
		}
	})

	t.Run("5_ReconcileStalePendingPayments_And_RunReconciliationCycle", func(t *testing.T) {
		ord := createStandardOrder(t, "reconcile-worker")
		payInit, err := paymentSvc.InitiatePayment(ctx, storeAID, service.InitiatePaymentInput{
			OrderID:        ord.Order.ID,
			IdempotencyKey: "idem-reconcile-worker-" + uuid.NewString()[:8],
			Method:         domain.PaymentMethodQRIS,
		})
		if err != nil {
			t.Fatalf("InitiatePayment for reconciliation worker failed: %v", err)
		}

		// Configure fake gateway CheckTransaction to return settlement (200) for this payment
		gw.mu.Lock()
		gw.checkFunc = func(ctx context.Context, pOrderID string) (*coreapi.TransactionStatusResponse, error) {
			if pOrderID == payInit.Payment.ProviderOrderID {
				return &coreapi.TransactionStatusResponse{
					StatusCode:        "200",
					OrderID:           pOrderID,
					TransactionID:     payInit.Payment.TransactionID,
					GrossAmount:       fmt.Sprintf("%d.00", payInit.Payment.Amount),
					PaymentType:       "qris",
					TransactionStatus: "settlement",
					FraudStatus:       "accept",
				}, nil
			}
			return nil, errors.New("status 404: not found")
		}
		gw.mu.Unlock()

		// Advance clock by 5 minutes so the pending payment is older than the 1-minute threshold.
		// payments.created_at is set by PostgreSQL NOW() (wall clock), while this test uses a fixed
		// application clock, so align the fixture's created_at with the application clock.
		if err := database.WithTenantTx(ctx, db, storeAID, func(tx *sqlx.Tx) error {
			_, err := tx.ExecContext(ctx, `UPDATE payments SET created_at = $3 WHERE store_id = $1 AND id = $2`,
				storeAID, payInit.Payment.ID, currentNow.Add(-1*time.Minute))
			return err
		}); err != nil {
			t.Fatalf("failed to align payment created_at with test clock: %v", err)
		}
		currentNow = currentNow.Add(5 * time.Minute)
		if err := paymentSvc.RunReconciliationCycle(ctx, 1*time.Minute, 50); err != nil {
			t.Fatalf("RunReconciliationCycle failed: %v", err)
		}

		var reconciledOrder domain.Order
		_ = database.WithTenantTx(ctx, db, storeAID, func(tx *sqlx.Tx) error {
			o, _, err := orderRepo.GetOrderByIDForUpdate(ctx, tx, storeAID, ord.Order.ID)
			if err != nil {
				return err
			}
			reconciledOrder = *o
			return nil
		})
		if reconciledOrder.PaymentStatus != domain.OrderPaymentStatusPaid || reconciledOrder.Status != domain.OrderStatusQueued {
			t.Fatalf("expected reconciled order to be paid+queued, got payment=%s status=%s",
				reconciledOrder.PaymentStatus, reconciledOrder.Status)
		}
	})

	t.Run("6_DuplicateSettlement_QRISPaidAfterCash_FlagsRefundRequired", func(t *testing.T) {
		ord := createStandardOrder(t, "dup-settle")
		qris, err := paymentSvc.InitiatePayment(ctx, storeAID, service.InitiatePaymentInput{
			OrderID:        ord.Order.ID,
			IdempotencyKey: "idem-dup-qris-" + uuid.NewString()[:8],
			Method:         domain.PaymentMethodQRIS,
		})
		if err != nil {
			t.Fatalf("InitiatePayment (qris) failed: %v", err)
		}
		// Customer walks to the cashier and pays cash while the QRIS charge is still open at Midtrans.
		if _, err := paymentSvc.InitiatePayment(ctx, storeAID, service.InitiatePaymentInput{
			OrderID:            ord.Order.ID,
			IdempotencyKey:     "idem-dup-cash-" + uuid.NewString()[:8],
			Method:             domain.PaymentMethodCash,
			CashTenderedAmount: 50000,
			ActorUserID:        &cashierID,
		}); err != nil {
			t.Fatalf("InitiatePayment (cash) failed: %v", err)
		}

		// The customer also completes the QRIS payment: Midtrans sends a signed settlement.
		res, err := paymentSvc.ProcessMidtransWebhook(ctx, service.MidtransWebhookInput{
			OrderID:           qris.Payment.ProviderOrderID,
			TransactionID:     qris.Payment.TransactionID,
			StatusCode:        "200",
			TransactionStatus: "settlement",
			GrossAmount:       fmt.Sprintf("%d.00", qris.Payment.Amount),
			FraudStatus:       "accept",
			PaymentType:       "qris",
			Currency:          "IDR",
		}, `{"transaction_status":"settlement","status_code":"200"}`)
		if err != nil {
			t.Fatalf("ProcessMidtransWebhook failed: %v", err)
		}
		if !res.Anomaly {
			t.Fatalf("expected duplicate settlement to be flagged as anomaly, got %+v", res)
		}

		var refundEvents int
		var paidAmount int64
		if err := database.WithTenantTx(ctx, db, storeAID, func(tx *sqlx.Tx) error {
			if err := tx.GetContext(ctx, &refundEvents, `SELECT COUNT(*) FROM outbox_events WHERE store_id = $1 AND aggregate_id = $2 AND event_type = 'payment.refund_required'`,
				storeAID, ord.Order.ID.String()); err != nil {
				return err
			}
			return tx.GetContext(ctx, &paidAmount, `SELECT paid_amount FROM orders WHERE store_id = $1 AND id = $2`, storeAID, ord.Order.ID)
		}); err != nil {
			t.Fatalf("failed to read duplicate settlement state: %v", err)
		}
		if refundEvents != 1 {
			t.Fatalf("expected exactly 1 payment.refund_required outbox event, got %d", refundEvents)
		}
		if paidAmount != ord.Order.TotalAmount {
			t.Fatalf("order paid_amount must stay %d after duplicate settlement, got %d", ord.Order.TotalAmount, paidAmount)
		}
	})
}
