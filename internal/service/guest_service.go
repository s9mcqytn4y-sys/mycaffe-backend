package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/domain"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/pkg/database"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/repository/postgres"
)

// Guest QR ordering (iteration 5b): one session per device (Q6), pay-first only (T2), caps of QR5.
const (
	guestSessionTTL       = 3 * time.Hour
	maxUnpaidPerGuest     = 2
	maxUnpaidPerTable     = 6
	maxGuestOrderItems    = 30
	maxGuestLineQuantity  = 20
	maxGuestIdempotentKey = 50
)

var (
	ErrQROrderingPaused    = errors.New("QR ordering is paused by the store")
	ErrStoreClosed         = errors.New("store is closed: no shift is open")
	ErrGuestSessionInvalid = errors.New("guest session is invalid or expired")
	ErrGuestUnpaidLimit    = errors.New("too many unpaid QR orders")
	ErrGuestOrderNotFound  = errors.New("order not found for this guest")
)

// GuestOrderBinding ties an order to the guest's table context; Guard runs inside the order
// transaction before stock is touched.
type GuestOrderBinding struct {
	TableID           uuid.UUID
	TableSessionID    uuid.UUID
	GuestSessionID    uuid.UUID
	TrackingTokenHash []byte
	Guard             func(ctx context.Context, tx *sqlx.Tx) error
}

// GuestIdentity is the authenticated guest behind the __Secure-mc_guest cookie.
type GuestIdentity struct {
	StoreID        uuid.UUID
	SessionID      uuid.UUID
	TableSessionID uuid.UUID
	TableID        uuid.UUID
	TableNumber    string
	ExpiresAt      time.Time
}

type GuestService struct {
	db       *sqlx.DB
	repo     *postgres.GuestRepository
	tables   *postgres.TableRepository
	tabs     *postgres.TabRepository
	settings *postgres.SettingsRepository
	orders   *OrderService
	payments *PaymentService
	nowFunc  func() time.Time
}

func NewGuestService(db *sqlx.DB, orders *OrderService, payments *PaymentService) *GuestService {
	return &GuestService{
		db: db, repo: postgres.NewGuestRepository(), tables: postgres.NewTableRepository(), tabs: postgres.NewTabRepository(),
		settings: postgres.NewSettingsRepository(), orders: orders, payments: payments, nowFunc: time.Now,
	}
}

func randomToken() (string, []byte, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", nil, fmt.Errorf("failed to generate token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(b)
	sum := sha256.Sum256([]byte(token))
	return token, sum[:], nil
}

// GuestSessionView is returned when a session starts and on GET /public/guest/session.
type GuestSessionView struct {
	TableNumber string    `json:"table_number"`
	ExpiresAt   time.Time `json:"expires_at"`
}

// StartSession turns a scanned QR into a guest session: it refuses while ordering is closed, joins the
// table's open session or opens one (opened_via = qr), and returns the cookie value
// "<store_id>.<session_id>.<secret>" (the store id is only a lookup key; the hash must match).
func (s *GuestService) StartSession(ctx context.Context, qrToken string) (string, *GuestSessionView, error) {
	if len(qrToken) < 22 || len(qrToken) > 64 {
		return "", nil, ErrTableNotFound
	}
	storeID, tableID, active, err := s.tables.ResolveTableQR(ctx, s.db, qrToken)
	if err != nil {
		return "", nil, mapTableError(err)
	}
	secret, hash, err := randomToken()
	if err != nil {
		return "", nil, err
	}
	sessionID := uuid.New()
	var view *GuestSessionView
	err = database.WithTenantTx(ctx, s.db, storeID, func(tx *sqlx.Tx) error {
		if err := s.ensureOrderingOpen(ctx, tx, storeID, active); err != nil {
			return err
		}
		now := s.nowFunc().UTC()
		settings, err := s.settings.GetStoreSettings(ctx, tx, storeID)
		if err != nil {
			return err
		}
		if err := s.tables.CloseSettledSessions(ctx, tx, storeID, settings.TableIdleCloseMinutes, now); err != nil {
			return err
		}
		// Locking the table serializes devices scanning the same table at once.
		t, err := s.tables.GetTable(ctx, tx, storeID, tableID, true)
		if err != nil {
			return err
		}
		session, err := s.tables.GetOpenSessionForUpdate(ctx, tx, storeID, tableID)
		if err != nil {
			return err
		}
		if session == nil {
			session = &domain.TableSession{ID: uuid.New(), StoreID: storeID, TableID: tableID, OpenedAt: now, OpenedVia: "qr"}
			if err := s.tables.InsertSession(ctx, tx, session); err != nil {
				return err
			}
		}
		expires := now.Add(guestSessionTTL)
		if err := s.repo.InsertGuestSession(ctx, tx, sessionID, storeID, session.ID, tableID, hash, now, expires); err != nil {
			return err
		}
		view = &GuestSessionView{TableNumber: t.TableNumber, ExpiresAt: expires}
		return nil
	})
	if err != nil {
		return "", nil, mapTableError(err)
	}
	return storeID.String() + "." + sessionID.String() + "." + secret, view, nil
}

