package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/rbac"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/repository/postgres"
)

var (
	ErrTabLabelInUse          = errors.New("label meja/tab sedang digunakan oleh tab aktif lain")
	ErrTabNotOpen             = errors.New("tab tidak dalam status terbuka")
	ErrTabClosed              = errors.New("tab sudah ditutup untuk pembayaran")
	ErrEmptyRoundItems        = errors.New("ronde pesanan harus memiliki minimal satu item")
	ErrInvalidTabInput        = errors.New("input tab tidak valid")
	ErrTabNotFound            = errors.New("tab tidak ditemukan")
	ErrSplitNotFound          = errors.New("split bill tidak ditemukan")
	ErrInvalidSplitStrategy   = errors.New("strategi split tidak valid, gunakan 'equal' atau 'by_item'")
	ErrInvalidSplitCount      = errors.New("jumlah split harus antara 2 dan 20")
	ErrItemAllocationMismatch = errors.New("alokasi item tidak sesuai dengan kuantitas pesanan")
	ErrTabCannotReopen        = errors.New("tab tidak dapat dibuka kembali")
)

type TabConflictError struct {
	Message         string    `json:"message"`
	ExistingOrderID uuid.UUID `json:"existing_order_id"`
}

func (e *TabConflictError) Error() string {
	return e.Message
}

type TabService struct {
	db       *sqlx.DB
	repo     *postgres.TabRepository
	orderRep *postgres.OrderRepository
	nowFunc  func() time.Time
}

func NewTabService(db *sqlx.DB, repo *postgres.TabRepository, orderRep *postgres.OrderRepository) *TabService {
	if repo == nil {
		repo = postgres.NewTabRepository()
	}
	if orderRep == nil {
		orderRep = postgres.NewOrderRepository()
	}
	return &TabService{
		db:       db,
		repo:     repo,
		orderRep: orderRep,
		nowFunc:  time.Now,
	}
}

// OpenTabInput defines parameters to initialize an open tab.
type OpenTabInput struct {
	IdempotencyKey      string           `json:"idempotency_key"`
	TableNumber         string           `json:"table_number"`
	CustomerName        string           `json:"customer_name"`
	CustomerPhone       string           `json:"customer_phone"`
	TableID             *uuid.UUID       `json:"table_id,omitempty"`
	AllowDuplicateLabel bool             `json:"allow_duplicate_label"`
	Notes               string           `json:"notes"`
	InitialItems        []OrderItemInput `json:"initial_items,omitempty"`
}

type OpenTabResult struct {
	Order              *domain.Order      `json:"order"`
	FirstRound         *domain.OrderRound `json:"first_round,omitempty"`
	IsIdempotentReplay bool               `json:"is_idempotent_replay"`
}

type canonicalItemPayload struct {
	ProductID string   `json:"product_id"`
	VariantID string   `json:"variant_id,omitempty"`
	Quantity  int      `json:"quantity"`
	Notes     string   `json:"notes,omitempty"`
	Modifiers []string `json:"modifiers,omitempty"`
}

type canonicalOpenTabPayload struct {
	StoreID             string                 `json:"store_id"`
	ActorUserID         string                 `json:"actor_user_id"`
	TableNumber         string                 `json:"table_number"`
	CustomerName        string                 `json:"customer_name"`
	CustomerPhone       string                 `json:"customer_phone"`
	Notes               string                 `json:"notes"`
	AllowDuplicateLabel bool                   `json:"allow_duplicate_label"`
	InitialItems        []canonicalItemPayload `json:"initial_items,omitempty"`
}

type canonicalRoundPayload struct {
	StoreID string                 `json:"store_id"`
	OrderID string                 `json:"order_id"`
	Notes   string                 `json:"notes"`
	Items   []canonicalItemPayload `json:"items"`
}

func buildCanonicalItems(items []OrderItemInput) []canonicalItemPayload {
	if len(items) == 0 {
		return nil
	}
	out := make([]canonicalItemPayload, len(items))
	for i, it := range items {
		var varIDStr string
		if it.VariantID != nil && *it.VariantID != uuid.Nil {
			varIDStr = it.VariantID.String()
		}
		mods := make([]string, len(it.Modifiers))
		for mIdx, m := range it.Modifiers {
			mods[mIdx] = fmt.Sprintf("%s:%d", m.OptionID.String(), m.Quantity)
		}
		sort.Strings(mods)

		out[i] = canonicalItemPayload{
			ProductID: it.ProductID.String(),
			VariantID: varIDStr,
			Quantity:  it.Quantity,
			Notes:     strings.TrimSpace(it.Notes),
			Modifiers: mods,
		}
	}
	return out
}

