package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/midtrans/midtrans-go/coreapi"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/domain"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/pkg/database"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/pkg/midtrans"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/repository/postgres"
)

// InitiatePayment initiates or settles a payment attempt for a tenant-scoped order split.
//   - Direct cashier tender methods (cash, static_qris, edc) execute in a single ACID transaction,
//     verifying an active owner/cashier actor, validating tendered amount or reconciliation_ref,
//     recording an immutable audit_logs entry, and committing the order to KDS.
//   - Midtrans Core API methods (qris, gopay, shopeepay, bank_transfer) execute via a 2-phase
//     transaction so PostgreSQL row locks are never held during external HTTP calls, with
//     CheckTransaction fallback reconciliation on network timeouts or ambiguous retries.
func (s *PaymentService) InitiatePayment(
	ctx context.Context,
	trustedStoreID uuid.UUID,
	input InitiatePaymentInput,
) (*InitiatePaymentResult, error) {
	if s == nil || s.db == nil || s.paymentRepo == nil || s.orderRepo == nil {
		return nil, errors.New("payment service dependencies are not initialized")
	}
	if trustedStoreID == uuid.Nil {
		return nil, postgres.ErrEmptyTenantContext
	}
	if input.OrderID == uuid.Nil {
		return nil, errors.New("order_id is required")
	}
	input.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey)
	if input.IdempotencyKey == "" || len(input.IdempotencyKey) > 100 {
		return nil, ErrPaymentIdempotencyKeyRequired
	}
	input.Bank = strings.ToLower(strings.TrimSpace(input.Bank))
	input.ReconciliationRef = strings.TrimSpace(input.ReconciliationRef)
	input.CallbackURL = strings.TrimSpace(input.CallbackURL)

	isDirectCashier := isDirectCashierMethod(input.Method)
	isMidtransGateway := isMidtransGatewayMethod(input.Method)
	if !isDirectCashier && !isMidtransGateway {
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedPaymentMethod, input.Method)
	}

	if isDirectCashier {
		if input.ActorUserID == nil || *input.ActorUserID == uuid.Nil {
			return nil, ErrUnauthorizedCashierTender
		}
		if (input.Method == domain.PaymentMethodStaticQRIS || input.Method == domain.PaymentMethodEDC || input.Method == domain.PaymentMethodPlatform) && input.ReconciliationRef == "" {
			return nil, ErrReconciliationRefRequired
		}
		return s.initiateDirectCashierPayment(ctx, trustedStoreID, input)
	}

	if s.midtransClient == nil && s.storeGatewayResolver == nil {
		return nil, errors.New("midtrans gateway client is not configured")
	}
	if input.Method == domain.PaymentMethodBankTransfer {
		switch input.Bank {
		case "bca", "bni", "bri", "mandiri", "permata":
		default:
			return nil, fmt.Errorf("%w: unsupported bank %q", ErrUnsupportedPaymentMethod, input.Bank)
		}
	}
	return s.initiateMidtransGatewayPayment(ctx, trustedStoreID, input)
}