func (s *GuestService) ensureOrderingOpen(ctx context.Context, tx *sqlx.Tx, storeID uuid.UUID, tableActive bool) error {
	settings, err := s.settings.GetStoreSettings(ctx, tx, storeID)
	if err != nil {
		return err
	}
	reason, err := qrClosedReason(ctx, tx, s.tables, settings, storeID, tableActive)
	switch {
	case err != nil:
		return err
	case reason == QRClosedInactive:
		return ErrTableInactive
	case reason == QRClosedPaused:
		return ErrQROrderingPaused
	case reason == QRClosedNoShift:
		return ErrStoreClosed
	}
	return nil
}

// Authenticate validates the guest cookie: matching hash, not expired or ended, table session open.
func (s *GuestService) Authenticate(ctx context.Context, cookie string) (*GuestIdentity, error) {
	parts := strings.SplitN(cookie, ".", 3)
	if len(parts) != 3 {
		return nil, ErrGuestSessionInvalid
	}
	storeID, err1 := uuid.Parse(parts[0])
	sessionID, err2 := uuid.Parse(parts[1])
	if err1 != nil || err2 != nil || storeID == uuid.Nil {
		return nil, ErrGuestSessionInvalid
	}
	sum := sha256.Sum256([]byte(parts[2]))
	var id *GuestIdentity
	err := database.WithTenantTx(ctx, s.db, storeID, func(tx *sqlx.Tx) error {
		row, err := s.repo.GetGuestSession(ctx, tx, storeID, sessionID, false)
		if err != nil {
			return err
		}
		if err := s.checkSession(row, sum[:]); err != nil {
			return err
		}
		id = &GuestIdentity{StoreID: storeID, SessionID: row.ID, TableSessionID: row.TableSessionID,
			TableID: row.TableID, TableNumber: row.TableNumber, ExpiresAt: row.ExpiresAt}
		return nil
	})
	if errors.Is(err, postgres.ErrGuestSessionNotFound) {
		return nil, ErrGuestSessionInvalid
	}
	return id, err
}

func (s *GuestService) checkSession(row *postgres.GuestSessionRow, hash []byte) error {
	if hash != nil && subtle.ConstantTimeCompare(row.TokenHash, hash) != 1 {
		return ErrGuestSessionInvalid
	}
	if row.EndedAt != nil || !s.nowFunc().Before(row.ExpiresAt) || row.TableSessionStatus != domain.TableSessionOpen {
		return ErrGuestSessionInvalid
	}
	return nil
}

type GuestOrderInput struct {
	IdempotencyKey           string           `json:"idempotency_key"`
	CustomerName             string           `json:"customer_name"`
	Notes                    string           `json:"notes"`
	ExpectedTotal            *int64           `json:"expected_total"`
	ExpectedQuoteFingerprint string           `json:"expected_quote_fingerprint"`
	Items                    []OrderItemInput `json:"items"`
}

type GuestOrderResult struct {
	Order domain.Order `json:"order"`
	// TrackingToken opens GET /public/orders/track/:token; returned once, only its hash is stored.
	TrackingToken      string `json:"tracking_token,omitempty"`
	IsIdempotentReplay bool   `json:"is_idempotent_replay"`
}

