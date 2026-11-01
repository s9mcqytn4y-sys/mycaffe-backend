package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	ErrTabLabelInUse   = errors.New("label meja/tab sedang digunakan oleh tab aktif lain")
	ErrTabNotOpen      = errors.New("tab tidak dalam status terbuka")
	ErrEmptyRoundItems = errors.New("ronde pesanan harus memiliki minimal satu item")
	ErrInvalidTabInput = errors.New("input tab tidak valid")
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
	Order      *domain.Order      `json:"order"`
	FirstRound *domain.OrderRound `json:"first_round,omitempty"`
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

	var result *OpenTabResult

	err := database.WithTenantTx(ctx, s.db, actor.Staff.Store.ID, func(tx *sqlx.Tx) error {
		storeID := actor.Staff.Store.ID

		// 1. Label duplicate check (unless allow_duplicate_label = true)
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

		// 2. Fetch catalog and store for daily sequence
		store, err := s.orderRep.GetStoreForOrder(ctx, tx, storeID)
		if err != nil {
			return err
		}
		if !store.IsActive || store.DeletedAt != nil {
			return ErrStoreInactive
		}

		currentTime := s.nowFunc().UTC()
		businessDateISO, compactDate := resolveStoreBusinessDates(currentTime, store.Timezone)
		seq, err := s.orderRep.AllocateDailySequence(ctx, tx, storeID, businessDateISO)
		if err != nil {
			return err
		}

		orderPrefix := strings.TrimSpace(store.OrderPrefix)
		if orderPrefix == "" {
			orderPrefix = "ORD"
		}
		queuePrefix := strings.TrimSpace(store.QueuePrefix)
		if queuePrefix == "" {
			queuePrefix = "A"
		}

		orderNum := fmt.Sprintf("%s-%s-%04d", orderPrefix, compactDate, seq)
		queueNum := fmt.Sprintf("%s%03d", queuePrefix, seq)

		orderID := uuid.New()
		actorUserID := actor.Staff.UserID
		order := &domain.Order{
			ID:                  orderID,
			StoreID:             storeID,
			IdempotencyKey:      in.IdempotencyKey,
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
		// 3. If initial items exist, submit round 1 in the same transaction
		if len(in.InitialItems) > 0 {
			round, err := s.submitRoundInternal(ctx, tx, store, order, actor, SubmitRoundInput{
				IdempotencyKey: in.IdempotencyKey + "-r1",
				Notes:          "Ronde 1 (Initial)",
				Items:          in.InitialItems,
			}, 1)
			if err != nil {
				return err
			}
			firstRound = round
		}

		// 4. Audit Log
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
	Round *domain.OrderRound `json:"round"`
	Order *domain.Order      `json:"order"`
}

// SubmitRound appends a new round to an existing open tab.
func (s *TabService) SubmitRound(ctx context.Context, actor *AuthenticatedStaff, orderID uuid.UUID, in SubmitRoundInput) (*SubmitRoundResult, error) {
	if actor == nil {
		return nil, ErrInvalidSession
	}
	if !actor.Can(rbac.TabManage) {
		return nil, ErrForbidden
	}
	if len(in.Items) == 0 {
		return nil, ErrEmptyRoundItems
	}

	var result *SubmitRoundResult

	err := database.WithTenantTx(ctx, s.db, actor.Staff.Store.ID, func(tx *sqlx.Tx) error {
		storeID := actor.Staff.Store.ID

		order, _, err := s.orderRep.GetOrderByIDForUpdate(ctx, tx, storeID, orderID)
		if err != nil {
			return err
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
// updates parent order totals, and writes outbox event 'order.round_submitted'.
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

	snapshot := CatalogSnapshot{
		Products:          catalogData.Products,
		ModifierGroups:    catalogData.ModifierGroups,
		ModifierOptions:   catalogData.ModifierOptions,
		ProductGroups:     catalogData.ProductModifierGroups,
		OptionChildGroups: catalogData.OptionModifierGroups,
		Variants:          catalogData.Variants,
	}

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

	round := &domain.OrderRound{
		ID:                roundID,
		StoreID:           store.ID,
		OrderID:           order.ID,
		RoundNumber:       roundNumber,
		Status:            domain.OrderRoundStatusQueued,
		SubmittedByUserID: &actorUserID,
		SubmittedAt:       currentTime,
		Notes:             strings.TrimSpace(in.Notes),
		CreatedAt:         currentTime,
		UpdatedAt:         currentTime,
	}

	if err := s.repo.InsertOrderRound(ctx, tx, round); err != nil {
		return nil, fmt.Errorf("failed to insert order round: %w", err)
	}

	// Insert items linked to round_id
	var roundItems []domain.OrderItem
	for _, pricedItem := range pricedOrder.Items {
		itemRow := pricedItem.Item
		itemRow.ID = uuid.New()
		itemRow.StoreID = store.ID
		itemRow.OrderID = order.ID
		itemRow.RoundID = &roundID
		itemRow.CreatedAt = currentTime

		if err := s.orderRep.InsertOrderItem(ctx, tx, &itemRow); err != nil {
			return nil, fmt.Errorf("failed to insert round item: %w", err)
		}

		for _, pricedOpt := range pricedItem.Options {
			parentOptRow := pricedOpt.Option
			parentOptRow.ID = uuid.New()
			parentOptRow.StoreID = store.ID
			parentOptRow.OrderItemID = itemRow.ID
			parentOptRow.ParentOrderItemOptionID = nil
			parentOptRow.CreatedAt = currentTime

			if err := s.orderRep.InsertOrderItemOption(ctx, tx, &parentOptRow); err != nil {
				return nil, fmt.Errorf("failed to insert round item option: %w", err)
			}

			for _, childOpt := range pricedOpt.Children {
				childOptRow := childOpt
				childOptRow.ID = uuid.New()
				childOptRow.StoreID = store.ID
				childOptRow.OrderItemID = itemRow.ID
				parentRowIDCopy := parentOptRow.ID
				childOptRow.ParentOrderItemOptionID = &parentRowIDCopy
				childOptRow.CreatedAt = currentTime

				if err := s.orderRep.InsertOrderItemOption(ctx, tx, &childOptRow); err != nil {
					return nil, fmt.Errorf("failed to insert round child option: %w", err)
				}
			}
		}
		roundItems = append(roundItems, itemRow)
	}
	round.Items = roundItems

	// Accumulate parent order totals
	order.SubtotalAmount += pricedOrder.SubtotalAmount
	order.TaxAmount += pricedOrder.TaxAmount
	order.ServiceChargeAmount += pricedOrder.ServiceChargeAmount
	order.TotalAmount += pricedOrder.TotalAmount
	order.UpdatedAt = currentTime

	if err := s.repo.UpdateOrderTotals(ctx, tx, order); err != nil {
		return nil, fmt.Errorf("failed to update order totals: %w", err)
	}

	// Outbox event murni: order.round_submitted
	outboxPayload, _ := json.Marshal(map[string]any{
		"order_id":     order.ID,
		"order_number": order.OrderNumber,
		"table_number": order.TableNumber,
		"round_id":     round.ID,
		"round_number": round.RoundNumber,
		"items_count":  len(roundItems),
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

// GetTabDetail returns the order along with its submitted rounds.
func (s *TabService) GetTabDetail(ctx context.Context, actor *AuthenticatedStaff, orderID uuid.UUID) (*TabDetailResult, error) {
	if actor == nil {
		return nil, ErrInvalidSession
	}

	var result *TabDetailResult
	err := database.WithTenantTx(ctx, s.db, actor.Staff.Store.ID, func(tx *sqlx.Tx) error {
		order, _, err := s.orderRep.GetOrderByIDForUpdate(ctx, tx, actor.Staff.Store.ID, orderID)
		if err != nil {
			return err
		}

		rounds, err := s.repo.ListRoundsByOrder(ctx, tx, actor.Staff.Store.ID, orderID)
		if err != nil {
			return err
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
