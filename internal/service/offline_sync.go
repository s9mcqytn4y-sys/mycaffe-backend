package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/domain"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/database"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/repository/postgres"
)

// Offline order sync (v0.10 PR 10.2, ADR-005 OF1/OF3/OF6–OF9, W16). A registered device uploads orders
// it took while the internet was down. Each order is processed in its own transaction: re-priced from the
// catalog snapshot it was sold with, given an official number, paid into the shift that was open, and
// recorded as a submission. Anything the server cannot accept is stored as "rejected" with a reason for
// the owner instead of being dropped (OF9). Re-sending the same client_order_id returns the first result.

const (
	offlineMaxBatch = 50
	// shortcut: OF7 limits are constants, not store_settings columns yet; move them there when an owner asks to tune them.
	offlineMaxHours  = 12
	offlineMaxOrders = 300
	staleCatalogAge  = 24 * time.Hour
	// offlineClockSkew tolerates a device clock that runs a little off the server clock.
	offlineClockSkew = 5 * time.Minute
)

var (
	ErrSubmissionNotFound   = errors.New("offline submission not found")
	ErrSubmissionResolved   = errors.New("offline submission is not waiting for review")
	errOfflineShiftMismatch = errors.New("the order's shift is not the open shift of its drawer")
	errOfflineCompliment    = errors.New("a 100% discount (compliment) is not allowed offline")
)

type OfflinePaymentInput struct {
	Method             domain.PaymentMethod `json:"method"`
	CashTenderedAmount int64                `json:"cash_tendered_amount"`
	Reference          string               `json:"reference"`
}

// OfflineOrderInput is one order as the device recorded it; prices are never taken from it.
type OfflineOrderInput struct {
	ClientOrderID      uuid.UUID           `json:"client_order_id"`
	OfflineQueueNumber string              `json:"offline_queue_number"`
	OfflineCreatedAt   time.Time           `json:"offline_created_at"`
	CashierID          uuid.UUID           `json:"cashier_id"`
	ShiftID            uuid.UUID           `json:"shift_id"`
	CatalogVersion     string              `json:"catalog_version"`
	OrderType          domain.OrderType    `json:"order_type"`
	CustomerName       string              `json:"customer_name"`
	Notes              string              `json:"notes,omitempty"`
	Items              []OrderItemInput    `json:"items"`
	DiscountBPS        int64               `json:"discount_bps,omitempty"`
	DiscountReason     string              `json:"discount_reason,omitempty"`
	Payment            OfflinePaymentInput `json:"payment"`
	// PrintedLocally: the kitchen tickets were printed on the LAN print agent; the server prints none (W18).
	PrintedLocally bool `json:"printed_locally,omitempty"`
}

// PINFailureInput reports wrong offline PINs typed on the device (OF2), audited on sync.
type PINFailureInput struct {
	UserID   uuid.UUID `json:"user_id"`
	At       time.Time `json:"at"`
	Attempts int       `json:"attempts"`
	Locked   bool      `json:"locked"`
}

type OfflineSyncInput struct {
	Orders      []OfflineOrderInput `json:"orders"`
	PINFailures []PINFailureInput   `json:"pin_failures"`
	// PendingShiftIDs are the shifts that still have orders waiting on the device after this batch; a
	// cashier cannot close them (OF4). Omitted keeps the last report; [] clears it.
	PendingShiftIDs *[]uuid.UUID `json:"pending_shift_ids"`
}

// OfflineSyncResult is the outcome per client_order_id; Status is accepted, rejected, resolved_accepted
// or resolved_rejected.
type OfflineSyncResult struct {
	ClientOrderID uuid.UUID  `json:"client_order_id"`
	Status        string     `json:"status"`
	Reason        string     `json:"reason,omitempty"`
	OrderID       *uuid.UUID `json:"order_id,omitempty"`
	OrderNumber   string     `json:"order_number,omitempty"`
	QueueNumber   string     `json:"queue_number,omitempty"`
}

