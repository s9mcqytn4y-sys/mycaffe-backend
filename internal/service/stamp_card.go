package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/domain"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/database"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/rbac"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/sms"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/repository/postgres"
)

// Stamp card (v0.11.1 PR 11.8, X16, X17). One program per store; stamps are earned and redeemed when an
// order is paid. A stamp counts while it is unredeemed, unexpired and its order is not fully refunded.

const stampRewardName = "Hadiah stamp card"

// liveStamps selects a customer's usable stamps ($1 store, $2 customer, $3 now).
const liveStamps = `FROM customer_stamps s JOIN orders o ON o.store_id = s.store_id AND o.id = s.order_id
	WHERE s.store_id = $1 AND s.customer_id = $2 AND s.redeemed_order_id IS NULL AND s.expires_at > $3
	AND o.payment_status <> 'refunded'`

var ErrCustomerNotFound = fmt.Errorf("customer: %w", ErrCatalogNotFound)

type StampProgram struct {
	IsActive         bool            `json:"is_active" db:"is_active"`
	StampsRequired   int64           `json:"stamps_required" db:"stamps_required"`
	MinOrderAmount   int64           `json:"min_order_amount" db:"min_order_amount"`
	StampValidDays   int64           `json:"stamp_valid_days" db:"stamp_valid_days"`
	RewardProductIDs domain.UUIDList `json:"reward_product_ids" db:"reward_product_ids"`
}

// defaultStampProgram is what a store has before the owner saves one (X17 defaults), inactive.
func defaultStampProgram() StampProgram {
	return StampProgram{StampsRequired: 10, MinOrderAmount: 25000, StampValidDays: 180, RewardProductIDs: domain.UUIDList{}}
}

func loadStampProgramTx(ctx context.Context, tx *sqlx.Tx, storeID uuid.UUID) (StampProgram, error) {
	p := defaultStampProgram()
	err := tx.GetContext(ctx, &p, `SELECT is_active, stamps_required, min_order_amount, stamp_valid_days, reward_product_ids
		FROM stamp_programs WHERE store_id = $1`, storeID)
	if errors.Is(err, sql.ErrNoRows) {
		return p, nil
	}
	return p, err
}

func (s *CatalogService) GetStampProgram(ctx context.Context, actor *AuthenticatedStaff) (*StampProgram, error) {
	if !actor.Can(rbac.CatalogManage) {
		return nil, ErrForbidden
	}
	var p StampProgram
	err := s.tx(ctx, actor, func(tx *sqlx.Tx) error {
		var err error
		p, err = loadStampProgramTx(ctx, tx, actor.Staff.Store.ID)
		return err
	})
	return &p, err
}

