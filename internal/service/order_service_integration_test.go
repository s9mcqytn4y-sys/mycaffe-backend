package service_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jmoiron/sqlx"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/config"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/domain"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/database"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/repository/postgres"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/service"
)

func openIntegrationAppDB(t *testing.T) *sqlx.DB {
	t.Helper()
	appDSN := strings.TrimSpace(os.Getenv("TEST_APP_DATABASE_URL"))
	if appDSN == "" {
		t.Skip("TEST_APP_DATABASE_URL not set; skipping PostgreSQL 18 transactional OrderService integration test")
	}
	db, err := database.NewPostgresDB(&config.Config{DatabaseURL: appDSN})
	if err != nil {
		t.Fatalf("failed to connect to PostgreSQL as mycaffe_app: %v", err)
	}
	db.SetMaxOpenConns(6)
	db.SetMaxIdleConns(2)
	t.Cleanup(func() {
		_ = db.Close()
	})
	return db
}

func TestOrderServiceIntegration_EndToEnd(t *testing.T) {
	db := openIntegrationAppDB(t)
	ctx := context.Background()

	storeAID := uuid.MustParse("11111111-1111-4111-8111-111111111111")
	storeBID := uuid.MustParse("22222222-2222-4222-8222-222222222222")

	// Seeded Store A catalog IDs from db/seeds/development.sql:
	// d0000001: Classic Iced Latte (24000 IDR, Milk Selection + Sweetness Level, Oat Milk -> Plant Milk Prep sub-addon)
	latteProdID := uuid.MustParse("d0000001-0000-4000-8000-000000000001")
	oatMilkOptID := uuid.MustParse("20000002-0000-4000-8000-000000000002")     // +8000 IDR
	lessSugarOptID := uuid.MustParse("20000004-0000-4000-8000-000000000004")   // +0 IDR
	coldFoamSubOptID := uuid.MustParse("20000006-0000-4000-8000-000000000006") // +4000 IDR (child of Oat Milk)

	// Seeded Store B product ID:
	storeBProdID := uuid.MustParse("d0000006-0000-4000-8000-000000000006")

	fixedClock := func() time.Time {
		return time.Date(2026, 10, 29, 14, 30, 0, 0, time.UTC)
	}
	repo := postgres.NewOrderRepository()
	svc := service.NewOrderService(db, repo, service.WithClock(fixedClock))

	t.Run("1_PersistedOrderWithSubAddonsAndSnapshotImmutabilityAndOutbox", func(t *testing.T) {
		idemKey := "idem-int-e2e-" + uuid.New().String()[:8]
		input := service.OrderCreationInput{
			IdempotencyKey: idemKey,
			TableNumber:    "Meja 07",
			CustomerName:   "Rina Kartika",
			CustomerPhone:  "081234567800",
			OrderType:      domain.OrderTypeDineIn,
			BillingMode:    domain.BillingModePayFirst,
			Notes:          "Tolong disajikan hangat",
			Items: []service.OrderItemInput{
				{
					ProductID: latteProdID,
					Quantity:  2,
					Notes:     "Extra creamy",
					Modifiers: []service.SelectedModifierInput{
						{
							OptionID: oatMilkOptID,
							Quantity: 1,
							SubAddons: []service.SelectedSubAddonInput{
								{OptionID: coldFoamSubOptID, Quantity: 1},
							},
						},
						{OptionID: lessSugarOptID, Quantity: 1},
					},
				},
			},
		}

		res, err := svc.CreateOrder(ctx, storeAID, input)
		if err != nil {
			t.Fatalf("CreateOrder failed: %v", err)
		}
		if res.IsIdempotentReplay {
			t.Fatal("expected IsIdempotentReplay=false on first creation")
		}

		// Unit base = 24000, modifiers = 8000 (Oat) + 0 (Less Sugar) + 4000 (Cold Foam) = 12000
		// Snapshot unit price = 36000, Qty 2 -> Subtotal = 72000
		// Store A: service_bps = 500 (5% of 72000 = 3600), taxableBase = 75600, tax_bps = 1000 (10% of 75600 = 7560) -> Total = 83160
		if res.Order.SubtotalAmount != 72000 {
			t.Fatalf("expected SubtotalAmount 72000, got %d", res.Order.SubtotalAmount)
		}
		if res.Order.ServiceChargeAmount != 3600 {
			t.Fatalf("expected ServiceChargeAmount 3600, got %d", res.Order.ServiceChargeAmount)
		}
		if res.Order.TaxAmount != 7560 {
			t.Fatalf("expected TaxAmount 7560, got %d", res.Order.TaxAmount)
		}
		if res.Order.TotalAmount != 83160 {
			t.Fatalf("expected TotalAmount 83160, got %d", res.Order.TotalAmount)
		}
		if !strings.HasPrefix(res.Order.OrderNumber, "ORD-20261029-") {
			t.Fatalf("expected OrderNumber prefix ORD-20261029-, got %q", res.Order.OrderNumber)
		}
		if !strings.HasPrefix(res.Order.QueueNumber, "A") {
			t.Fatalf("expected QueueNumber prefix A, got %q", res.Order.QueueNumber)
		}
		if len(res.Order.Items) != 1 {
			t.Fatalf("expected 1 persisted order item, got %d", len(res.Order.Items))
		}
		if len(res.Order.Items[0].Options) != 3 {
			t.Fatalf("expected 3 persisted order_item_options (2 parent + 1 sub-addon), got %d", len(res.Order.Items[0].Options))
		}

		// Verify child option has ParentOrderItemOptionID pointing to the Oat Milk order_item_options row
		var oatOptionRowID uuid.UUID
		var childParentRowID *uuid.UUID
		for _, opt := range res.Order.Items[0].Options {
			if opt.ModifierOptionID != nil && *opt.ModifierOptionID == oatMilkOptID {
				oatOptionRowID = opt.ID
			}
			if opt.ModifierOptionID != nil && *opt.ModifierOptionID == coldFoamSubOptID {
				childParentRowID = opt.ParentOrderItemOptionID
			}
		}
		if oatOptionRowID == uuid.Nil || childParentRowID == nil || *childParentRowID != oatOptionRowID {
			t.Fatalf("expected child sub-addon ParentOrderItemOptionID (%v) to equal Oat Milk row ID (%s)", childParentRowID, oatOptionRowID)
		}

		// Verify initial payment_split and outbox_event in DB inside tenant context
		err = database.WithTenantTx(ctx, db, storeAID, func(tx *sqlx.Tx) error {
			var outbox domain.OutboxEvent
			if err := tx.GetContext(ctx, &outbox, `
				SELECT id, store_id, event_type, aggregate_type, aggregate_id, payload::text AS payload, created_at
				FROM outbox_events
				WHERE store_id = $1 AND id = $2
			`, storeAID, res.OutboxEventID); err != nil {
				return fmt.Errorf("failed to load outbox event: %w", err)
			}
			if outbox.EventType != "order.created" || outbox.AggregateID != res.Order.ID.String() {
				t.Fatalf("unexpected outbox event: %+v", outbox)
			}
			var payloadMap map[string]any
			if err := json.Unmarshal([]byte(outbox.Payload), &payloadMap); err != nil {
				t.Fatalf("invalid outbox JSON payload: %v", err)
			}
			if payloadMap["order_number"] != res.Order.OrderNumber {
				t.Fatalf("expected outbox order_number %q, got %v", res.Order.OrderNumber, payloadMap["order_number"])
			}

			// Mutate catalog product price & name, then verify historical order_items snapshot is unchanged
			if _, err := tx.ExecContext(ctx, `
				UPDATE products SET name = 'Renamed Latte V2', base_price = 99000 WHERE store_id = $1 AND id = $2
			`, storeAID, latteProdID); err != nil {
				return err
			}

			var snap struct {
				SnapshotName   string `db:"snapshot_name"`
				UnitBasePrice  int64  `db:"unit_base_price"`
				SnapshotPrice  int64  `db:"snapshot_price"`
				SubtotalAmount int64  `db:"subtotal_amount"`
			}
			if err := tx.GetContext(ctx, &snap, `
				SELECT snapshot_name, unit_base_price, snapshot_price, subtotal_amount
				FROM order_items
				WHERE store_id = $1 AND id = $2
			`, storeAID, res.Order.Items[0].ID); err != nil {
				return err
			}
			if snap.SnapshotName != "Classic Iced Latte" || snap.UnitBasePrice != 24000 || snap.SnapshotPrice != 36000 || snap.SubtotalAmount != 72000 {
				t.Fatalf("historical snapshot was mutated after catalog update: %+v", snap)
			}

			// Restore product name and price for subsequent tests
			_, err := tx.ExecContext(ctx, `
				UPDATE products SET name = 'Classic Iced Latte', base_price = 24000 WHERE store_id = $1 AND id = $2
			`, storeAID, latteProdID)
			return err
		})
		if err != nil {
			t.Fatalf("DB verification failed: %v", err)
		}

		// 7. Idempotent replay with identical request
		replayRes, err := svc.CreateOrder(ctx, storeAID, input)
		if err != nil {
			t.Fatalf("idempotent replay failed: %v", err)
		}
		if !replayRes.IsIdempotentReplay {
			t.Fatal("expected IsIdempotentReplay=true on duplicate idempotency_key with identical payload")
		}
		if replayRes.Order.ID != res.Order.ID || replayRes.Order.OrderNumber != res.Order.OrderNumber {
			t.Fatalf("expected identical order on replay (%s), got (%s)", res.Order.ID, replayRes.Order.ID)
		}

		// 8. Idempotency conflict with different payload on same idempotency_key
		conflictingInput := input
		conflictingInput.Items = []service.OrderItemInput{
			{
				ProductID: latteProdID,
				Quantity:  3, // changed from 2 to 3
				Modifiers: []service.SelectedModifierInput{
					{
						OptionID: oatMilkOptID,
						Quantity: 1,
						SubAddons: []service.SelectedSubAddonInput{
							{OptionID: coldFoamSubOptID, Quantity: 1},
						},
					},
					{OptionID: lessSugarOptID, Quantity: 1},
				},
			},
		}
		_, conflictErr := svc.CreateOrder(ctx, storeAID, conflictingInput)
		if !errors.Is(conflictErr, service.ErrIdempotencyConflict) {
			t.Fatalf("expected ErrIdempotencyConflict when reusing idempotency_key with modified payload, got: %v", conflictErr)
		}
	})

	t.Run("2_UnavailableProductAndInvalidModifierRejected", func(t *testing.T) {
		unavailProdID := uuid.New()
		err := database.WithTenantTx(ctx, db, storeAID, func(tx *sqlx.Tx) error {
			_, err := tx.ExecContext(ctx, `
				WITH p AS (INSERT INTO products (id, store_id, name, base_price, is_available, track_stock, stock_quantity)
				VALUES ($1, $2, 'Seasonal Sold Out Drink', 30000, false, false, 0)
				RETURNING store_id, id, base_price)
				INSERT INTO product_variants (store_id, product_id, name, price, is_default) SELECT store_id, id, 'Reguler', base_price, true FROM p
			`, unavailProdID, storeAID)
			return err
		})
		if err != nil {
			t.Fatalf("failed to seed unavailable product: %v", err)
		}

		_, err = svc.CreateOrder(ctx, storeAID, service.OrderCreationInput{
			IdempotencyKey: "idem-unavail-" + uuid.New().String()[:8],
			CustomerName:   "Budi",
			OrderType:      domain.OrderTypeDineIn,
			Items: []service.OrderItemInput{
				{ProductID: unavailProdID, Quantity: 1},
			},
		})
		if !errors.Is(err, service.ErrProductUnavailable) {
			t.Fatalf("expected ErrProductUnavailable, got: %v", err)
		}

		// Missing required modifier groups on Classic Iced Latte
		_, err = svc.CreateOrder(ctx, storeAID, service.OrderCreationInput{
			IdempotencyKey: "idem-missmod-" + uuid.New().String()[:8],
			CustomerName:   "Budi",
			OrderType:      domain.OrderTypeDineIn,
			Items: []service.OrderItemInput{
				{ProductID: latteProdID, Quantity: 1},
			},
		})
		if !errors.Is(err, service.ErrInvalidModifierSelection) {
			t.Fatalf("expected ErrInvalidModifierSelection for missing required modifiers, got: %v", err)
		}
	})

	t.Run("3_InsufficientStockAndConcurrentStockRaceCondition", func(t *testing.T) {
		raceProdID := uuid.New()
		err := database.WithTenantTx(ctx, db, storeAID, func(tx *sqlx.Tx) error {
			_, err := tx.ExecContext(ctx, `
				WITH p AS (INSERT INTO products (id, store_id, name, base_price, is_available, track_stock, stock_quantity)
				VALUES ($1, $2, 'Limited Geisha Pour Over', 45000, true, true, 3)
				RETURNING store_id, id, base_price)
				INSERT INTO product_variants (store_id, product_id, name, price, is_default) SELECT store_id, id, 'Reguler', base_price, true FROM p
			`, raceProdID, storeAID)
			return err
		})
		if err != nil {
			t.Fatalf("failed to seed limited stock product: %v", err)
		}

		// Requesting 5 when only 3 in stock must fail immediately without changing stock
		_, err = svc.CreateOrder(ctx, storeAID, service.OrderCreationInput{
			IdempotencyKey: "idem-excess-" + uuid.New().String()[:8],
			CustomerName:   "Excess Buyer",
			OrderType:      domain.OrderTypeTakeaway,
			Items: []service.OrderItemInput{
				{ProductID: raceProdID, Quantity: 5},
			},
		})
		if !errors.Is(err, service.ErrInsufficientStock) {
			t.Fatalf("expected ErrInsufficientStock, got: %v", err)
		}

		// Launch 10 concurrent goroutines each buying 1 unit of the 3 remaining units
		const workers = 10
		var wg sync.WaitGroup
		var successCount int32
		var insufficientCount int32
		var otherErrCount int32
		orderNumbers := make(chan string, workers)

		startGate := make(chan struct{})
		for i := 0; i < workers; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				<-startGate
				res, err := svc.CreateOrder(ctx, storeAID, service.OrderCreationInput{
					IdempotencyKey: fmt.Sprintf("race-%s-%d", raceProdID.String()[:8], idx),
					CustomerName:   fmt.Sprintf("Concurrent Customer %d", idx),
					OrderType:      domain.OrderTypeTakeaway,
					Items: []service.OrderItemInput{
						{ProductID: raceProdID, Quantity: 1},
					},
				})
				if err == nil {
					atomic.AddInt32(&successCount, 1)
					orderNumbers <- res.Order.OrderNumber
				} else if errors.Is(err, service.ErrInsufficientStock) {
					atomic.AddInt32(&insufficientCount, 1)
				} else {
					t.Logf("unexpected concurrent error: %v", err)
					atomic.AddInt32(&otherErrCount, 1)
				}
			}(i)
		}
		close(startGate)
		wg.Wait()
		close(orderNumbers)

		if successCount != 3 || insufficientCount != 7 || otherErrCount != 0 {
			t.Fatalf("expected 3 successes, 7 ErrInsufficientStock, 0 other errors; got success=%d insufficient=%d other=%d",
				successCount, insufficientCount, otherErrCount)
		}

		seenOrderNums := make(map[string]bool)
		for on := range orderNumbers {
			if seenOrderNums[on] {
				t.Fatalf("duplicate order_number generated under concurrency: %s", on)
			}
			seenOrderNums[on] = true
		}

		// Verify final stock_quantity in DB is exactly 0
		var remainingStock int
		err = database.WithTenantTx(ctx, db, storeAID, func(tx *sqlx.Tx) error {
			return tx.GetContext(ctx, &remainingStock, `SELECT stock_quantity FROM products WHERE store_id = $1 AND id = $2`, storeAID, raceProdID)
		})
		if err != nil || remainingStock != 0 {
			t.Fatalf("expected remaining stock 0 after concurrent race, got %d (err=%v)", remainingStock, err)
		}
	})

	t.Run("4_CrossTenantAccessBlockedByRLS", func(t *testing.T) {
		// Store A attempting to order Store B's product must fail with ErrProductNotFound
		_, err := svc.CreateOrder(ctx, storeAID, service.OrderCreationInput{
			IdempotencyKey: "idem-xtenant-" + uuid.New().String()[:8],
			CustomerName:   "Cross Tenant Attacker",
			OrderType:      domain.OrderTypeDineIn,
			Items: []service.OrderItemInput{
				{ProductID: storeBProdID, Quantity: 1},
			},
		})
		if !errors.Is(err, service.ErrProductNotFound) {
			t.Fatalf("expected ErrProductNotFound when Store A requests Store B product, got: %v", err)
		}

		// Non-existent store UUID must fail with ErrCrossTenantAccess
		_, err = svc.CreateOrder(ctx, uuid.New(), service.OrderCreationInput{
			IdempotencyKey: "idem-ghost-" + uuid.New().String()[:8],
			CustomerName:   "Ghost Store",
			OrderType:      domain.OrderTypeDineIn,
			Items: []service.OrderItemInput{
				{ProductID: storeBProdID, Quantity: 1},
			},
		})
		if !errors.Is(err, service.ErrCrossTenantAccess) {
			t.Fatalf("expected ErrCrossTenantAccess for unknown store context, got: %v", err)
		}

		// Verify Store B cannot read any of Store A's orders under RLS
		err = database.WithTenantTx(ctx, db, storeBID, func(tx *sqlx.Tx) error {
			var count int
			if err := tx.GetContext(ctx, &count, `SELECT COUNT(*) FROM orders WHERE store_id = $1`, storeAID); err != nil {
				return err
			}
			if count != 0 {
				t.Fatalf("RLS violation: Store B saw %d orders belonging to Store A", count)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("Store B RLS verification failed: %v", err)
		}
	})

	t.Run("5_FullTransactionRollbackOnIntermediateFailure", func(t *testing.T) {
		rollbackProdID := uuid.New()
		err := database.WithTenantTx(ctx, db, storeAID, func(tx *sqlx.Tx) error {
			_, err := tx.ExecContext(ctx, `
				WITH p AS (INSERT INTO products (id, store_id, name, base_price, is_available, track_stock, stock_quantity)
				VALUES ($1, $2, 'Rollback Test Croissant', 25000, true, true, 10)
				RETURNING store_id, id, base_price)
				INSERT INTO product_variants (store_id, product_id, name, price, is_default) SELECT store_id, id, 'Reguler', base_price, true FROM p
			`, rollbackProdID, storeAID)
			return err
		})
		if err != nil {
			t.Fatalf("failed to seed rollback test product: %v", err)
		}

		var seqBefore int
		err = database.WithTenantTx(ctx, db, storeAID, func(tx *sqlx.Tx) error {
			return tx.GetContext(ctx, &seqBefore, `
				SELECT COALESCE(MAX(last_seq), 0) FROM store_daily_sequences WHERE store_id = $1 AND business_date = '2026-10-29'
			`, storeAID)
		})
		if err != nil {
			t.Fatalf("failed to read sequence before rollback test: %v", err)
		}

		simulatedBoom := errors.New("simulated outbox serialization failure")
		failingSvc := service.NewOrderService(
			db,
			repo,
			service.WithClock(fixedClock),
			service.WithBeforeOutboxHook(func(ctx context.Context, tx *sqlx.Tx) error {
				return simulatedBoom
			}),
		)

		idemKey := "rollback-idem-" + uuid.New().String()[:8]
		_, err = failingSvc.CreateOrder(ctx, storeAID, service.OrderCreationInput{
			IdempotencyKey: idemKey,
			CustomerName:   "Rollback Customer",
			OrderType:      domain.OrderTypeDineIn,
			Items: []service.OrderItemInput{
				{ProductID: rollbackProdID, Quantity: 4},
			},
		})
		if !errors.Is(err, simulatedBoom) {
			t.Fatalf("expected simulatedBoom error, got: %v", err)
		}

		// Verify stock is still 10, sequence is still seqBefore, and order with idemKey does not exist
		err = database.WithTenantTx(ctx, db, storeAID, func(tx *sqlx.Tx) error {
			var stockAfter int
			if err := tx.GetContext(ctx, &stockAfter, `SELECT stock_quantity FROM products WHERE store_id = $1 AND id = $2`, storeAID, rollbackProdID); err != nil {
				return err
			}
			if stockAfter != 10 {
				t.Fatalf("expected stock_quantity to roll back to 10, got %d", stockAfter)
			}

			var seqAfter int
			if err := tx.GetContext(ctx, &seqAfter, `
				SELECT COALESCE(MAX(last_seq), 0) FROM store_daily_sequences WHERE store_id = $1 AND business_date = '2026-10-29'
			`, storeAID); err != nil {
				return err
			}
			if seqAfter != seqBefore {
				t.Fatalf("expected store_daily_sequences.last_seq to roll back to %d, got %d", seqBefore, seqAfter)
			}

			var orderCount int
			if err := tx.GetContext(ctx, &orderCount, `SELECT COUNT(*) FROM orders WHERE store_id = $1 AND idempotency_key = $2`, storeAID, idemKey); err != nil {
				return err
			}
			if orderCount != 0 {
				t.Fatalf("expected 0 orders committed after rollback, got %d", orderCount)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("rollback verification failed: %v", err)
		}
	})

	t.Run("6_DatabaseCheckConstraintsRejectInvalidStatusValues", func(t *testing.T) {
		err := database.WithTenantTx(ctx, db, storeAID, func(tx *sqlx.Tx) error {
			_, err := tx.ExecContext(ctx, `
				INSERT INTO orders (
					id, store_id, order_number, queue_number, customer_name,
					order_type, billing_mode, status, payment_status, total_amount
				) VALUES (
					$1, $2, $3, 'A999', 'Invalid Status Test',
					'dine_in', 'pay_first', 'queued', 'refund_pending', 25000
				)
			`, uuid.New(), storeAID, "ORD-INVALID-"+uuid.New().String()[:6])
			return err
		})
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "23514" {
			t.Fatalf("expected PostgreSQL CHECK violation 23514 for invalid payment_status 'refund_pending', got: %v", err)
		}
	})

	t.Run("7_StockReservationReleaseAndLateSettlementRecovery", func(t *testing.T) {
		resvProdID := uuid.New()
		err := database.WithTenantTx(ctx, db, storeAID, func(tx *sqlx.Tx) error {
			_, err := tx.ExecContext(ctx, `
				WITH p AS (INSERT INTO products (id, store_id, name, base_price, is_available, track_stock, stock_quantity)
				VALUES ($1, $2, 'Limited Matcha Ceremonial', 35000, true, true, 5)
				RETURNING store_id, id, base_price)
				INSERT INTO product_variants (store_id, product_id, name, price, is_default) SELECT store_id, id, 'Reguler', base_price, true FROM p
			`, resvProdID, storeAID)
			return err
		})
		if err != nil {
			t.Fatalf("failed to seed reservation test product: %v", err)
		}

		res, err := svc.CreateOrder(ctx, storeAID, service.OrderCreationInput{
			IdempotencyKey: "idem-resv-" + uuid.New().String()[:8],
			TableNumber:    "T10",
			CustomerName:   "Dewi Lestari",
			OrderType:      domain.OrderTypeDineIn,
			BillingMode:    domain.BillingModePayFirst,
			Items: []service.OrderItemInput{
				{ProductID: resvProdID, Quantity: 2},
			},
		})
		if err != nil {
			t.Fatalf("CreateOrder for stock reservation failed: %v", err)
		}
		if res.Order.StockStatus != domain.OrderStockStatusReserved {
			t.Fatalf("expected StockStatus='reserved', got %q", res.Order.StockStatus)
		}
		if res.Order.ExpiresAt == nil {
			t.Fatal("expected non-nil ExpiresAt on pending_payment order")
		}

		// Stock should now be 5 - 2 = 3
		var stockNow int
		_ = database.WithTenantTx(ctx, db, storeAID, func(tx *sqlx.Tx) error {
			return tx.GetContext(ctx, &stockNow, `SELECT stock_quantity FROM products WHERE store_id = $1 AND id = $2`, storeAID, resvProdID)
		})
		if stockNow != 3 {
			t.Fatalf("expected reserved stock 3, got %d", stockNow)
		}

		// Expire the unpaid order -> stock must be restored to 5
		cancelledOrder, err := svc.CancelOrExpireUnpaidOrder(ctx, storeAID, res.Order.ID, domain.OrderPaymentStatusExpired, "Midtrans QRIS expired after 15m")
		if err != nil {
			t.Fatalf("CancelOrExpireUnpaidOrder failed: %v", err)
		}
		if cancelledOrder.Status != domain.OrderStatusCancelled || cancelledOrder.StockStatus != domain.OrderStockStatusReleased {
			t.Fatalf("expected status=cancelled & stock_status=released, got status=%s stock_status=%s", cancelledOrder.Status, cancelledOrder.StockStatus)
		}

		_ = database.WithTenantTx(ctx, db, storeAID, func(tx *sqlx.Tx) error {
			return tx.GetContext(ctx, &stockNow, `SELECT stock_quantity FROM products WHERE store_id = $1 AND id = $2`, storeAID, resvProdID)
		})
		if stockNow != 5 {
			t.Fatalf("expected restored stock 5 after expiry, got %d", stockNow)
		}

		// Calling CancelOrExpireUnpaidOrder a second time must be idempotent and NOT double-restore stock to 7
		cancelledAgain, err := svc.CancelOrExpireUnpaidOrder(ctx, storeAID, res.Order.ID, domain.OrderPaymentStatusExpired, "Duplicate expire webhook")
		if err != nil {
			t.Fatalf("second CancelOrExpireUnpaidOrder failed: %v", err)
		}
		if cancelledAgain.StockStatus != domain.OrderStockStatusReleased {
			t.Fatalf("expected StockStatus='released', got %q", cancelledAgain.StockStatus)
		}
		_ = database.WithTenantTx(ctx, db, storeAID, func(tx *sqlx.Tx) error {
			return tx.GetContext(ctx, &stockNow, `SELECT stock_quantity FROM products WHERE store_id = $1 AND id = $2`, storeAID, resvProdID)
		})
		if stockNow != 5 {
			t.Fatalf("idempotency bug: stock was double-restored to %d (expected 5)", stockNow)
		}

		// Simulate late settlement arriving after expiry when stock is still available (5 >= 2)
		paidOrder, err := svc.ConfirmOrderPaymentAndCommitStock(ctx, storeAID, res.Order.ID)
		if err != nil {
			t.Fatalf("ConfirmOrderPaymentAndCommitStock on late payment failed: %v", err)
		}
		if paidOrder.PaymentStatus != domain.OrderPaymentStatusPaid || paidOrder.Status != domain.OrderStatusQueued || paidOrder.StockStatus != domain.OrderStockStatusCommitted {
			t.Fatalf("expected paid+queued+committed, got payment=%s status=%s stock=%s", paidOrder.PaymentStatus, paidOrder.Status, paidOrder.StockStatus)
		}
		_ = database.WithTenantTx(ctx, db, storeAID, func(tx *sqlx.Tx) error {
			return tx.GetContext(ctx, &stockNow, `SELECT stock_quantity FROM products WHERE store_id = $1 AND id = $2`, storeAID, resvProdID)
		})
		if stockNow != 3 {
			t.Fatalf("expected re-deducted stock 3 after late settlement recovery, got %d", stockNow)
		}

		// Once paid, attempting to expire/cancel as unpaid must fail with ErrOrderAlreadyPaid
		_, err = svc.CancelOrExpireUnpaidOrder(ctx, storeAID, res.Order.ID, domain.OrderPaymentStatusExpired, "Late out-of-order expire")
		if !errors.Is(err, service.ErrOrderAlreadyPaid) {
			t.Fatalf("expected ErrOrderAlreadyPaid when expiring an already paid order, got: %v", err)
		}
	})

	t.Run("8_LegacyEmptyRequestHashAndBillingModeGuards", func(t *testing.T) {
		// Ensure a legacy order row with idempotency_key 'idem-seed-order-0001' and empty request_hash '' exists
		err := database.WithTenantTx(ctx, db, storeAID, func(tx *sqlx.Tx) error {
			_, execErr := tx.ExecContext(ctx, `
				INSERT INTO orders (
					id, store_id, idempotency_key, request_hash, order_number, queue_number, table_number,
					customer_name, customer_phone, order_type, billing_mode, status, payment_status,
					subtotal_amount, discount_amount, tax_amount, service_charge_amount, total_amount, paid_amount
				) VALUES (
					'e0000001-0000-4000-8000-000000000001', $1, 'idem-seed-order-0001', '',
					'ORD-LEGACY-0001', 'A98', 'Meja 04', 'Alexander Budi', '081311223344',
					'dine_in', 'pay_first', 'served', 'paid', 50000, 0, 5000, 2500, 57500, 57500
				) ON CONFLICT (id) DO NOTHING`, storeAID)
			return execErr
		})
		if err != nil {
			t.Fatalf("failed to insert legacy empty request_hash fixture: %v", err)
		}

		_, err = svc.CreateOrder(ctx, storeAID, service.OrderCreationInput{
			IdempotencyKey: "idem-seed-order-0001",
			TableNumber:    "Meja 04",
			CustomerName:   "Alexander Budi",
			OrderType:      domain.OrderTypeDineIn,
			Items: []service.OrderItemInput{
				{
					ProductID: latteProdID,
					Quantity:  1,
					Modifiers: []service.SelectedModifierInput{
						{OptionID: lessSugarOptID, Quantity: 1},
						{
							OptionID:  oatMilkOptID,
							Quantity:  1,
							SubAddons: []service.SelectedSubAddonInput{{OptionID: coldFoamSubOptID, Quantity: 1}},
						},
					},
				},
			},
		})
		if !errors.Is(err, service.ErrIdempotencyConflict) {
			t.Fatalf("expected ErrIdempotencyConflict on legacy order with empty request_hash, got: %v", err)
		}

		// open_tab with Barista role (a0000004) must fail with ErrUnauthorizedBillingMode
		baristaID := uuid.MustParse("a0000004-0000-4000-8000-000000000004")
		_, err = svc.CreateOrder(ctx, storeAID, service.OrderCreationInput{
			IdempotencyKey: "idem-opentab-barista-" + uuid.New().String()[:8],
			TableNumber:    "Meja 02",
			CustomerName:   "Tamu Open Tab",
			OrderType:      domain.OrderTypeDineIn,
			BillingMode:    domain.BillingModeOpenTab,
			ActorUserID:    &baristaID,
			Items: []service.OrderItemInput{
				{
					ProductID: latteProdID,
					Quantity:  1,
					Modifiers: []service.SelectedModifierInput{
						{OptionID: lessSugarOptID, Quantity: 1},
						{
							OptionID:  oatMilkOptID,
							Quantity:  1,
							SubAddons: []service.SelectedSubAddonInput{{OptionID: coldFoamSubOptID, Quantity: 1}},
						},
					},
				},
			},
		})
		if !errors.Is(err, service.ErrUnauthorizedBillingMode) {
			t.Fatalf("expected ErrUnauthorizedBillingMode when barista initiates open_tab, got: %v", err)
		}

		// open_tab with Cashier role (a0000003) must succeed and start in 'queued' status
		cashierID := uuid.MustParse("a0000003-0000-4000-8000-000000000003")
		openTabRes, err := svc.CreateOrder(ctx, storeAID, service.OrderCreationInput{
			IdempotencyKey: "idem-opentab-cashier-" + uuid.New().String()[:8],
			TableNumber:    "Meja 02",
			CustomerName:   "Tamu VIP Open Tab",
			OrderType:      domain.OrderTypeDineIn,
			BillingMode:    domain.BillingModeOpenTab,
			ActorUserID:    &cashierID,
			Items: []service.OrderItemInput{
				{
					ProductID: latteProdID,
					Quantity:  1,
					Modifiers: []service.SelectedModifierInput{
						{OptionID: lessSugarOptID, Quantity: 1},
						{
							OptionID:  oatMilkOptID,
							Quantity:  1,
							SubAddons: []service.SelectedSubAddonInput{{OptionID: coldFoamSubOptID, Quantity: 1}},
						},
					},
				},
			},
		})
		if err != nil {
			t.Fatalf("expected open_tab with active cashier to succeed, got: %v", err)
		}
		if openTabRes.Order.Status != domain.OrderStatusQueued || openTabRes.Order.CreatedByUserID == nil || *openTabRes.Order.CreatedByUserID != cashierID {
			t.Fatalf("expected open_tab order status=queued and CreatedByUserID=%s, got status=%s actor=%v",
				cashierID, openTabRes.Order.Status, openTabRes.Order.CreatedByUserID)
		}
	})
}