type OfflineSyncService struct {
	db       *sqlx.DB
	orders   *OrderService
	payments *PaymentService
	nowFunc  func() time.Time
}

func NewOfflineSyncService(db *sqlx.DB, orders *OrderService, payments *PaymentService) *OfflineSyncService {
	return &OfflineSyncService{db: db, orders: orders, payments: payments, nowFunc: time.Now}
}

// Sync processes a batch from a device. A bad credential or revoked device refuses the whole batch.
func (s *OfflineSyncService) Sync(ctx context.Context, credential string, in OfflineSyncInput) ([]OfflineSyncResult, error) {
	storeID, deviceID, secret, ok := parseRefreshToken(credential)
	if !ok {
		return nil, ErrDeviceInvalid
	}
	if len(in.Orders) > offlineMaxBatch || len(in.PINFailures) > offlineMaxBatch {
		return nil, &ValidationError{Fields: map[string]string{"orders": fmt.Sprintf("maksimal %d per sinkronisasi", offlineMaxBatch)}}
	}
	if err := s.recordPINFailures(ctx, storeID, deviceID, secret, in.PINFailures); err != nil {
		return nil, err
	}
	results := make([]OfflineSyncResult, 0, len(in.Orders))
	for _, o := range in.Orders {
		res, err := s.syncOne(ctx, storeID, deviceID, secret, o)
		if err != nil {
			return nil, err
		}
		results = append(results, *res)
	}
	err := database.WithTenantTx(ctx, s.db, storeID, func(tx *sqlx.Tx) error {
		now := s.nowFunc().UTC()
		if _, err := authenticateDeviceTx(ctx, tx, storeID, deviceID, secret, now); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE devices SET last_sync_at = $3 WHERE store_id = $1 AND id = $2`, storeID, deviceID, now); err != nil {
			return err
		}
		if in.PendingShiftIDs == nil {
			return nil
		}
		pending := make([]string, 0, len(*in.PendingShiftIDs))
		for _, id := range *in.PendingShiftIDs {
			pending = append(pending, id.String())
		}
		_, err := tx.ExecContext(ctx, `UPDATE devices SET pending_shift_ids = $3::uuid[] WHERE store_id = $1 AND id = $2`, storeID, deviceID, pending)
		return err
	})
	if err != nil {
		return nil, err
	}
	return results, nil
}

func (s *OfflineSyncService) syncOne(ctx context.Context, storeID, deviceID uuid.UUID, secret string, o OfflineOrderInput) (*OfflineSyncResult, error) {
	var res *OfflineSyncResult
	err := database.WithTenantTx(ctx, s.db, storeID, func(tx *sqlx.Tx) error {
		now := s.nowFunc().UTC()
		device, err := authenticateDeviceTx(ctx, tx, storeID, deviceID, secret, now)
		if err != nil {
			return err
		}
		if o.ClientOrderID == uuid.Nil {
			res = &OfflineSyncResult{Status: "rejected", Reason: "client_order_id wajib diisi"}
			return nil // nothing to key a stored submission on
		}
		if res, err = loadSubmissionResult(ctx, tx, storeID, o.ClientOrderID); err != nil || res != nil {
			return err
		}
		reason, err := s.checkPolicy(ctx, tx, storeID, device, o, now)
		if err != nil {
			return err
		}
		var order *domain.Order
		if reason == "" {
			if order, reason, err = s.placeWithSavepoint(ctx, tx, storeID, deviceID, o, now); err != nil {
				return err
			}
		}
		res, err = s.storeSubmission(ctx, tx, storeID, deviceID, o, order, reason, now)
		return err
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}

// checkPolicy applies the device-side rules (OF1, OF2, OF7, W16) and returns a rejection reason or "".
func (s *OfflineSyncService) checkPolicy(ctx context.Context, tx *sqlx.Tx, storeID uuid.UUID, device *Device, o OfflineOrderInput, now time.Time) (string, error) {
	switch {
	case len(o.Items) == 0:
		return "pesanan tanpa item", nil
	case o.OfflineCreatedAt.IsZero() || o.OfflineCreatedAt.After(now.Add(offlineClockSkew)):
		return "waktu pesanan offline tidak valid", nil
	case !strings.HasPrefix(o.OfflineQueueNumber, device.Prefix+"-") || len(o.OfflineQueueNumber) > 20:
		return "nomor offline harus diawali prefix perangkat " + device.Prefix, nil
	case o.OrderType != domain.OrderTypeTakeaway && o.OrderType != domain.OrderTypeDineIn:
		return "jenis pesanan offline hanya takeaway atau dine_in", nil
	case o.Payment.Method != domain.PaymentMethodCash && o.Payment.Method != domain.PaymentMethodStaticQRIS:
		return "pembayaran offline hanya tunai atau QRIS statis", nil
	case o.Payment.Method == domain.PaymentMethodStaticQRIS && strings.TrimSpace(o.Payment.Reference) == "":
		return "QRIS statis offline wajib menyertakan referensi", nil
	case o.DiscountBPS < 0 || o.DiscountBPS > 10000:
		return "diskon tidak valid", nil
	}
	since := device.CreatedAt
	if device.LastSyncAt != nil {
		since = *device.LastSyncAt
	}
	if o.OfflineCreatedAt.Sub(since) > offlineMaxHours*time.Hour {
		return fmt.Sprintf("melewati batas %d jam sejak sinkronisasi terakhir", offlineMaxHours), nil
	}
	var taken int
	if err := tx.GetContext(ctx, &taken, `SELECT COUNT(*) FROM orders WHERE store_id = $1 AND device_id = $2 AND offline_created_at > $3`,
		storeID, device.ID, since); err != nil {
		return "", err
	}
	if taken >= offlineMaxOrders {
		return fmt.Sprintf("melewati batas %d pesanan sejak sinkronisasi terakhir", offlineMaxOrders), nil
	}
	var verified bool
	if err := tx.GetContext(ctx, &verified, `SELECT EXISTS (SELECT 1 FROM device_pin_verifiers v JOIN users u ON u.store_id = v.store_id AND u.id = v.user_id
		WHERE v.store_id = $1 AND v.device_id = $2 AND v.user_id = $3 AND v.expires_at >= $4 AND u.is_active)`,
		storeID, device.ID, o.CashierID, o.OfflineCreatedAt); err != nil {
		return "", err
	}
	if !verified {
		return "kasir tidak punya verifier PIN yang berlaku di perangkat ini", nil
	}
	return "", nil
}

// placeWithSavepoint creates and pays the order; a business failure rolls back to the savepoint and
// becomes the rejection reason, anything else aborts the sync so the device retries.
func (s *OfflineSyncService) placeWithSavepoint(ctx context.Context, tx *sqlx.Tx, storeID, deviceID uuid.UUID, o OfflineOrderInput, now time.Time) (*domain.Order, string, error) {
	if _, err := tx.ExecContext(ctx, "SAVEPOINT offline_order"); err != nil {
		return nil, "", err
	}
	order, err := s.place(ctx, tx, storeID, deviceID, o, now)
	if err == nil {
		_, err = tx.ExecContext(ctx, "RELEASE SAVEPOINT offline_order")
		return order, "", err
	}
	if !isOfflineRejection(err) {
		return nil, "", err
	}
	if _, rbErr := tx.ExecContext(ctx, "ROLLBACK TO SAVEPOINT offline_order"); rbErr != nil {
		return nil, "", rbErr
	}
	reason := err.Error()
	var validation *ValidationError
	if errors.As(err, &validation) {
		parts := make([]string, 0, len(validation.Fields))
		for field, msg := range validation.Fields {
			parts = append(parts, field+": "+msg)
		}
		sort.Strings(parts)
		reason = strings.Join(parts, "; ")
	}
	return nil, truncate(reason, 200), nil
}

func isOfflineRejection(err error) bool {
	var validation *ValidationError
	if errors.As(err, &validation) {
		return true
	}
	for _, target := range []error{
		errUnknownCatalogVersion, errOfflineShiftMismatch, errOfflineCompliment, ErrNoOpenShift,
		ErrInvalidOrderInput, ErrProductNotFound, ErrProductUnavailable, ErrInvalidModifierSelection, ErrInvalidSubAddonSelection,
		ErrInsufficientStock, ErrIntegerOverflow, ErrIdempotencyConflict, ErrPaymentMethodNotEnabled, ErrUnsupportedPaymentMethod,
		ErrInsufficientCashTendered, ErrReconciliationRefRequired, ErrUnauthorizedCashierTender, ErrInvalidTenderAmount,
		ErrOrderNotPayable, ErrSplitAlreadyPaid, ErrPaymentIdempotencyConflict,
	} {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}

// place re-prices the order from its catalog snapshot (OF3), applies the cashier discount (W16), takes
// the official number (OF6) and pays it into the shift that was open on the drawer (OF4).
func (s *OfflineSyncService) place(ctx context.Context, tx *sqlx.Tx, storeID, deviceID uuid.UUID, o OfflineOrderInput, now time.Time) (*domain.Order, error) {
	var snap struct {
		Snapshot      []byte    `db:"snapshot"`
		FirstServedAt time.Time `db:"first_served_at"`
	}
	err := tx.GetContext(ctx, &snap, `SELECT snapshot, first_served_at FROM catalog_versions WHERE store_id = $1 AND version = $2`, storeID, o.CatalogVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errUnknownCatalogVersion
	}
	if err != nil {
		return nil, err
	}
	pricing, err := newOfflinePricing(snap.Snapshot)
	if err != nil {
		return nil, err
	}
	pricing.at = o.OfflineCreatedAt
	if o.DiscountBPS > pricing.snap.CashierMaxDiscountBPS {
		return nil, &ValidationError{Fields: map[string]string{"discount_bps": "melebihi batas diskon kasir; diskon offline tidak bisa disetujui owner"}}
	}
	var shift struct {
		OpenedAt time.Time  `db:"opened_at"`
		ClosedAt *time.Time `db:"closed_at"`
	}
	err = tx.GetContext(ctx, &shift, `SELECT opened_at, closed_at FROM shifts WHERE store_id = $1 AND id = $2`, storeID, o.ShiftID)
	// The order must have been taken while its shift was open; a shift closed since then still takes it and
	// its Z-report shows as amended (OF4).
	if errors.Is(err, sql.ErrNoRows) || (err == nil && (shift.OpenedAt.After(o.OfflineCreatedAt.Add(offlineClockSkew)) ||
		(shift.ClosedAt != nil && o.OfflineCreatedAt.After(shift.ClosedAt.Add(offlineClockSkew))))) {
		return nil, ErrNoOpenShift
	}
	if err != nil {
		return nil, err
	}

	name := strings.TrimSpace(o.CustomerName)
	if name == "" {
		name = "Offline " + o.OfflineQueueNumber
	}
	cashier := o.CashierID
	created, err := s.orders.createOrderTx(ctx, tx, storeID, OrderCreationInput{
		IdempotencyKey: "offline:" + o.ClientOrderID.String(),
		CustomerName:   name,
		OrderType:      o.OrderType,
		BillingMode:    domain.BillingModePayFirst,
		ActorUserID:    &cashier,
		Notes:          o.Notes,
		Items:          o.Items,
		Offline:        pricing,
	})
	if err != nil {
		return nil, err
	}
	order := created.Order
	if o.DiscountBPS > 0 {
		discount, err := CalculateRoundHalfUpBPS(order.SubtotalAmount, o.DiscountBPS)
		if err != nil {
			return nil, err
		}
		store := domain.Store{TaxBps: pricing.snap.TaxBPS, ServiceBps: pricing.snap.ServiceBPS}
		service, tax, total, err := orderTotals(order.SubtotalAmount, discount, &store)
		if err != nil {
			return nil, err
		}
		if total == 0 {
			return nil, errOfflineCompliment
		}
		reason := strings.TrimSpace(o.DiscountReason)
		if reason == "" {
			reason = "Diskon kasir (offline)"
		}
		if err := persistDiscountTx(ctx, tx, &order, created.Splits[0].ID, discount, service, tax, total, reason, now); err != nil {
			return nil, err
		}
	}

	// Marked before the payment: settling it creates the KDS tickets and print jobs, which skip an
	// order already printed on the LAN agent (W18).
	stale := o.OfflineCreatedAt.Sub(snap.FirstServedAt) > staleCatalogAge
	var delta int64
	if pricing.liveSubtotal != nil {
		delta = order.SubtotalAmount - *pricing.liveSubtotal
	}
	if _, err := tx.ExecContext(ctx, `UPDATE orders SET origin = 'offline', client_order_id = $3, device_id = $4, offline_created_at = $5,
		offline_queue_number = $6, catalog_version = $7, stale_catalog = $8, price_delta = $9, printed_locally = $10 WHERE store_id = $1 AND id = $2`,
		storeID, order.ID, o.ClientOrderID, deviceID, o.OfflineCreatedAt, o.OfflineQueueNumber, o.CatalogVersion, stale, delta, o.PrintedLocally); err != nil {
		return nil, fmt.Errorf("failed to mark offline order: %w", err)
	}

	paid, err := s.payments.directCashierPaymentTx(ctx, tx, storeID, InitiatePaymentInput{
		OrderID:            order.ID,
		IdempotencyKey:     "offline-pay:" + o.ClientOrderID.String(),
		Method:             o.Payment.Method,
		CashTenderedAmount: o.Payment.CashTenderedAmount,
		ReconciliationRef:  strings.TrimSpace(o.Payment.Reference),
		ActorUserID:        &cashier,
		ShiftID:            &o.ShiftID,
	})
	if err != nil {
		return nil, err
	}
	if paid.Payment.ShiftID == nil || *paid.Payment.ShiftID != o.ShiftID {
		return nil, errOfflineShiftMismatch
	}
	if o.Payment.Method == domain.PaymentMethodStaticQRIS {
		if _, err := tx.ExecContext(ctx, `UPDATE payments SET offline_unreconciled = true WHERE store_id = $1 AND id = $2`, storeID, paid.Payment.ID); err != nil {
			return nil, err
		}
	}
	return paid.Order, nil
}

func (s *OfflineSyncService) storeSubmission(ctx context.Context, tx *sqlx.Tx, storeID, deviceID uuid.UUID, o OfflineOrderInput, order *domain.Order, reason string, now time.Time) (*OfflineSyncResult, error) {
	payload, err := json.Marshal(o)
	if err != nil {
		return nil, err
	}
	res := &OfflineSyncResult{ClientOrderID: o.ClientOrderID, Status: "rejected", Reason: reason}
	var orderID *uuid.UUID
	action, auditReason := "OFFLINE_ORDER_REJECTED", "Pesanan offline "+o.OfflineQueueNumber+" ditolak: "+reason
	if order != nil {
		orderID = &order.ID
		res = &OfflineSyncResult{ClientOrderID: o.ClientOrderID, Status: "accepted", OrderID: orderID, OrderNumber: order.OrderNumber, QueueNumber: order.QueueNumber}
		action, auditReason = "OFFLINE_ORDER_SYNCED", "Pesanan offline "+o.OfflineQueueNumber+" menjadi "+order.OrderNumber
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO offline_order_submissions (store_id, device_id, client_order_id, payload, status, reason, order_id, created_at)
		VALUES ($1, $2, $3, $4, $5, NULLIF($6, ''), $7, $8)`, storeID, deviceID, o.ClientOrderID, payload, res.Status, reason, orderID, now); err != nil {
		return nil, fmt.Errorf("failed to store offline submission: %w", err)
	}
	var oversold int
	if orderID != nil {
		if err := tx.GetContext(ctx, &oversold, `SELECT COUNT(*) FROM order_items WHERE store_id = $1 AND order_id = $2 AND oversold`, storeID, *orderID); err != nil {
			return nil, err
		}
	}
	meta, _ := json.Marshal(map[string]any{"client_order_id": o.ClientOrderID, "device_id": deviceID, "offline_queue_number": o.OfflineQueueNumber,
		"order_id": orderID, "oversold_lines": oversold})
	cashier := o.CashierID
	var actorName string
	_ = tx.GetContext(ctx, &actorName, `SELECT full_name FROM users WHERE store_id = $1 AND id = $2`, storeID, cashier)
	actor := &cashier
	if actorName == "" {
		actor, actorName = nil, "Perangkat POS"
	}
	return res, postgres.InsertAuditLog(ctx, tx, &domain.AuditLog{StoreID: storeID, ActorID: actor, ActorName: actorName, Action: action,
		Reason: truncate(auditReason, 500), ReferenceID: o.ClientOrderID.String(), Metadata: string(meta), CreatedAt: now})
}

func loadSubmissionResult(ctx context.Context, tx *sqlx.Tx, storeID, clientOrderID uuid.UUID) (*OfflineSyncResult, error) {
	var row struct {
		Status      string         `db:"status"`
		Reason      sql.NullString `db:"reason"`
		OrderID     *uuid.UUID     `db:"order_id"`
		OrderNumber sql.NullString `db:"order_number"`
		QueueNumber sql.NullString `db:"queue_number"`
	}
	err := tx.GetContext(ctx, &row, `SELECT s.status, s.reason, s.order_id, o.order_number, o.queue_number
		FROM offline_order_submissions s LEFT JOIN orders o ON o.store_id = s.store_id AND o.id = s.order_id
		WHERE s.store_id = $1 AND s.client_order_id = $2`, storeID, clientOrderID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &OfflineSyncResult{ClientOrderID: clientOrderID, Status: row.Status, Reason: row.Reason.String, OrderID: row.OrderID,
		OrderNumber: row.OrderNumber.String, QueueNumber: row.QueueNumber.String}, nil
}

func (s *OfflineSyncService) recordPINFailures(ctx context.Context, storeID, deviceID uuid.UUID, secret string, failures []PINFailureInput) error {
	return database.WithTenantTx(ctx, s.db, storeID, func(tx *sqlx.Tx) error {
		now := s.nowFunc().UTC()
		device, err := authenticateDeviceTx(ctx, tx, storeID, deviceID, secret, now)
		if err != nil {
			return err
		}
		for _, f := range failures {
			var name string
			if err := tx.GetContext(ctx, &name, `SELECT full_name FROM users WHERE store_id = $1 AND id = $2`, storeID, f.UserID); errors.Is(err, sql.ErrNoRows) {
				continue // unknown user: nothing to attribute
			} else if err != nil {
				return err
			}
			userID := f.UserID
			meta, _ := json.Marshal(map[string]any{"device_id": device.ID, "prefix": device.Prefix, "at": f.At, "attempts": f.Attempts, "locked": f.Locked})
			if err := postgres.InsertAuditLog(ctx, tx, &domain.AuditLog{StoreID: storeID, ActorID: &userID, ActorName: name, Action: "DEVICE_PIN_FAILED",
				Reason: fmt.Sprintf("%d PIN salah offline di perangkat %s", f.Attempts, device.Prefix), ReferenceID: device.ID.String(),
				Metadata: string(meta), CreatedAt: now}); err != nil {
				return err
			}
		}
		return nil
	})
}