// SetStampProgram replaces the store's program; reward products must be live, non-bundle items.
func (s *CatalogService) SetStampProgram(ctx context.Context, actor *AuthenticatedStaff, p StampProgram) (*StampProgram, error) {
	if !actor.Can(rbac.CatalogManage) {
		return nil, ErrForbidden
	}
	f := fieldErrors{}
	f.intRange("stamps_required", p.StampsRequired, 2, 50)
	f.intRange("min_order_amount", p.MinOrderAmount, 0, maxItemPrice)
	f.intRange("stamp_valid_days", p.StampValidDays, 1, 730)
	if p.RewardProductIDs == nil {
		p.RewardProductIDs = domain.UUIDList{}
	}
	if len(p.RewardProductIDs) > 100 || (p.IsActive && len(p.RewardProductIDs) == 0) {
		f.add("reward_product_ids", "pilih 1-100 menu hadiah")
	}
	if err := f.err(); err != nil {
		return nil, err
	}
	err := s.tx(ctx, actor, func(tx *sqlx.Tx) error {
		storeID := actor.Staff.Store.ID
		var n int
		strs := make([]string, 0, len(p.RewardProductIDs))
		for id := range uniqueUUIDs(p.RewardProductIDs) {
			strs = append(strs, id.String())
		}
		if err := tx.GetContext(ctx, &n, `SELECT COUNT(*) FROM products WHERE store_id = $1 AND id = ANY($2::uuid[])
			AND archived_at IS NULL AND product_type <> 'bundle'`, storeID, strs); err != nil {
			return err
		}
		if n != len(strs) {
			return &ValidationError{Fields: map[string]string{"reward_product_ids": "berisi menu yang tidak ada, diarsipkan, atau paket"}}
		}
		before, err := loadStampProgramTx(ctx, tx, storeID)
		if err != nil {
			return err
		}
		now := s.nowFunc().UTC()
		if _, err := tx.ExecContext(ctx, `INSERT INTO stamp_programs (store_id, is_active, stamps_required, min_order_amount, stamp_valid_days, reward_product_ids, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7) ON CONFLICT (store_id) DO UPDATE SET is_active = EXCLUDED.is_active,
			stamps_required = EXCLUDED.stamps_required, min_order_amount = EXCLUDED.min_order_amount, stamp_valid_days = EXCLUDED.stamp_valid_days,
			reward_product_ids = EXCLUDED.reward_product_ids, updated_at = EXCLUDED.updated_at`,
			storeID, p.IsActive, p.StampsRequired, p.MinOrderAmount, p.StampValidDays, p.RewardProductIDs, now); err != nil {
			return err
		}
		return s.auditPromotion(ctx, tx, actor, "STAMP_PROGRAM_CHANGED", "Program stamp card diubah", storeID, map[string]any{"before": before, "after": p}, now)
	})
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// CustomerStamps is what the cashier sees after asking for the customer's phone number.
type CustomerStamps struct {
	Customer         Customer        `json:"customer"`
	Verified         bool            `json:"verified"`
	ProgramActive    bool            `json:"program_active"`
	Stamps           int64           `json:"stamps"`
	StampsRequired   int64           `json:"stamps_required"`
	RewardReady      bool            `json:"reward_ready"`
	RewardProductIDs domain.UUIDList `json:"reward_product_ids"`
	NextExpiry       *time.Time      `json:"next_expiry,omitempty"`
}

// LookupCustomer finds a customer by phone with their live stamps (order.pos.create).
func (s *CustomerService) LookupCustomer(ctx context.Context, actor *AuthenticatedStaff, rawPhone string) (*CustomerStamps, error) {
	if !actor.Can(rbac.OrderPOSCreate) {
		return nil, ErrForbidden
	}
	phone, err := sms.NormalizePhone(rawPhone)
	if err != nil {
		return nil, &ValidationError{Fields: map[string]string{"phone": "nomor HP tidak valid (contoh 081234567890)"}}
	}
	var out CustomerStamps
	err = database.WithTenantTx(ctx, s.db, actor.Staff.Store.ID, func(tx *sqlx.Tx) error {
		storeID := actor.Staff.Store.ID
		if err := tx.GetContext(ctx, &out.Customer, `SELECT id, phone, COALESCE(name, '') AS name, phone_verified_at FROM customers
			WHERE store_id = $1 AND phone = $2`, storeID, phone); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrCustomerNotFound
			}
			return err
		}
		out.Verified = out.Customer.PhoneVerifiedAt != nil
		p, err := loadStampProgramTx(ctx, tx, storeID)
		if err != nil {
			return err
		}
		out.ProgramActive, out.StampsRequired, out.RewardProductIDs = p.IsActive, p.StampsRequired, p.RewardProductIDs
		if err := tx.QueryRowxContext(ctx, `SELECT COUNT(*), MIN(s.expires_at) `+liveStamps, storeID, out.Customer.ID, s.nowFunc().UTC()).
			Scan(&out.Stamps, &out.NextExpiry); err != nil {
			return err
		}
		out.RewardReady = p.IsActive && out.Verified && out.Stamps >= p.StampsRequired
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// applyCustomerTx checks the order's customer and, for a reward, that the program, product and stamps allow it.
func applyCustomerTx(ctx context.Context, tx *sqlx.Tx, storeID uuid.UUID, in OrderCreationInput, snap *CatalogSnapshot, now time.Time) error {
	var verified bool
	if err := tx.GetContext(ctx, &verified, `SELECT phone_verified_at IS NOT NULL FROM customers WHERE store_id = $1 AND id = $2`,
		storeID, *in.CustomerID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: customer %s not found", ErrInvalidOrderInput, *in.CustomerID)
		}
		return err
	}
	if !verified {
		return fmt.Errorf("%w: the customer's phone number is not verified", ErrInvalidOrderInput)
	}
	if in.RewardItemIndex == nil {
		return nil
	}
	idx := *in.RewardItemIndex
	if in.SalesChannelID != nil || idx < 0 || idx >= len(in.Items) {
		return fmt.Errorf("%w: reward_item_index must point at an item of a direct order", ErrInvalidOrderInput)
	}
	p, err := loadStampProgramTx(ctx, tx, storeID)
	if err != nil {
		return err
	}
	if !p.IsActive || !uniqueUUIDs(p.RewardProductIDs)[in.Items[idx].ProductID] {
		return fmt.Errorf("%w: this item is not a stamp card reward", ErrInvalidOrderInput)
	}
	var live int64
	if err := tx.GetContext(ctx, &live, `SELECT COUNT(*) `+liveStamps, storeID, *in.CustomerID, now); err != nil {
		return err
	}
	if live < p.StampsRequired {
		return fmt.Errorf("%w: the customer has %d of %d stamps", ErrInvalidOrderInput, live, p.StampsRequired)
	}
	snap.RewardItem = &idx
	return nil
}

// settleStampsTx runs when an order is paid: it spends the stamps of a reward order, then earns this order's
// stamp. Both are idempotent per order.
func settleStampsTx(ctx context.Context, tx *sqlx.Tx, storeID, orderID uuid.UUID, now time.Time) error {
	var o struct {
		CustomerID *uuid.UUID `db:"customer_id"`
		Channel    bool       `db:"channel"`
		Total      int64      `db:"total_amount"`
		Reward     bool       `db:"stamp_reward"`
		Number     string     `db:"order_number"`
	}
	if err := tx.GetContext(ctx, &o, `SELECT customer_id, sales_channel_id IS NOT NULL AS channel, total_amount, stamp_reward, order_number
		FROM orders WHERE store_id = $1 AND id = $2`, storeID, orderID); err != nil {
		return err
	}
	if o.CustomerID == nil {
		return nil
	}
	p, err := loadStampProgramTx(ctx, tx, storeID)
	if err != nil {
		return err
	}
	if o.Reward {
		var done bool
		if err := tx.GetContext(ctx, &done, `SELECT EXISTS (SELECT 1 FROM customer_stamps WHERE store_id = $1 AND redeemed_order_id = $2)
			OR (SELECT stamp_reward_unbacked FROM orders WHERE store_id = $1 AND id = $2)`, storeID, orderID); err != nil {
			return err
		}
		if !done {
			var ids []uuid.UUID
			if err := tx.SelectContext(ctx, &ids, `SELECT s.id `+liveStamps+` ORDER BY s.earned_at LIMIT $4 FOR UPDATE OF s`,
				storeID, *o.CustomerID, now, p.StampsRequired); err != nil {
				return err
			}
			action, meta := "STAMP_REWARD_REDEEMED", map[string]any{"order_id": orderID, "stamps": len(ids)}
			if int64(len(ids)) < p.StampsRequired {
				// The stamps were spent by another order in the meantime: the paid order keeps its free item (X3 style).
				meta["unbacked"] = true
				if _, err := tx.ExecContext(ctx, `UPDATE orders SET stamp_reward_unbacked = true WHERE store_id = $1 AND id = $2`, storeID, orderID); err != nil {
					return err
				}
			} else {
				for _, id := range ids {
					if _, err := tx.ExecContext(ctx, `UPDATE customer_stamps SET redeemed_order_id = $3, redeemed_at = $4 WHERE store_id = $1 AND id = $2`,
						storeID, id, orderID, now); err != nil {
						return err
					}
				}
			}
			payload, _ := json.Marshal(meta)
			if err := postgres.InsertAuditLog(ctx, tx, &domain.AuditLog{StoreID: storeID, ActorName: "Sistem", Action: action,
				Reason: "Hadiah stamp card pesanan " + o.Number, ReferenceID: o.Number, Metadata: string(payload), CreatedAt: now}); err != nil {
				return err
			}
		}
	}
	if !p.IsActive || o.Channel || o.Total < p.MinOrderAmount {
		return nil
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO customer_stamps (store_id, customer_id, order_id, earned_at, expires_at)
		VALUES ($1, $2, $3, $4, $5) ON CONFLICT (store_id, order_id) DO NOTHING`,
		storeID, *o.CustomerID, orderID, now, now.AddDate(0, 0, int(p.StampValidDays)))
	return err
}
