package service

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/domain"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/pkg/database"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/repository/postgres"
)

const (
	guestSplitPayWindow = 15 * time.Minute
	maxGuestSplitLabel  = 40
	splitShareTokenLen  = 43 // 32 random bytes, base64url without padding
)

var (
	ErrGuestSplitNotAllowed = errors.New("this order can no longer be divided")
	ErrSplitShareNotFound   = errors.New("split link not found or expired")
)

// GuestSplitLink carries the only copy of a share token; it is never readable again.
type GuestSplitLink struct {
	SplitNumber int    `json:"split_number"`
	Label       string `json:"label"`
	Amount      int64  `json:"amount"`
	Token       string `json:"token"`
}

// GuestSplitStatus is what the ordering guest sees while the shares come in.
type GuestSplitStatus struct {
	SplitNumber int    `json:"split_number"`
	Label       string `json:"label"`
	Amount      int64  `json:"amount"`
	Status      string `json:"status"`
}

// GuestSharePayment is the QRIS charge shown to a link holder, without order or payer details.
type GuestSharePayment struct {
	ID        uuid.UUID  `json:"id"`
	QRString  string     `json:"qr_string"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	Amount    int64      `json:"amount"`
	Status    string     `json:"status"`
}

func (s *GuestService) ownsOrder(ctx context.Context, tx *sqlx.Tx, g *GuestIdentity, orderID uuid.UUID) error {
	owner, err := s.repo.OrderGuestSession(ctx, tx, g.StoreID, orderID)
	if errors.Is(err, postgres.ErrOrderNotFound) {
		return ErrGuestOrderNotFound
	}
	if err != nil {
		return err
	}
	if owner == nil || *owner != g.SessionID {
		return ErrGuestOrderNotFound
	}
	return nil
}

// CreateSplits divides an untouched pay-first order into per-person shares. It works once per order,
// before any QR exists, so no guest can be charged against an amount that later changes.
func (s *GuestService) CreateSplits(ctx context.Context, g *GuestIdentity, orderID uuid.UUID, in CloseTabInput) ([]GuestSplitLink, error) {
	if in.Strategy != "equal" && in.Strategy != "by_item" {
		return nil, fmt.Errorf("%w: %q", ErrInvalidSplitStrategy, in.Strategy)
	}
	f := fieldErrors{}
	for i := range in.Splits {
		in.Splits[i].Label = f.text("label", in.Splits[i].Label, 0, maxGuestSplitLabel)
	}
	if err := f.err(); err != nil {
		return nil, err
	}

	var links []GuestSplitLink
	err := database.WithTenantTx(ctx, s.db, g.StoreID, func(tx *sqlx.Tx) error {
		if err := s.ownsOrder(ctx, tx, g, orderID); err != nil {
			return err
		}
		order, existing, err := s.payments.orderRepo.GetOrderByIDForUpdate(ctx, tx, g.StoreID, orderID)
		if err != nil {
			return err
		}
		now := s.nowFunc().UTC()
		if order.BillingMode != domain.BillingModePayFirst || order.Status != domain.OrderStatusPendingPayment ||
			order.PaidAmount != 0 || order.ExpiresAt == nil || !now.Before(*order.ExpiresAt) ||
			len(existing) != 1 || existing[0].Status != domain.SplitBillStatusUnpaid {
			return ErrGuestSplitNotAllowed
		}
		active, err := s.payments.paymentRepo.GetActivePaymentForSplitForUpdate(ctx, tx, g.StoreID, existing[0].ID)
		if err != nil && !errors.Is(err, postgres.ErrPaymentNotFound) {
			return err
		}
		if active != nil {
			return ErrGuestSplitNotAllowed
		}

		items, err := s.tabs.GetOrderItemsWithOptionsByOrder(ctx, tx, g.StoreID, orderID)
		if err != nil {
			return err
		}
		splits, splitItems, err := allocateSplits(order, items, in, in.Strategy, now)
		if err != nil {
			return err
		}
		if err := s.tabs.CancelPaymentSplitsTx(ctx, tx, g.StoreID, orderID); err != nil {
			return err
		}
		if err := s.tabs.CreatePaymentSplitsTx(ctx, tx, splits, splitItems); err != nil {
			return fmt.Errorf("failed to save payment splits: %w", err)
		}
		deadline := now.Add(guestSplitPayWindow)
		order.ExpiresAt = &deadline
		if err := s.payments.orderRepo.UpdateOrderStatusAndStock(ctx, tx, order); err != nil {
			return err
		}
		links = make([]GuestSplitLink, len(splits))
		for i, sp := range splits {
			token, hash, err := randomToken()
			if err != nil {
				return err
			}
			if err := s.repo.InsertSplitShare(ctx, tx, g.StoreID, orderID, sp.ID, hash, now.Add(guestSessionTTL)); err != nil {
				return err
			}
			links[i] = GuestSplitLink{SplitNumber: sp.SplitNumber, Label: sp.Label, Amount: sp.TotalAmount, Token: token}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return links, nil
}

// ListSplits reports each live share of one of this guest's orders, without tokens.
func (s *GuestService) ListSplits(ctx context.Context, g *GuestIdentity, orderID uuid.UUID) ([]GuestSplitStatus, error) {
	out := []GuestSplitStatus{}
	err := database.WithTenantTx(ctx, s.db, g.StoreID, func(tx *sqlx.Tx) error {
		if err := s.ownsOrder(ctx, tx, g, orderID); err != nil {
			return err
		}
		splits, err := s.tabs.GetPaymentSplitsByOrderID(ctx, tx, g.StoreID, orderID)
		if err != nil {
			return err
		}
		for _, sp := range splits {
			if sp.Status != domain.SplitBillStatusCancelled {
				out = append(out, GuestSplitStatus{SplitNumber: sp.SplitNumber, Label: sp.Label, Amount: sp.TotalAmount, Status: string(sp.Status)})
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *GuestService) resolveShare(ctx context.Context, token string) (*postgres.SplitShare, error) {
	if len(token) != splitShareTokenLen {
		return nil, ErrSplitShareNotFound
	}
	sum := sha256.Sum256([]byte(token))
	share, err := s.repo.ResolveSplitShare(ctx, s.db, sum[:])
	if err != nil {
		return nil, err
	}
	if share == nil || !s.nowFunc().Before(share.ExpiresAt) {
		return nil, ErrSplitShareNotFound
	}
	return share, nil
}

// SplitShare is the public view behind a link: store, label, amount and status only.
func (s *GuestService) SplitShare(ctx context.Context, token string) (*postgres.SplitShareView, error) {
	share, err := s.resolveShare(ctx, token)
	if err != nil {
		return nil, err
	}
	var view *postgres.SplitShareView
	err = database.WithTenantTx(ctx, s.db, share.StoreID, func(tx *sqlx.Tx) error {
		var err error
		view, err = s.repo.GetSplitShareView(ctx, tx, share.StoreID, share.SplitID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return view, nil
}

// PaySplitShare starts (or replays) the dynamic QRIS charge for one share.
func (s *GuestService) PaySplitShare(ctx context.Context, token, idempotencyKey string) (*GuestSharePayment, error) {
	f := fieldErrors{}
	key := f.text("idempotency_key", idempotencyKey, 1, maxGuestIdempotentKey)
	if err := f.err(); err != nil {
		return nil, err
	}
	share, err := s.resolveShare(ctx, token)
	if err != nil {
		return nil, err
	}
	res, err := s.payments.InitiatePayment(ctx, share.StoreID, InitiatePaymentInput{
		OrderID:        share.OrderID,
		SplitID:        &share.SplitID,
		IdempotencyKey: "s:" + share.SplitID.String() + ":" + key,
		Method:         domain.PaymentMethodQRIS,
	})
	if err != nil {
		return nil, err
	}
	p := res.Payment
	return &GuestSharePayment{ID: p.ID, QRString: p.QRString, ExpiresAt: p.ExpiresAt, Amount: p.Amount, Status: string(p.Status)}, nil
}