// CreateOrder places a pay-first dine-in order for the guest's table (decision T2).
func (s *GuestService) CreateOrder(ctx context.Context, g *GuestIdentity, in GuestOrderInput) (*GuestOrderResult, error) {
	f := fieldErrors{}
	key := f.text("idempotency_key", in.IdempotencyKey, 1, maxGuestIdempotentKey)
	name := f.text("customer_name", in.CustomerName, 0, 60)
	notes := f.text("notes", in.Notes, 0, 140)
	if len(in.Items) == 0 || len(in.Items) > maxGuestOrderItems {
		f.add("items", fmt.Sprintf("harus berisi 1 sampai %d item", maxGuestOrderItems))
	}
	for _, it := range in.Items {
		if it.Quantity < 1 || it.Quantity > maxGuestLineQuantity {
			f.add("items", fmt.Sprintf("jumlah per item harus 1 sampai %d", maxGuestLineQuantity))
		}
		if utf8.RuneCountInString(it.Notes) > 140 {
			f.add("items", "catatan item maksimal 140 karakter")
		}
	}
	if err := f.err(); err != nil {
		return nil, err
	}
	if name == "" {
		name = "Tamu Meja " + g.TableNumber
	}
	token, hash, err := randomToken()
	if err != nil {
		return nil, err
	}
	binding := &GuestOrderBinding{
		TableID: g.TableID, TableSessionID: g.TableSessionID, GuestSessionID: g.SessionID, TrackingTokenHash: hash,
		Guard: func(ctx context.Context, tx *sqlx.Tx) error {
			row, err := s.repo.GetGuestSession(ctx, tx, g.StoreID, g.SessionID, true)
			if err != nil {
				return err
			}
			if err := s.checkSession(row, nil); err != nil {
				return err
			}
			if err := s.ensureOrderingOpen(ctx, tx, g.StoreID, row.TableActive); err != nil {
				return err
			}
			guest, table, err := s.repo.CountUnpaidQROrders(ctx, tx, g.StoreID, g.SessionID, g.TableSessionID)
			if err != nil {
				return err
			}
			if guest >= maxUnpaidPerGuest || table >= maxUnpaidPerTable {
				return ErrGuestUnpaidLimit
			}
			return s.repo.ClearSettled(ctx, tx, g.StoreID, g.TableSessionID)
		},
	}
	res, err := s.orders.CreateOrder(ctx, g.StoreID, OrderCreationInput{
		// Namespaced so one guest's key can never replay another guest's order.
		IdempotencyKey:           "g:" + g.SessionID.String() + ":" + key,
		TableNumber:              g.TableNumber,
		CustomerName:             name,
		OrderType:                domain.OrderTypeDineIn,
		BillingMode:              domain.BillingModePayFirst,
		ExpectedTotal:            in.ExpectedTotal,
		ExpectedQuoteFingerprint: in.ExpectedQuoteFingerprint,
		Notes:                    notes,
		Items:                    in.Items,
		Guest:                    binding,
	})
	if err != nil {
		if errors.Is(err, postgres.ErrGuestSessionNotFound) {
			return nil, ErrGuestSessionInvalid
		}
		return nil, mapTableError(err)
	}
	out := &GuestOrderResult{Order: res.Order, IsIdempotentReplay: res.IsIdempotentReplay}
	if !res.IsIdempotentReplay {
		out.TrackingToken = token
	}
	return out, nil
}

func (s *GuestService) ListOrders(ctx context.Context, g *GuestIdentity) ([]postgres.GuestOrderSummary, error) {
	var out []postgres.GuestOrderSummary
	err := database.WithTenantTx(ctx, s.db, g.StoreID, func(tx *sqlx.Tx) error {
		var err error
		out, err = s.repo.ListGuestOrders(ctx, tx, g.StoreID, g.SessionID)
		return err
	})
	return out, err
}

// InitiatePayment charges one of the guest's own orders through dynamic QRIS (Midtrans) only.
func (s *GuestService) InitiatePayment(ctx context.Context, g *GuestIdentity, orderID uuid.UUID, idempotencyKey string) (*InitiatePaymentResult, error) {
	f := fieldErrors{}
	key := f.text("idempotency_key", idempotencyKey, 1, maxGuestIdempotentKey)
	if err := f.err(); err != nil {
		return nil, err
	}
	err := database.WithTenantTx(ctx, s.db, g.StoreID, func(tx *sqlx.Tx) error {
		owner, err := s.repo.OrderGuestSession(ctx, tx, g.StoreID, orderID)
		if err != nil {
			return err
		}
		if owner == nil || *owner != g.SessionID {
			return ErrGuestOrderNotFound
		}
		return nil
	})
	if errors.Is(err, postgres.ErrOrderNotFound) {
		return nil, ErrGuestOrderNotFound
	}
	if err != nil {
		return nil, err
	}
	return s.payments.InitiatePayment(ctx, g.StoreID, InitiatePaymentInput{
		OrderID:        orderID,
		IdempotencyKey: "g:" + g.SessionID.String() + ":" + key,
		Method:         domain.PaymentMethodQRIS,
	})
}

// Track resolves a public tracker token (decision QR6).
func (s *GuestService) Track(ctx context.Context, token string) (*postgres.TrackedOrder, error) {
	if len(token) != 43 {
		return nil, ErrGuestOrderNotFound
	}
	sum := sha256.Sum256([]byte(token))
	out, err := s.repo.TrackOrder(ctx, s.db, sum[:])
	if errors.Is(err, postgres.ErrTrackedOrderNotFound) {
		return nil, ErrGuestOrderNotFound
	}
	return out, err
}