func (s *PaymentService) initiateDirectCashierPayment(
	ctx context.Context,
	trustedStoreID uuid.UUID,
	input InitiatePaymentInput,
) (*InitiatePaymentResult, error) {
	var result *InitiatePaymentResult
	err := database.WithTenantTx(ctx, s.db, trustedStoreID, func(tx *sqlx.Tx) error {
		var err error
		result, err = s.directCashierPaymentTx(ctx, tx, trustedStoreID, input)
		return err
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// directCashierPaymentTx records a cash/static QRIS/EDC tender inside an open tenant transaction
// (also used by offline sync).
func (s *PaymentService) directCashierPaymentTx(ctx context.Context, tx *sqlx.Tx, trustedStoreID uuid.UUID, input InitiatePaymentInput) (*InitiatePaymentResult, error) {
	requestHash := computePaymentInitiationHash(trustedStoreID, input)
	var result *InitiatePaymentResult
	err := func() error {
		store, err := s.orderRepo.GetStoreForOrder(ctx, tx, trustedStoreID)
		if err != nil {
			return err
		}
		if !store.IsActive || store.DeletedAt != nil {
			return ErrStoreInactive
		}
		if input.Method != domain.PaymentMethodPlatform && input.Method != domain.PaymentMethodInvoice && !isPaymentMethodEnabled(store.EnabledPaymentMethods, input.Method) {
			return fmt.Errorf("%w: %s", ErrPaymentMethodNotEnabled, input.Method)
		}

		existing, err := s.paymentRepo.GetPaymentByIdempotencyKeyForUpdate(ctx, tx, trustedStoreID, input.IdempotencyKey)
		if err != nil && !errors.Is(err, postgres.ErrPaymentNotFound) {
			return err
		}
		if existing != nil {
			if existing.RequestHash == "" || existing.RequestHash != requestHash {
				return ErrPaymentIdempotencyConflict
			}
			order, splits, err := s.orderRepo.GetOrderByIDForUpdate(ctx, tx, trustedStoreID, existing.OrderID)
			if err != nil {
				return err
			}
			split, err := resolveOrderSplit(existing.SplitID, splits)
			if err != nil {
				return err
			}
			result = &InitiatePaymentResult{
				Payment:            existing,
				Order:              order,
				Split:              split,
				IsIdempotentReplay: true,
				OpenDrawer:         existing.Method == domain.PaymentMethodCash,
			}
			return nil
		}

		actor, err := s.paymentRepo.GetActiveCashierOrOwnerActor(ctx, tx, trustedStoreID, *input.ActorUserID)
		if err != nil {
			return err
		}
		if actor == nil {
			return ErrUnauthorizedCashierTender
		}

		// 1. Verify that cashier has an open shift (Strict Fail-Closed for direct cashier tender)
		var activeShift *domain.Shift
		if input.ShiftID != nil {
			// Offline sync pays into the shift the order was taken in, open or already closed (ADR-005 OF4).
			activeShift, err = s.shiftRepo.GetShiftByID(ctx, tx, trustedStoreID, *input.ShiftID)
			if errors.Is(err, postgres.ErrShiftNotFound) {
				return ErrNoOpenShift
			}
		} else {
			activeShift, err = s.shiftRepo.GetActiveShiftForStaff(ctx, tx, trustedStoreID, actor.ID, input.DrawerID)
		}
		if err != nil {
			if errors.Is(err, postgres.ErrNoOpenShift) || errors.Is(err, postgres.ErrDrawerNotFound) {
				return ErrNoOpenShift
			}
			return fmt.Errorf("failed to verify active cashier shift: %w", err)
		}

		order, splits, err := s.orderRepo.GetOrderByIDForUpdate(ctx, tx, trustedStoreID, input.OrderID)
		if err != nil {
			return err
		}
		if err := checkChannelPaymentTx(ctx, tx, trustedStoreID, order.ID, input.Method); err != nil {
			return err
		}
		if order.BillingMode == domain.BillingModeOpenTab {
			if order.Status == domain.OrderStatusOpen {
				return ErrTabNotClosed
			}
			if input.SplitID == nil {
				return ErrSplitIDRequired
			}
		} else {
			if input.SplitID != nil {
				return ErrSplitIDNotAllowed
			}
		}
		targetSplit, err := resolveOrderSplit(input.SplitID, splits)
		if err != nil {
			return err
		}
		split, err := s.paymentRepo.GetPaymentSplitForUpdate(ctx, tx, trustedStoreID, targetSplit.ID)
		if err != nil {
			return err
		}

		if order.PaymentStatus == domain.OrderPaymentStatusPaid || split.Status == domain.SplitBillStatusPaid {
			return ErrSplitAlreadyPaid
		}
		if order.Status == domain.OrderStatusCancelled {
			return ErrOrderNotPayable
		}

		remaining := split.TotalAmount - split.PaidAmount
		amount := remaining
		if input.Amount != nil {
			amount = *input.Amount
		}
		if amount <= 0 || amount > remaining {
			return fmt.Errorf("%w: amount=%d remaining=%d", ErrInvalidTenderAmount, amount, remaining)
		}
		partial := amount < remaining

		// Concurrency protection (P12, V12): open tabs and partial tenders never run beside a pending
		// gateway attempt; a pay-first tender of the whole balance still supersedes it (customer switches to cash).
		if order.BillingMode == domain.BillingModeOpenTab || partial || split.PaidAmount > 0 {
			activePending, err := s.paymentRepo.GetActivePaymentForSplitForUpdate(ctx, tx, trustedStoreID, split.ID)
			if err != nil && !errors.Is(err, postgres.ErrPaymentNotFound) {
				return err
			}
			if activePending != nil && (activePending.Status == domain.PaymentStatusPending || activePending.Status == domain.PaymentStatusAuthorized) {
				return ErrSplitPaymentPending
			}
		} else {
			if err := s.paymentRepo.ExpirePendingPaymentsByOrder(ctx, tx, trustedStoreID, order.ID); err != nil {
				return err
			}
		}
		attemptNo, err := s.paymentRepo.NextAttemptNumberForSplit(ctx, tx, trustedStoreID, split.ID)
		if err != nil {
			return err
		}

		if input.Method == domain.PaymentMethodInvoice {
			if err := chargeToAccountTx(ctx, tx, trustedStoreID, order, input.BusinessCustomerID, partial); err != nil {
				return err
			}
		}

		var cashTendered, changeGiven, rounding int64
		if input.Method == domain.PaymentMethodCash {
			cashDue := amount
			if !partial { // V11: only the closing cash tender is rounded, to the nearest store step
				settings, err := postgres.NewSettingsRepository().GetStoreSettings(ctx, tx, trustedStoreID)
				if err != nil {
					return err
				}
				cashDue = roundToNearest(amount, int64(settings.CashRoundingStep))
				rounding = cashDue - amount
			}
			if input.CashTenderedAmount < cashDue {
				return fmt.Errorf("%w: tendered=%d required=%d", ErrInsufficientCashTendered, input.CashTenderedAmount, cashDue)
			}
			cashTendered = input.CashTenderedAmount
			changeGiven = input.CashTenderedAmount - cashDue
		}

		now := s.nowFunc().UTC()
		paymentID := uuid.New()
		splitID := split.ID
		actorID := actor.ID
		activeShiftID := activeShift.ID
		payment := &domain.Payment{
			ID:                 paymentID,
			StoreID:            trustedStoreID,
			OrderID:            order.ID,
			SplitID:            &splitID,
			ShiftID:            &activeShiftID,
			AttemptNo:          attemptNo,
			IdempotencyKey:     input.IdempotencyKey,
			RequestHash:        requestHash,
			Provider:           "manual",
			ProviderOrderID:    paymentID.String(),
			Amount:             amount,
			RefundedAmount:     0,
			RoundingAmount:     rounding,
			Method:             input.Method,
			Status:             domain.PaymentStatusPaid,
			RawProviderStatus:  "cashier_settled",
			PaymentType:        string(input.Method),
			ReconciliationRef:  input.ReconciliationRef,
			VerifiedByUserID:   &actorID,
			CashTenderedAmount: cashTendered,
			ChangeGivenAmount:  changeGiven,
			PaidAt:             &now,
		}
		if err := s.paymentRepo.InsertPayment(ctx, tx, payment); err != nil {
			return err
		}

		// Bind order to active shift if not already assigned
		if order.ShiftID == nil {
			if _, err := tx.ExecContext(ctx, `
				UPDATE orders
				SET shift_id = $1
				WHERE store_id = $2 AND id = $3
			`, activeShift.ID, trustedStoreID, order.ID); err != nil {
				return fmt.Errorf("failed to bind order to active shift: %w", err)
			}
			order.ShiftID = &activeShift.ID
		}

		// If method is cash, record cash movement in the drawer ledger
		if input.Method == domain.PaymentMethodCash {
			saleReason := fmt.Sprintf("Penjualan tunai pesanan #%s", order.OrderNumber)
			cashMovement := &domain.CashMovement{
				ID:           uuid.New(),
				StoreID:      trustedStoreID,
				ShiftID:      activeShift.ID,
				MovementType: domain.CashMovementSale,
				Amount:       amount + rounding, // what physically entered the drawer
				ActorID:      actor.ID,
				Reason:       &saleReason,
				OrderID:      &order.ID,
				PaymentID:    &payment.ID,
				CreatedAt:    now,
			}
			if err := s.shiftRepo.InsertCashMovement(ctx, tx, cashMovement); err != nil {
				return fmt.Errorf("failed to record cash movement for cash payment: %w", err)
			}
		}

		split.PaidAmount += amount
		split.Status = domain.SplitBillStatusPaid
		if partial {
			split.Status = domain.SplitBillStatusPartiallyPaid
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE payment_splits SET status = $3, paid_amount = $4
			WHERE store_id = $1 AND id = $2
		`, trustedStoreID, split.ID, split.Status, split.PaidAmount); err != nil {
			return fmt.Errorf("failed to settle split for cashier payment: %w", err)
		}

		if partial {
			if err := s.recordPartialPaymentTx(ctx, tx, order); err != nil {
				return err
			}
		} else if err := s.commitPaidOrderTx(ctx, tx, order); err != nil {
			return err
		}

		auditAction, auditReason := directCashierAuditDetails(input.Method, order.OrderNumber, input.ReconciliationRef)
		metaBytes, _ := json.Marshal(map[string]any{
			"payment_id":           payment.ID.String(),
			"order_id":             order.ID.String(),
			"split_id":             split.ID.String(),
			"method":               payment.Method,
			"amount":               payment.Amount,
			"rounding_amount":      payment.RoundingAmount,
			"remaining_after":      split.TotalAmount - split.PaidAmount,
			"cash_tendered_amount": payment.CashTenderedAmount,
			"change_given_amount":  payment.ChangeGivenAmount,
			"reconciliation_ref":   payment.ReconciliationRef,
		})
		auditEntry := &domain.AuditLog{
			ID:          uuid.New(),
			StoreID:     trustedStoreID,
			ActorID:     &actorID,
			ActorName:   actor.FullName,
			Action:      auditAction,
			Reason:      auditReason,
			ReferenceID: order.OrderNumber,
			Metadata:    string(metaBytes),
		}
		if err := s.paymentRepo.InsertAuditLog(ctx, tx, auditEntry); err != nil {
			return err
		}

		result = &InitiatePaymentResult{
			Payment:            payment,
			Order:              order,
			Split:              split,
			IsIdempotentReplay: false,
			OpenDrawer:         input.Method == domain.PaymentMethodCash,
		}
		return nil
	}()
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (s *PaymentService) initiateMidtransGatewayPayment(
	ctx context.Context,
	trustedStoreID uuid.UUID,
	input InitiatePaymentInput,
) (*InitiatePaymentResult, error) {
	requestHash := computePaymentInitiationHash(trustedStoreID, input)

	var (
		phase1Payment                 *domain.Payment
		phase1Order                   *domain.Order
		phase1Split                   *domain.PaymentSplit
		immediateReplay               bool
		reconcileExistingBeforeCharge bool
		gwClient                      = s.midtransClient
	)

	// Phase 1: Short database transaction to check idempotency and persist/lock the pending attempt.
	err := database.WithTenantTx(ctx, s.db, trustedStoreID, func(tx *sqlx.Tx) error {
		store, err := s.orderRepo.GetStoreForOrder(ctx, tx, trustedStoreID)
		if err != nil {
			return err
		}
		if !store.IsActive || store.DeletedAt != nil {
			return ErrStoreInactive
		}
		if input.Method != domain.PaymentMethodPlatform && input.Method != domain.PaymentMethodInvoice && !isPaymentMethodEnabled(store.EnabledPaymentMethods, input.Method) {
			return fmt.Errorf("%w: %s", ErrPaymentMethodNotEnabled, input.Method)
		}
		if s.storeGatewayResolver != nil {
			customClient, resolveErr := s.storeGatewayResolver(ctx, tx, trustedStoreID)
			if resolveErr != nil {
				return resolveErr
			}
			normalized := normalizeGatewayClient(customClient)
			if normalized != nil {
				gwClient = normalized
			} else if s.tenantGatewayRequired {
				return fmt.Errorf("%w: store has no Midtrans credentials", ErrPaymentMethodNotEnabled)
			}
		}
		if gwClient == nil {
			return errors.New("midtrans gateway client is not configured")
		}

		existing, err := s.paymentRepo.GetPaymentByIdempotencyKeyForUpdate(ctx, tx, trustedStoreID, input.IdempotencyKey)
		if err != nil && !errors.Is(err, postgres.ErrPaymentNotFound) {
			return err
		}
		if existing != nil {
			if existing.RequestHash == "" || existing.RequestHash != requestHash {
				return ErrPaymentIdempotencyConflict
			}
			order, splits, err := s.orderRepo.GetOrderByIDForUpdate(ctx, tx, trustedStoreID, existing.OrderID)
			if err != nil {
				return err
			}
			split, err := resolveOrderSplit(existing.SplitID, splits)
			if err != nil {
				return err
			}
			phase1Payment = existing
			phase1Order = order
			phase1Split = split

			if existing.Status == domain.PaymentStatusPaid ||
				((existing.Status == domain.PaymentStatusPending || existing.Status == domain.PaymentStatusAuthorized) && hasGatewayChargeDetails(existing)) {
				immediateReplay = true
				return nil
			}
			if existing.Status == domain.PaymentStatusPending || existing.Status == domain.PaymentStatusAuthorized {
				reconcileExistingBeforeCharge = true
				return nil
			}
			return fmt.Errorf("%w: previous attempt with idempotency_key is in terminal status %s", ErrOrderNotPayable, existing.Status)
		}

		order, splits, err := s.orderRepo.GetOrderByIDForUpdate(ctx, tx, trustedStoreID, input.OrderID)
		if err != nil {
			return err
		}
		if err := checkChannelPaymentTx(ctx, tx, trustedStoreID, order.ID, input.Method); err != nil {
			return err
		}
		if order.BillingMode == domain.BillingModeOpenTab {
			if order.Status == domain.OrderStatusOpen {
				return ErrTabNotClosed
			}
			if input.SplitID == nil {
				return ErrSplitIDRequired
			}
		} else {
			if input.SplitID != nil {
				return ErrSplitIDNotAllowed
			}
		}
		targetSplit, err := resolveOrderSplit(input.SplitID, splits)
		if err != nil {
			return err
		}
		split, err := s.paymentRepo.GetPaymentSplitForUpdate(ctx, tx, trustedStoreID, targetSplit.ID)
		if err != nil {
			return err
		}

		if order.PaymentStatus == domain.OrderPaymentStatusPaid || split.Status == domain.SplitBillStatusPaid {
			return ErrSplitAlreadyPaid
		}
		if input.Amount != nil && *input.Amount != split.TotalAmount-split.PaidAmount {
			return ErrGatewayMustPayRemaining
		}
		now := s.nowFunc().UTC()
		if order.Status == domain.OrderStatusCancelled || (order.ExpiresAt != nil && !now.Before(*order.ExpiresAt)) {
			return ErrOrderNotPayable
		}

		activeAttempt, err := s.paymentRepo.GetActivePaymentForSplitForUpdate(ctx, tx, trustedStoreID, split.ID)
		if err != nil && !errors.Is(err, postgres.ErrPaymentNotFound) {
			return err
		}
		if activeAttempt != nil {
			activeNotExpired := activeAttempt.ExpiresAt == nil || now.Before(*activeAttempt.ExpiresAt)
			if activeAttempt.Provider == "midtrans" && activeAttempt.Method == input.Method && activeAttempt.Bank == input.Bank && activeNotExpired {
				phase1Payment = activeAttempt
				phase1Order = order
				phase1Split = split
				if hasGatewayChargeDetails(activeAttempt) {
					immediateReplay = true
					return nil
				}
				reconcileExistingBeforeCharge = true
				return nil
			}
			// Different method/bank or expired attempt on the same split: cancel older active attempt.
			if err := s.paymentRepo.UpdatePaymentStatus(
				ctx, tx, activeAttempt, domain.PaymentStatusCancelled,
				"superseded_by_new_attempt", activeAttempt.FraudStatus, activeAttempt.TransactionID, activeAttempt.PaymentType,
			); err != nil {
				return err
			}
		}

		var activeShiftID *uuid.UUID
		if input.ActorUserID != nil && *input.ActorUserID != uuid.Nil {
			activeShift, err := s.shiftRepo.GetActiveShiftForStaff(ctx, tx, trustedStoreID, *input.ActorUserID, input.DrawerID)
			if err == nil && activeShift != nil {
				activeShiftID = &activeShift.ID
			}
		}

		attemptNo, err := s.paymentRepo.NextAttemptNumberForSplit(ctx, tx, trustedStoreID, split.ID)
		if err != nil {
			return err
		}

		paymentID := uuid.New()
		splitID := split.ID
		payment := &domain.Payment{
			ID:              paymentID,
			StoreID:         trustedStoreID,
			OrderID:         order.ID,
			SplitID:         &splitID,
			ShiftID:         activeShiftID,
			AttemptNo:       attemptNo,
			IdempotencyKey:  input.IdempotencyKey,
			RequestHash:     requestHash,
			Provider:        "midtrans",
			ProviderOrderID: paymentID.String(),
			Amount:          split.TotalAmount - split.PaidAmount, // V12: the remaining balance after direct tenders
			RefundedAmount:  0,
			Method:          input.Method,
			Bank:            input.Bank,
			Status:          domain.PaymentStatusPending,
		}
		if err := s.paymentRepo.InsertPayment(ctx, tx, payment); err != nil {
			return err
		}

		if split.Status == domain.SplitBillStatusUnpaid {
			if _, err := tx.ExecContext(ctx, `UPDATE payment_splits SET status = 'pending' WHERE store_id = $1 AND id = $2`, trustedStoreID, split.ID); err != nil {
				return err
			}
			split.Status = domain.SplitBillStatusPending
		}
		if order.PaymentStatus == domain.OrderPaymentStatusUnpaid || order.PaymentStatus == domain.OrderPaymentStatusFailed {
			order.PaymentStatus = domain.OrderPaymentStatusPending
			if err := s.orderRepo.UpdateOrderStatusAndStock(ctx, tx, order); err != nil {
				return err
			}
		}

		if input.ActorUserID != nil && order.ShiftID == nil {
			if activeShift, err := s.shiftRepo.GetActiveShiftForStaff(ctx, tx, trustedStoreID, *input.ActorUserID, input.DrawerID); err == nil && activeShift != nil {
				_, _ = tx.ExecContext(ctx, `UPDATE orders SET shift_id = $1 WHERE store_id = $2 AND id = $3`, activeShift.ID, trustedStoreID, order.ID)
				order.ShiftID = &activeShift.ID
			}
		}

		phase1Payment = payment
		phase1Order = order
		phase1Split = split
		return nil
	})
	if err != nil {
		return nil, err
	}
	if immediateReplay {
		return &InitiatePaymentResult{
			Payment:            phase1Payment,
			Order:              phase1Order,
			Split:              phase1Split,
			IsIdempotentReplay: true,
		}, nil
	}

	// Network Phase (Outside DB Lock):
	// 1. If recovering an existing pending attempt that had no gateway details saved,
	//    check Midtrans status first before issuing ChargeCoreAPI.
	var (
		chargeResp        *coreapi.ChargeResponse
		statusResp        *coreapi.TransactionStatusResponse
		chargeErr         error
		recoveredByStatus bool
	)

	if reconcileExistingBeforeCharge {
		statusResp, err = gwClient.CheckTransaction(ctx, phase1Payment.ProviderOrderID)
		if err == nil && statusResp != nil && (statusResp.StatusCode == "200" || statusResp.StatusCode == "201") {
			recoveredByStatus = true
		}
	}

	if !recoveredByStatus {
		chargeResp, chargeErr = gwClient.ChargeCoreAPI(ctx, midtrans.ChargeRequest{
			OrderID:        phase1Payment.ProviderOrderID,
			Amount:         phase1Payment.Amount,
			Method:         phase1Payment.Method,
			Bank:           phase1Payment.Bank,
			IdempotencyKey: phase1Payment.ID.String(),
			CallbackURL:    input.CallbackURL,
		})
		if chargeErr != nil {
			// Fallback CheckTransaction per specification: determine whether the charge actually
			// reached Midtrans before the network error/timeout occurred.
			fallbackStatus, checkErr := gwClient.CheckTransaction(ctx, phase1Payment.ProviderOrderID)
			if checkErr == nil && fallbackStatus != nil && (fallbackStatus.StatusCode == "200" || fallbackStatus.StatusCode == "201") {
				statusResp = fallbackStatus
				recoveredByStatus = true
				chargeErr = nil
			} else if isPermanentGatewayRejection(chargeErr) {
				_ = database.WithTenantTx(ctx, s.db, trustedStoreID, func(tx *sqlx.Tx) error {
					lockedPayment, lockErr := s.paymentRepo.GetPaymentByIDForUpdate(ctx, tx, trustedStoreID, phase1Payment.ID)
					if lockErr != nil {
						return lockErr
					}
					if lockedPayment.Status == domain.PaymentStatusPending {
						return s.paymentRepo.MarkPaymentFailed(ctx, tx, lockedPayment, "MIDTRANS_CHARGE_REJECTED", chargeErr.Error())
					}
					return nil
				})
				return nil, chargeErr
			} else {
				// Transient network timeout and transaction is not yet visible at Midtrans:
				// keep the pending payment row intact so an idempotent retry reuses the same payment.ID.
				return nil, chargeErr
			}
		}
	}

	// Phase 2: Short database transaction to persist gateway details or reconciled status.
	var finalResult *InitiatePaymentResult
	err = database.WithTenantTx(ctx, s.db, trustedStoreID, func(tx *sqlx.Tx) error {
		order, _, err := s.orderRepo.GetOrderByIDForUpdate(ctx, tx, trustedStoreID, phase1Order.ID)
		if err != nil {
			return err
		}
		split, err := s.paymentRepo.GetPaymentSplitForUpdate(ctx, tx, trustedStoreID, phase1Split.ID)
		if err != nil {
			return err
		}
		payment, err := s.paymentRepo.GetPaymentByIDForUpdate(ctx, tx, trustedStoreID, phase1Payment.ID)
		if err != nil {
			return err
		}

		if recoveredByStatus && statusResp != nil {
			applyMidtransStatusResponse(payment, statusResp)
			if err := s.paymentRepo.UpdatePaymentGatewayDetails(ctx, tx, payment); err != nil {
				return err
			}
			mapped, mapErr := midtrans.MapMidtransStatus(statusResp.TransactionStatus, statusResp.FraudStatus)
			if mapErr == nil && domain.CanTransitionPaymentStatus(payment.Status, mapped) {
				priorStatus := payment.Status
				if err := s.paymentRepo.UpdatePaymentStatus(
					ctx, tx, payment, mapped, statusResp.TransactionStatus,
					statusResp.FraudStatus, statusResp.TransactionID, statusResp.PaymentType,
				); err != nil {
					return err
				}
				if mapped == domain.PaymentStatusPaid && statusResp.StatusCode == "200" {
					if duplicate, err := s.flagDuplicateSettlementTx(ctx, tx, order, priorStatus); err != nil || duplicate {
						return err
					}
					if split.Status != domain.SplitBillStatusPaid {
						if _, err := tx.ExecContext(ctx, `UPDATE payment_splits SET status = 'paid', paid_amount = total_amount WHERE store_id = $1 AND id = $2`, trustedStoreID, split.ID); err != nil {
							return err
						}
						split.Status = domain.SplitBillStatusPaid
						split.PaidAmount = split.TotalAmount
					}
					if err := s.commitPaidOrderTx(ctx, tx, order); err != nil {
						return err
					}
				}
			}
		} else if chargeResp != nil {
			if chargeResp.StatusCode != "" && chargeResp.StatusCode != "200" && chargeResp.StatusCode != "201" {
				if err := s.paymentRepo.MarkPaymentFailed(ctx, tx, payment, chargeResp.StatusCode, chargeResp.StatusMessage); err != nil {
					return err
				}
				return fmt.Errorf("midtrans rejected charge with status_code %s: %s", chargeResp.StatusCode, chargeResp.StatusMessage)
			}
			applyMidtransChargeResponse(payment, chargeResp)
			if err := s.paymentRepo.UpdatePaymentGatewayDetails(ctx, tx, payment); err != nil {
				return err
			}
		}

		finalResult = &InitiatePaymentResult{
			Payment:            payment,
			Order:              order,
			Split:              split,
			IsIdempotentReplay: reconcileExistingBeforeCharge,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return finalResult, nil
}
