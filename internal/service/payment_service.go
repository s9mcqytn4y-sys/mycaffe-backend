package service

import (
	"context"
	"errors"
	"reflect"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/midtrans/midtrans-go/coreapi"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/domain"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/midtrans"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/repository/postgres"
)

var (
	ErrInvalidMidtransWebhook        = errors.New("invalid Midtrans webhook")
	ErrPaymentReconciliation         = errors.New("payment reconciliation failed")
	ErrPaymentIdempotencyKeyRequired = errors.New("payment idempotency_key is required")
	ErrPaymentIdempotencyConflict    = errors.New("idempotency_key already used with a different payment request payload")
	ErrPaymentMethodNotEnabled       = errors.New("payment method is not enabled for this store")
	ErrUnsupportedPaymentMethod      = errors.New("unsupported payment method")
	ErrOrderNotPayable               = errors.New("order is not in a payable state")
	ErrSplitAlreadyPaid              = errors.New("payment split is already paid")
	ErrUnauthorizedCashierTender     = errors.New("direct cashier tender requires an active owner or cashier actor")
	ErrInsufficientCashTendered      = errors.New("cash_tendered_amount cannot be less than total payable amount")
	ErrReconciliationRefRequired     = errors.New("reconciliation_ref is required for static_qris and edc payments")
	ErrNoOpenShift                   = errors.New("tidak ada sesi shift aktif untuk kasir ini; buka shift kasir terlebih dahulu")
	ErrTabNotClosed                  = errors.New("tab belum ditutup; selesaikan atau tutup tab sebelum memproses pembayaran")
	ErrSplitIDRequired               = errors.New("split_id is required for open_tab payments")
	ErrSplitIDNotAllowed             = errors.New("split_id is not allowed for pay_first payments")
	ErrSplitPaymentPending           = errors.New("split bill memiliki transaksi pembayaran yang sedang gantung (pending)")
	ErrInvalidTenderAmount           = errors.New("amount must be between 1 and the remaining balance")
	ErrGatewayMustPayRemaining       = errors.New("dynamic QRIS and gateway payments must cover exactly the remaining balance")
)

// GatewayClient abstracts Midtrans Core API operations for deterministic unit and integration testing.
type GatewayClient interface {
	ChargeCoreAPI(ctx context.Context, input midtrans.ChargeRequest) (*coreapi.ChargeResponse, error)
	CheckTransaction(ctx context.Context, providerOrderID string) (*coreapi.TransactionStatusResponse, error)
}

type PaymentServiceOption func(*PaymentService)

func WithPaymentClock(clock func() time.Time) PaymentServiceOption {
	return func(s *PaymentService) {
		if clock != nil {
			s.nowFunc = clock
		}
	}
}

func WithGatewayClient(client GatewayClient) PaymentServiceOption {
	return func(s *PaymentService) {
		s.midtransClient = normalizeGatewayClient(client)
	}
}

func WithShiftRepository(repo *postgres.ShiftRepository) PaymentServiceOption {
	return func(s *PaymentService) {
		if repo != nil {
			s.shiftRepo = repo
		}
	}
}

func normalizeGatewayClient(client GatewayClient) GatewayClient {
	if client == nil {
		return nil
	}
	v := reflect.ValueOf(client)
	if v.Kind() == reflect.Ptr && v.IsNil() {
		return nil
	}
	return client
}

type InitiatePaymentInput struct {
	OrderID            uuid.UUID
	SplitID            *uuid.UUID
	IdempotencyKey     string
	Method             domain.PaymentMethod
	Bank               string
	CashTenderedAmount int64
	// Amount is an optional partial tender (V12); nil pays the whole remaining balance of the split.
	Amount            *int64
	ReconciliationRef string
	CallbackURL       string
	ActorUserID       *uuid.UUID
	DrawerID          *uuid.UUID
}

type InitiatePaymentResult struct {
	Payment            *domain.Payment      `json:"payment"`
	Order              *domain.Order        `json:"order"`
	Split              *domain.PaymentSplit `json:"split"`
	IsIdempotentReplay bool                 `json:"is_idempotent_replay"`
	OpenDrawer         bool                 `json:"open_drawer"`
}

type MidtransWebhookInput struct {
	OrderID           string
	TransactionID     string
	StatusCode        string
	TransactionStatus string
	GrossAmount       string
	FraudStatus       string
	PaymentType       string
	Currency          string
}

type PaymentWebhookResult struct {
	Duplicate bool
	Ignored   bool
	Anomaly   bool
	Status    domain.PaymentAttemptStatus
}

type PaymentService struct {
	db             *sqlx.DB
	paymentRepo    *postgres.PaymentRepository
	orderRepo      *postgres.OrderRepository
	shiftRepo      *postgres.ShiftRepository
	midtransClient GatewayClient
	nowFunc        func() time.Time
}

func NewPaymentService(
	db *sqlx.DB,
	paymentRepo *postgres.PaymentRepository,
	orderRepo *postgres.OrderRepository,
	midtransClient GatewayClient,
	opts ...PaymentServiceOption,
) *PaymentService {
	if paymentRepo == nil {
		paymentRepo = postgres.NewPaymentRepository()
	}
	if orderRepo == nil {
		orderRepo = postgres.NewOrderRepository()
	}
	svc := &PaymentService{
		db:             db,
		paymentRepo:    paymentRepo,
		orderRepo:      orderRepo,
		shiftRepo:      postgres.NewShiftRepository(),
		midtransClient: normalizeGatewayClient(midtransClient),
		nowFunc:        time.Now,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(svc)
		}
	}
	return svc
}