func computeOpenTabRequestHash(storeID uuid.UUID, actorUserID uuid.UUID, in OpenTabInput) string {
	payload := canonicalOpenTabPayload{
		StoreID:             storeID.String(),
		ActorUserID:         actorUserID.String(),
		TableNumber:         strings.TrimSpace(in.TableNumber),
		CustomerName:        strings.TrimSpace(in.CustomerName),
		CustomerPhone:       strings.TrimSpace(in.CustomerPhone),
		Notes:               strings.TrimSpace(in.Notes),
		AllowDuplicateLabel: in.AllowDuplicateLabel,
		InitialItems:        buildCanonicalItems(in.InitialItems),
	}
	raw, _ := json.Marshal(payload)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// OpenTab initializes an open tab order and optionally submits round 1 atomically.
func (s *TabService) OpenTab(ctx context.Context, actor *AuthenticatedStaff, in OpenTabInput) (*OpenTabResult, error) {
	if actor == nil {
		return nil, ErrInvalidSession
	}
	if !actor.Can(rbac.TabManage) {
		return nil, ErrForbidden
	}

	in.TableNumber = strings.TrimSpace(in.TableNumber)
	in.CustomerName = strings.TrimSpace(in.CustomerName)
	in.CustomerPhone = strings.TrimSpace(in.CustomerPhone)
	in.Notes = strings.TrimSpace(in.Notes)
	in.IdempotencyKey = strings.TrimSpace(in.IdempotencyKey)

	label := in.TableNumber
	if label == "" {
		label = in.CustomerName
	}
	if label == "" {
		return nil, fmt.Errorf("%w: table_number atau customer_name wajib diisi sebagai label tab", ErrInvalidTabInput)
	}
	if in.CustomerName == "" {
		in.CustomerName = "Pelanggan " + label
	}
	if in.IdempotencyKey == "" {
		return nil, fmt.Errorf("%w: idempotency_key wajib diisi", ErrInvalidTabInput)
	}
	if len(in.IdempotencyKey) > 100 {
		return nil, fmt.Errorf("%w: idempotency_key exceeds 100 characters", ErrInvalidTabInput)
	}
	if len(in.InitialItems) > 0 {
		if err := ValidateItemInputs(in.InitialItems); err != nil {
			return nil, err
		}
	}

	var result *OpenTabResult

	err := database.WithTenantTx(ctx, s.db, actor.Staff.Store.ID, func(tx *sqlx.Tx) error {
		storeID := actor.Staff.Store.ID
		actorUserID := actor.Staff.UserID
		computedHash := computeOpenTabRequestHash(storeID, actorUserID, in)

		// 1. Transaction-scoped advisory lock on (store_id, idempotency_key) to prevent concurrent OpenTab race
		if err := s.repo.AcquireTabIdempotencyLock(ctx, tx, storeID, in.IdempotencyKey); err != nil {
			return fmt.Errorf("failed to acquire tab idempotency advisory lock: %w", err)
		}

		// 2. Idempotency Check (A2)
		existingOrder, _, err := s.orderRep.GetOrderByIdempotencyKey(ctx, tx, storeID, in.IdempotencyKey)
		if err == nil && existingOrder != nil {
			if existingOrder.RequestHash == "" || existingOrder.RequestHash != computedHash {
				return fmt.Errorf("%w: idempotency_key %q was already used with a different tab payload", ErrIdempotencyConflict, in.IdempotencyKey)
			}
			rounds, err := s.repo.ListRoundsByOrder(ctx, tx, storeID, existingOrder.ID)
			if err != nil {
				return err
			}
			var firstRound *domain.OrderRound
			if len(rounds) > 0 {
				firstRound = &rounds[0]
			}
			result = &OpenTabResult{
				Order:              existingOrder,
				FirstRound:         firstRound,
				IsIdempotentReplay: true,
			}
			return nil
		} else if err != nil && !errors.Is(err, postgres.ErrOrderNotFound) {
			return err
		}

		// 3. Race condition guard on table label via PostgreSQL advisory lock (A3)
		if err := s.repo.AcquireTabLabelLock(ctx, tx, storeID, label); err != nil {
			return fmt.Errorf("failed to acquire tab label advisory lock: %w", err)
		}

		// 4. Label duplicate check (unless allow_duplicate_label = true)
		if !in.AllowDuplicateLabel {
			existingTab, err := s.repo.FindActiveTabByLabel(ctx, tx, storeID, label)
			if err != nil {
				return err
			}
			if existingTab != nil {
				return &TabConflictError{
					Message:         fmt.Sprintf("Label %q sedang digunakan oleh tab aktif lain", label),
					ExistingOrderID: existingTab.ID,
				}
			}
		}

		// 5. Fetch store configuration
		store, err := s.orderRep.GetStoreForOrder(ctx, tx, storeID)
		if err != nil {
			return err
		}
		if !store.IsActive || store.DeletedAt != nil {
			return ErrStoreInactive
		}

		currentTime := s.nowFunc().UTC()
		orderNum, queueNum, err := AllocateOrderIdentity(ctx, tx, s.orderRep, store, storeID, currentTime)
		if err != nil {
			return err
		}

		orderID := uuid.New()
		order := &domain.Order{
			ID:                  orderID,
			StoreID:             storeID,
			IdempotencyKey:      in.IdempotencyKey,
			RequestHash:         computedHash,
			OrderNumber:         orderNum,
			QueueNumber:         queueNum,
			TableNumber:         in.TableNumber,
			CustomerName:        in.CustomerName,
			CustomerPhone:       in.CustomerPhone,
			OrderType:           domain.OrderTypeDineIn,
			BillingMode:         domain.BillingModeOpenTab,
			Status:              domain.OrderStatusOpen,
			PaymentStatus:       domain.OrderPaymentStatusUnpaid,
			StockStatus:         domain.OrderStockStatusReserved,
			CreatedByUserID:     &actorUserID,
			TableID:             in.TableID,
			SubtotalAmount:      0,
			DiscountAmount:      0,
			TaxAmount:           0,
			ServiceChargeAmount: 0,
			TotalAmount:         0,
			PaidAmount:          0,
			RefundedAmount:      0,
			Notes:               in.Notes,
		}

		if err := s.orderRep.InsertOrder(ctx, tx, order); err != nil {
			return err
		}

		var firstRound *domain.OrderRound
		// 6. If initial items exist, submit round 1 in the same transaction
		// Note: Use in.IdempotencyKey directly (without "-r1") to strictly avoid exceeding VARCHAR(100).
		// (uq_order_rounds_idempotency is scoped to store_id + order_id + idempotency_key).
		if len(in.InitialItems) > 0 {
			round, err := s.submitRoundInternal(ctx, tx, store, order, actor, SubmitRoundInput{
				IdempotencyKey: in.IdempotencyKey,
				Notes:          "Ronde 1 (Initial)",
				Items:          in.InitialItems,
			}, 1)
			if err != nil {
				return err
			}
			firstRound = round
		}

		// 7. Audit Log
		auditPayload, _ := json.Marshal(map[string]any{
			"order_id":     order.ID,
			"order_number": order.OrderNumber,
			"table_number": order.TableNumber,
			"has_round_1":  firstRound != nil,
		})
		if err := postgres.InsertAuditLog(ctx, tx, &domain.AuditLog{
			StoreID:     storeID,
			ActorID:     &actorUserID,
			ActorName:   actor.Staff.FullName,
			Action:      "TAB_OPEN",
			Reason:      fmt.Sprintf("Membuka tab meja %q", label),
			ReferenceID: order.OrderNumber,
			Metadata:    string(auditPayload),
			CreatedAt:   currentTime,
		}); err != nil {
			return err
		}

		result = &OpenTabResult{
			Order:      order,
			FirstRound: firstRound,
		}
		return nil
	})

	if err != nil {
		return nil, err
	}
	return result, nil
}

// SubmitRoundInput defines payload for adding a round of items to an open tab.
type SubmitRoundInput struct {
	IdempotencyKey string           `json:"idempotency_key"`
	Notes          string           `json:"notes"`
	Items          []OrderItemInput `json:"items"`
}

type SubmitRoundResult struct {
	Round              *domain.OrderRound `json:"round"`
	Order              *domain.Order      `json:"order"`
	IsIdempotentReplay bool               `json:"is_idempotent_replay"`
}

func computeRoundRequestHash(storeID, orderID uuid.UUID, in SubmitRoundInput) string {
	payload := canonicalRoundPayload{
		StoreID: storeID.String(),
		OrderID: orderID.String(),
		Notes:   strings.TrimSpace(in.Notes),
		Items:   buildCanonicalItems(in.Items),
	}
	raw, _ := json.Marshal(payload)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// SubmitRound appends a new round to an existing open tab.
func (s *TabService) SubmitRound(ctx context.Context, actor *AuthenticatedStaff, orderID uuid.UUID, in SubmitRoundInput) (*SubmitRoundResult, error) {
	if actor == nil {
		return nil, ErrInvalidSession
	}
	if !actor.Can(rbac.TabManage) {
		return nil, ErrForbidden
	}
	in.IdempotencyKey = strings.TrimSpace(in.IdempotencyKey)
	if in.IdempotencyKey == "" {
		return nil, fmt.Errorf("%w: idempotency_key wajib diisi", ErrInvalidTabInput)
	}
	if len(in.IdempotencyKey) > 100 {
		return nil, fmt.Errorf("%w: idempotency_key exceeds 100 characters", ErrInvalidTabInput)
	}
	if err := ValidateItemInputs(in.Items); err != nil {
		return nil, err
	}

	var result *SubmitRoundResult

	err := database.WithTenantTx(ctx, s.db, actor.Staff.Store.ID, func(tx *sqlx.Tx) error {
		storeID := actor.Staff.Store.ID

		order, _, err := s.orderRep.GetOrderByIDForUpdate(ctx, tx, storeID, orderID)
		if err != nil {
			return err
		}

		// 1. Transaction-scoped advisory lock on round idempotency (store_id, order_id, idempotency_key)
		if err := s.repo.AcquireRoundIdempotencyLock(ctx, tx, storeID, orderID, in.IdempotencyKey); err != nil {
			return fmt.Errorf("failed to acquire round idempotency advisory lock: %w", err)
		}

		computedHash := computeRoundRequestHash(storeID, orderID, in)

		// 2. Idempotency Check for Round (A1) BEFORE status check to guarantee replay after tab is closed
		existingRound, err := s.repo.GetRoundByIdempotencyKey(ctx, tx, storeID, orderID, in.IdempotencyKey)
		if err != nil {
			return err
		}
		if existingRound != nil {
			if existingRound.RequestHash == "" || existingRound.RequestHash != computedHash {
				return fmt.Errorf("%w: idempotency_key %q was already used for round #%d with a different payload", ErrIdempotencyConflict, in.IdempotencyKey, existingRound.RoundNumber)
			}
			// Idempotent replay: populate items and return existing state without duplicate stock deductions
			items, err := s.repo.GetOrderItemsWithOptionsByOrder(ctx, tx, storeID, orderID)
			if err != nil {
				return err
			}
			var roundItems []domain.OrderItem
			for _, it := range items {
				if it.RoundID != nil && *it.RoundID == existingRound.ID {
					roundItems = append(roundItems, it)
				}
			}
			existingRound.Items = roundItems
			result = &SubmitRoundResult{
				Round:              existingRound,
				Order:              order,
				IsIdempotentReplay: true,
			}
			return nil
		}

		// 3. Tab Open Guard: New round submissions require the order to still be open
		if order.Status == domain.OrderStatusPendingPayment {
			return ErrTabClosed
		}
		if order.Status != domain.OrderStatusOpen {
			return ErrTabNotOpen
		}

		store, err := s.orderRep.GetStoreForOrder(ctx, tx, storeID)
		if err != nil {
			return err
		}

		nextRoundNo, err := s.repo.GetNextRoundNumber(ctx, tx, storeID, orderID)
		if err != nil {
			return err
		}

		round, err := s.submitRoundInternal(ctx, tx, store, order, actor, in, nextRoundNo)
		if err != nil {
			return err
		}

		// Audit Log
		actorUserID := actor.Staff.UserID
		auditPayload, _ := json.Marshal(map[string]any{
			"order_id":     order.ID,
			"round_id":     round.ID,
			"round_number": round.RoundNumber,
			"items_count":  len(round.Items),
			"new_total":    order.TotalAmount,
		})
		if err := postgres.InsertAuditLog(ctx, tx, &domain.AuditLog{
			StoreID:     storeID,
			ActorID:     &actorUserID,
			ActorName:   actor.Staff.FullName,
			Action:      "ROUND_SUBMIT",
			Reason:      fmt.Sprintf("Menambahkan ronde #%d ke tab pesanan %s", round.RoundNumber, order.OrderNumber),
			ReferenceID: order.OrderNumber,
			Metadata:    string(auditPayload),
			CreatedAt:   s.nowFunc().UTC(),
		}); err != nil {
			return err
		}

		result = &SubmitRoundResult{
			Round: round,
			Order: order,
		}
		return nil
	})

	if err != nil {
		return nil, err
	}
	return result, nil
}

// submitRoundInternal validates pricing snapshot, commits stock, inserts order_items tied to round_id,
// recalculates parent order totals from all cumulative items, and writes outbox event 'order.round_submitted'.
func (s *TabService) submitRoundInternal(
	ctx context.Context,
	tx *sqlx.Tx,
	store *domain.Store,
	order *domain.Order,
	actor *AuthenticatedStaff,
	in SubmitRoundInput,
	roundNumber int,
) (*domain.OrderRound, error) {
	productIDs := make([]uuid.UUID, 0, len(in.Items))
	for _, it := range in.Items {
		productIDs = append(productIDs, it.ProductID)
	}

	catalogData, err := s.orderRep.LockAndLoadCatalogForOrder(ctx, tx, store.ID, productIDs)
	if err != nil {
		return nil, err
	}

	snapshot := catalogSnapshotOf(catalogData, s.nowFunc())

	actorUserID := actor.Staff.UserID
	orderInput := OrderCreationInput{
		IdempotencyKey: in.IdempotencyKey,
		CustomerName:   order.CustomerName,
		OrderType:      order.OrderType,
		BillingMode:    domain.BillingModeOpenTab,
		ActorUserID:    &actorUserID,
		Items:          in.Items,
	}

	pricedOrder, err := ValidateAndPriceOrder(catalogData.Store, orderInput, snapshot)
	if err != nil {
		return nil, err
	}

	// Commit stock deductions for products
	for _, prodID := range sortedUUIDKeysFromIntMap(pricedOrder.ProductStockDeducts) {
		qty := pricedOrder.ProductStockDeducts[prodID]
		if err := s.orderRep.DeductProductStock(ctx, tx, store.ID, prodID, qty); err != nil {
			if errors.Is(err, postgres.ErrStockUpdateFailed) {
				return nil, ErrInsufficientStock
			}
			return nil, err
		}
	}

	// Commit stock deductions for modifier options
	for _, optID := range sortedUUIDKeysFromIntMap(pricedOrder.OptionStockDeducts) {
		qty := pricedOrder.OptionStockDeducts[optID]
		if err := s.orderRep.DeductModifierOptionStock(ctx, tx, store.ID, optID, qty); err != nil {
			if errors.Is(err, postgres.ErrStockUpdateFailed) {
				return nil, ErrInsufficientStock
			}
			return nil, err
		}
	}

	roundID := uuid.New()
	currentTime := s.nowFunc().UTC()
	roundHash := computeRoundRequestHash(store.ID, order.ID, in)

	round := &domain.OrderRound{
		ID:                roundID,
		StoreID:           store.ID,
		OrderID:           order.ID,
		RoundNumber:       roundNumber,
		Status:            domain.OrderRoundStatusQueued,
		IdempotencyKey:    in.IdempotencyKey,
		RequestHash:       roundHash,
		SubmittedByUserID: &actorUserID,
		SubmittedAt:       currentTime,
		Notes:             strings.TrimSpace(in.Notes),
		CreatedAt:         currentTime,
		UpdatedAt:         currentTime,
	}

	if err := s.repo.InsertOrderRound(ctx, tx, round); err != nil {
		return nil, fmt.Errorf("failed to insert order round: %w", err)
	}

	// Persist priced items via shared helper
	persistedItems, err := InsertPricedItems(ctx, tx, s.orderRep, store.ID, order.ID, &roundID, pricedOrder.Items)
	if err != nil {
		return nil, err
	}
	round.Items = persistedItems

	// Each round reaches the stations as its own tickets (decision P2); a round of ready-to-serve items only is ready at once.
	tickets, err := createKDSTicketsTx(ctx, tx, s.orderRep, order, &roundID, false, currentTime)
	if err != nil {
		return nil, err
	}
	if tickets == 0 {
		if _, err := tx.ExecContext(ctx, `UPDATE order_rounds SET status = 'ready' WHERE store_id = $1 AND id = $2`, store.ID, roundID); err != nil {
			return nil, err
		}
		round.Status = domain.OrderRoundStatusReady
	}

	// Recalculate order cumulative totals once from all cumulative order items (A4)
	if err := RecalculateOrderTotals(ctx, tx, store, order); err != nil {
		return nil, fmt.Errorf("failed to recalculate cumulative order totals: %w", err)
	}

	// Outbox event: order.round_submitted
	outboxPayload, _ := json.Marshal(map[string]any{
		"order_id":     order.ID,
		"order_number": order.OrderNumber,
		"table_number": order.TableNumber,
		"round_id":     round.ID,
		"round_number": round.RoundNumber,
		"items_count":  len(persistedItems),
		"round_total":  pricedOrder.TotalAmount,
		"order_total":  order.TotalAmount,
	})
	outboxEvent := &domain.OutboxEvent{
		ID:            uuid.New(),
		StoreID:       store.ID,
		EventType:     "order.round_submitted",
		AggregateType: "order",
		AggregateID:   order.ID.String(),
		Payload:       string(outboxPayload),
		CreatedAt:     currentTime,
	}
	if err := s.orderRep.InsertOutboxEvent(ctx, tx, outboxEvent); err != nil {
		return nil, fmt.Errorf("failed to insert outbox event: %w", err)
	}

	return round, nil
}

// TabDetailResult wraps the complete tab order with its rounds.
type TabDetailResult struct {
	Order  *domain.Order       `json:"order"`
	Rounds []domain.OrderRound `json:"rounds"`
}

// GetTabDetail returns the order along with its submitted rounds and items (A6: read-only, no locks, batch items).
func (s *TabService) GetTabDetail(ctx context.Context, actor *AuthenticatedStaff, orderID uuid.UUID) (*TabDetailResult, error) {
	if actor == nil {
		return nil, ErrInvalidSession
	}

	var result *TabDetailResult
	err := database.WithTenantTx(ctx, s.db, actor.Staff.Store.ID, func(tx *sqlx.Tx) error {
		storeID := actor.Staff.Store.ID

		order, err := s.repo.GetTabOrderByIDReadOnly(ctx, tx, storeID, orderID)
		if err != nil {
			if errors.Is(err, postgres.ErrOrderNotFound) {
				return ErrTabNotFound
			}
			return err
		}
		if order.BillingMode != domain.BillingModeOpenTab {
			return ErrTabNotFound
		}

		rounds, err := s.repo.ListRoundsByOrder(ctx, tx, storeID, orderID)
		if err != nil {
			return err
		}

		items, err := s.repo.GetOrderItemsWithOptionsByOrder(ctx, tx, storeID, orderID)
		if err != nil {
			return err
		}

		roundItemsMap := make(map[uuid.UUID][]domain.OrderItem)
		for _, it := range items {
			if it.RoundID != nil {
				roundItemsMap[*it.RoundID] = append(roundItemsMap[*it.RoundID], it)
			}
		}

		for i := range rounds {
			rounds[i].Items = roundItemsMap[rounds[i].ID]
		}

		result = &TabDetailResult{
			Order:  order,
			Rounds: rounds,
		}
		return nil
	})

	if err != nil {
		return nil, err
	}
	return result, nil
}

// SplitItemAllocationInput specifies an item and quantity allocated to a specific split.
type SplitItemAllocationInput struct {
	OrderItemID uuid.UUID `json:"order_item_id"`
	Quantity    int       `json:"quantity"`
}

// SplitInput defines the payload for an individual split in by_item strategy.
type SplitInput struct {
	Label string                     `json:"label"`
	Items []SplitItemAllocationInput `json:"items,omitempty"`
}

// CloseTabInput defines the configuration for closing a tab and locking rounds.
type CloseTabInput struct {
	Strategy   string       `json:"strategy"` // "equal" | "by_item" | empty / "full" for single-payer
	SplitCount int          `json:"split_count,omitempty"`
	Splits     []SplitInput `json:"splits,omitempty"`
}

// CloseTabResult contains the locked tab order and created payment splits.
type CloseTabResult struct {
	Order  *domain.Order         `json:"order"`
	Splits []domain.PaymentSplit `json:"splits"`
}

// CloseTab locks an open tab (transitions status from 'open' to 'pending_payment') and generates payment splits.
func (s *TabService) CloseTab(
	ctx context.Context,
	actor *AuthenticatedStaff,
	orderID uuid.UUID,
	in CloseTabInput,
) (*CloseTabResult, error) {
	if actor == nil {
		return nil, ErrInvalidSession
	}
	if !actor.Can(rbac.TabManage) && !actor.Can(rbac.OrderPOSCreate) {
		return nil, ErrForbidden
	}
	if orderID == uuid.Nil {
		return nil, ErrInvalidTabInput
	}

	strategy := strings.ToLower(strings.TrimSpace(in.Strategy))
	if strategy == "" {
		strategy = "full"
	}
	if strategy != "full" && strategy != "equal" && strategy != "by_item" {
		return nil, fmt.Errorf("%w: %q", ErrInvalidSplitStrategy, in.Strategy)
	}

	var result *CloseTabResult
	err := database.WithTenantTx(ctx, s.db, actor.Staff.Store.ID, func(tx *sqlx.Tx) error {
		storeID := actor.Staff.Store.ID

		order, _, err := s.orderRep.GetOrderByIDForUpdate(ctx, tx, storeID, orderID)
		if err != nil {
			if errors.Is(err, postgres.ErrOrderNotFound) {
				return ErrTabNotFound
			}
			return err
		}
		if order.BillingMode != domain.BillingModeOpenTab {
			return ErrTabNotFound
		}

		// Guard: Tab must be currently open
		if order.Status == domain.OrderStatusPendingPayment {
			return ErrTabClosed
		}
		if order.Status != domain.OrderStatusOpen {
			return ErrTabNotOpen
		}

		items, err := s.repo.GetOrderItemsWithOptionsByOrder(ctx, tx, storeID, orderID)
		if err != nil {
			return err
		}
		if len(items) == 0 {
			return errors.New("cannot close a tab with no items")
		}

		now := s.nowFunc().UTC()
		var splits []domain.PaymentSplit
		var splitItems []domain.PaymentSplitItem

		switch strategy {
		case "full":
			// Single-payer tab: 1 full split
			splitID := uuid.New()
			splits = []domain.PaymentSplit{
				{
					ID:                  splitID,
					StoreID:             storeID,
					OrderID:             order.ID,
					SplitNumber:         1,
					Label:               "Main Bill",
					SplitMode:           domain.SplitModeFull,
					SubtotalAmount:      order.SubtotalAmount,
					TaxAmount:           order.TaxAmount,
					ServiceChargeAmount: order.ServiceChargeAmount,
					TotalAmount:         order.TotalAmount,
					PaidAmount:          0,
					Status:              domain.SplitBillStatusUnpaid,
					CreatedAt:           now,
					UpdatedAt:           now,
				},
			}

		case "equal":
			if in.SplitCount < 2 || in.SplitCount > 20 {
				return fmt.Errorf("%w: got %d, expected 2-20", ErrInvalidSplitCount, in.SplitCount)
			}
			n := in.SplitCount
			baseSubtotal := order.SubtotalAmount / int64(n)
			baseTax := order.TaxAmount / int64(n)
			baseService := order.ServiceChargeAmount / int64(n)

			remSubtotal := order.SubtotalAmount - (baseSubtotal * int64(n))
			remTax := order.TaxAmount - (baseTax * int64(n))
			remService := order.ServiceChargeAmount - (baseService * int64(n))

			splits = make([]domain.PaymentSplit, n)
			for i := 0; i < n; i++ {
				spID := uuid.New()
				sub := baseSubtotal
				tax := baseTax
				svc := baseService

				// Remainder deterministic allocation: Split #1 gets all rounding remainders
				if i == 0 {
					sub += remSubtotal
					tax += remTax
					svc += remService
				}

				tot := sub + tax + svc
				if tot <= 0 {
					return errors.New("calculated split amount must be strictly positive")
				}

				splits[i] = domain.PaymentSplit{
					ID:                  spID,
					StoreID:             storeID,
					OrderID:             order.ID,
					SplitNumber:         i + 1,
					Label:               fmt.Sprintf("Split %d/%d", i+1, n),
					SplitMode:           domain.SplitModeEqual,
					SubtotalAmount:      sub,
					TaxAmount:           tax,
					ServiceChargeAmount: svc,
					TotalAmount:         tot,
					PaidAmount:          0,
					Status:              domain.SplitBillStatusUnpaid,
					CreatedAt:           now,
					UpdatedAt:           now,
				}
			}

		case "by_item":
			if len(in.Splits) < 2 || len(in.Splits) > 20 {
				return fmt.Errorf("%w: got %d splits, expected 2-20", ErrInvalidSplitCount, len(in.Splits))
			}

			// Validate item allocations
			itemMap := make(map[uuid.UUID]domain.OrderItem, len(items))
			for _, it := range items {
				itemMap[it.ID] = it
			}
			allocatedQty := make(map[uuid.UUID]int, len(items))

			for sIdx, spInput := range in.Splits {
				if len(spInput.Items) == 0 {
					return fmt.Errorf("split #%d has no items allocated", sIdx+1)
				}
				for _, itAlloc := range spInput.Items {
					if itAlloc.Quantity <= 0 {
						return fmt.Errorf("split #%d has invalid quantity %d for item %s", sIdx+1, itAlloc.Quantity, itAlloc.OrderItemID)
					}
					it, exists := itemMap[itAlloc.OrderItemID]
					if !exists {
						return fmt.Errorf("split #%d references unknown order_item_id %s", sIdx+1, itAlloc.OrderItemID)
					}
					allocatedQty[it.ID] += itAlloc.Quantity
				}
			}

			// Every single purchased unit must be allocated exactly once
			for _, it := range items {
				alloc := allocatedQty[it.ID]
				if alloc != it.Quantity {
					return fmt.Errorf("%w: item %q (ID %s) has purchased qty %d but allocated %d",
						ErrItemAllocationMismatch, it.SnapshotName, it.ID, it.Quantity, alloc)
				}
			}

			splits = make([]domain.PaymentSplit, len(in.Splits))
			var cumTax, cumService int64

			for sIdx, spInput := range in.Splits {
				spID := uuid.New()
				var splitSubtotal int64
				var thisSplitItems []domain.PaymentSplitItem

				for _, itAlloc := range spInput.Items {
					it := itemMap[itAlloc.OrderItemID]
					unitPrice := it.SubtotalAmount / int64(it.Quantity)
					itemAllocTotal := unitPrice * int64(itAlloc.Quantity)
					splitSubtotal += itemAllocTotal

					psi := domain.PaymentSplitItem{
						ID:              uuid.New(),
						StoreID:         storeID,
						SplitID:         spID,
						OrderItemID:     it.ID,
						Quantity:        itAlloc.Quantity,
						AllocatedAmount: itemAllocTotal,
						CreatedAt:       now,
					}
					thisSplitItems = append(thisSplitItems, psi)
					splitItems = append(splitItems, psi)
				}

				// Proportional tax and service charge
				var splitTax, splitService int64
				if order.SubtotalAmount > 0 {
					splitService = (order.ServiceChargeAmount * splitSubtotal) / order.SubtotalAmount
					splitTax = (order.TaxAmount * splitSubtotal) / order.SubtotalAmount
				}

				// Remainder deterministic allocation: Last split absorbs residual rounding
				isLastSplit := sIdx == len(in.Splits)-1
				if isLastSplit {
					splitService = order.ServiceChargeAmount - cumService
					splitTax = order.TaxAmount - cumTax
				} else {
					cumService += splitService
					cumTax += splitTax
				}

				splitTotal := splitSubtotal + splitTax + splitService
				label := strings.TrimSpace(spInput.Label)
				if label == "" {
					label = fmt.Sprintf("Split %d/%d", sIdx+1, len(in.Splits))
				}

				splits[sIdx] = domain.PaymentSplit{
					ID:                  spID,
					StoreID:             storeID,
					OrderID:             order.ID,
					SplitNumber:         sIdx + 1,
					Label:               label,
					SplitMode:           domain.SplitModeByItem,
					SubtotalAmount:      splitSubtotal,
					TaxAmount:           splitTax,
					ServiceChargeAmount: splitService,
					TotalAmount:         splitTotal,
					PaidAmount:          0,
					Status:              domain.SplitBillStatusUnpaid,
					Items:               thisSplitItems,
					CreatedAt:           now,
					UpdatedAt:           now,
				}
			}

			// Validate complete split math integrity
			if err := domain.ValidateSplitAllocations(order.TotalAmount, items, splits); err != nil {
				return fmt.Errorf("split validation failed: %w", err)
			}
		}

		// Save splits to database
		if err := s.repo.CreatePaymentSplitsTx(ctx, tx, splits, splitItems); err != nil {
			return fmt.Errorf("failed to save payment splits: %w", err)
		}

		// Update order status to pending_payment (rounds locked)
		if err := s.repo.UpdateOrderStatusAndPaymentStatusTx(
			ctx, tx, storeID, order.ID,
			domain.OrderStatusPendingPayment, domain.OrderPaymentStatusUnpaid,
		); err != nil {
			return fmt.Errorf("failed to update order status to pending_payment: %w", err)
		}

		order.Status = domain.OrderStatusPendingPayment
		order.PaymentStatus = domain.OrderPaymentStatusUnpaid

		actorID := actor.Staff.UserID
		if err := postgres.InsertAuditLog(ctx, tx, &domain.AuditLog{
			StoreID:     storeID,
			ActorID:     &actorID,
			ActorName:   actor.Staff.FullName,
			Action:      "TAB_CLOSE",
			Reason:      fmt.Sprintf("Tab #%s dikunci untuk pembayaran (%s)", order.OrderNumber, strategy),
			ReferenceID: order.OrderNumber,
			CreatedAt:   now,
		}); err != nil {
			return err
		}

		result = &CloseTabResult{
			Order:  order,
			Splits: splits,
		}
		return nil
	})

	if err != nil {
		return nil, err
	}
	return result, nil
}

// ReopenTab unlocks a tab in pending_payment status, cancelling any unpaid splits and returning it to open status.
func (s *TabService) ReopenTab(
	ctx context.Context,
	actor *AuthenticatedStaff,
	orderID uuid.UUID,
) (*domain.Order, error) {
	if actor == nil {
		return nil, ErrInvalidSession
	}
	if !actor.Can(rbac.TabManage) && !actor.Can(rbac.OrderPOSCreate) {
		return nil, ErrForbidden
	}
	if orderID == uuid.Nil {
		return nil, ErrInvalidTabInput
	}

	var updatedOrder *domain.Order
	err := database.WithTenantTx(ctx, s.db, actor.Staff.Store.ID, func(tx *sqlx.Tx) error {
		storeID := actor.Staff.Store.ID

		order, _, err := s.orderRep.GetOrderByIDForUpdate(ctx, tx, storeID, orderID)
		if err != nil {
			if errors.Is(err, postgres.ErrOrderNotFound) {
				return ErrTabNotFound
			}
			return err
		}
		if order.BillingMode != domain.BillingModeOpenTab {
			return ErrTabNotFound
		}

		// Idempotent: already open
		if order.Status == domain.OrderStatusOpen {
			updatedOrder = order
			return nil
		}

		if order.Status != domain.OrderStatusPendingPayment {
			return ErrTabCannotReopen
		}

		// Check if any split has already been paid
		splits, err := s.repo.GetPaymentSplitsByOrderID(ctx, tx, storeID, orderID)
		if err != nil {
			return err
		}
		for _, sp := range splits {
			if sp.Status == domain.SplitBillStatusPaid || sp.PaidAmount > 0 {
				return ErrSplitAlreadyPaid
			}
			// Check if any split has a pending/authorized payment attempt
			hasPending, err := s.repo.HasPendingPaymentForSplitTx(ctx, tx, storeID, sp.ID)
			if err != nil {
				return err
			}
			if hasPending {
				return ErrSplitPaymentPending
			}
		}

		// Cancel existing splits (keep rows for audit history)
		if err := s.repo.CancelPaymentSplitsTx(ctx, tx, storeID, orderID); err != nil {
			return fmt.Errorf("failed to cancel splits on reopen: %w", err)
		}

		// Return order to open status
		if err := s.repo.UpdateOrderStatusAndPaymentStatusTx(
			ctx, tx, storeID, order.ID,
			domain.OrderStatusOpen, domain.OrderPaymentStatusUnpaid,
		); err != nil {
			return fmt.Errorf("failed to return order to open status: %w", err)
		}

		order.Status = domain.OrderStatusOpen
		order.PaymentStatus = domain.OrderPaymentStatusUnpaid
		updatedOrder = order

		now := s.nowFunc().UTC()
		actorID := actor.Staff.UserID
		if err := postgres.InsertAuditLog(ctx, tx, &domain.AuditLog{
			StoreID:     storeID,
			ActorID:     &actorID,
			ActorName:   actor.Staff.FullName,
			Action:      "TAB_REOPEN",
			Reason:      fmt.Sprintf("Tab #%s dibuka kembali dari pending_payment", order.OrderNumber),
			ReferenceID: order.OrderNumber,
			CreatedAt:   now,
		}); err != nil {
			return err
		}

		return nil
	})

	if err != nil {
		return nil, err
	}
	return updatedOrder, nil
}
