package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/domain"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/rbac"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/repository/postgres"
)

// Sales channels (v0.11.1 PR 11.7, X12-X15): delivery apps whose orders the cashier types in at the POS. Each
// channel has its own price per variant and a commission estimate; channels are deactivated, never deleted.

var ErrSalesChannelNotFound = fmt.Errorf("sales channel: %w", ErrCatalogNotFound)

type SalesChannel struct {
	ID            uuid.UUID      `json:"id" db:"id"`
	Name          string         `json:"name" db:"name"`
	CommissionBPS int64          `json:"commission_bps" db:"commission_bps"`
	IsActive      bool           `json:"is_active" db:"is_active"`
	UpdatedAt     time.Time      `json:"updated_at" db:"updated_at"`
	Prices        []ChannelPrice `json:"prices" db:"-"`
}

type ChannelPrice struct {
	VariantID uuid.UUID `json:"variant_id" db:"variant_id"`
	Price     int64     `json:"price" db:"price"`
}

type SalesChannelInput struct {
	Name          *string `json:"name"`
	CommissionBPS *int64  `json:"commission_bps"`
	IsActive      *bool   `json:"is_active"`
}

// ChannelPriceInput sets one variant's channel price; a nil price removes it (back to the normal price).
type ChannelPriceInput struct {
	VariantID uuid.UUID `json:"variant_id"`
	Price     *int64    `json:"price"`
}

const salesChannelColumns = `id, name, commission_bps, is_active, updated_at`

func applySalesChannel(c *SalesChannel, in SalesChannelInput) error {
	f := fieldErrors{}
	if in.Name != nil {
		c.Name = f.text("name", *in.Name, 1, 50)
	}
	if in.CommissionBPS != nil {
		f.intRange("commission_bps", *in.CommissionBPS, 0, 10000)
		c.CommissionBPS = *in.CommissionBPS
	}
	if in.IsActive != nil {
		c.IsActive = *in.IsActive
	}
	if c.Name == "" {
		f.add("name", "wajib diisi")
	}
	return f.err()
}

func channelConflict(err error) error {
	if _, ok := postgres.UniqueViolation(err); ok {
		return &ConflictError{Field: "name", Message: "nama kanal sudah dipakai"}
	}
	return err
}

// ListSalesChannels returns every channel with its prices; activeOnly is the cashier's view at the POS.
func (s *CatalogService) ListSalesChannels(ctx context.Context, actor *AuthenticatedStaff, activeOnly bool) ([]SalesChannel, error) {
	if !activeOnly && !actor.Can(rbac.CatalogManage) {
		return nil, ErrForbidden
	}
	out := []SalesChannel{}
	err := s.tx(ctx, actor, func(tx *sqlx.Tx) error {
		storeID := actor.Staff.Store.ID
		if err := tx.SelectContext(ctx, &out, `SELECT `+salesChannelColumns+` FROM sales_channels WHERE store_id = $1 AND (is_active OR NOT $2)
			ORDER BY is_active DESC, name`, storeID, activeOnly); err != nil {
			return err
		}
		for i := range out {
			out[i].Prices = []ChannelPrice{}
			if err := tx.SelectContext(ctx, &out[i].Prices, `SELECT variant_id, price FROM channel_prices WHERE store_id = $1 AND sales_channel_id = $2
				ORDER BY variant_id`, storeID, out[i].ID); err != nil {
				return err
			}
		}
		return nil
	})
	return out, err
}

func (s *CatalogService) CreateSalesChannel(ctx context.Context, actor *AuthenticatedStaff, in SalesChannelInput) (*SalesChannel, error) {
	if !actor.Can(rbac.CatalogManage) {
		return nil, ErrForbidden
	}
	c := SalesChannel{ID: uuid.New(), IsActive: true, Prices: []ChannelPrice{}}
	if err := applySalesChannel(&c, in); err != nil {
		return nil, err
	}
	err := s.tx(ctx, actor, func(tx *sqlx.Tx) error {
		now := s.nowFunc().UTC()
		if err := tx.GetContext(ctx, &c.UpdatedAt, `INSERT INTO sales_channels (id, store_id, name, commission_bps, is_active, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $6) RETURNING updated_at`, c.ID, actor.Staff.Store.ID, c.Name, c.CommissionBPS, c.IsActive, now); err != nil {
			return channelConflict(err)
		}
		return s.auditPromotion(ctx, tx, actor, "SALES_CHANNEL_CHANGED", "Kanal penjualan dibuat: "+c.Name, c.ID, map[string]any{"after": c}, now)
	})
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func (s *CatalogService) UpdateSalesChannel(ctx context.Context, actor *AuthenticatedStaff, id uuid.UUID, in SalesChannelInput) (*SalesChannel, error) {
	if !actor.Can(rbac.CatalogManage) {
		return nil, ErrForbidden
	}
	var c SalesChannel
	err := s.tx(ctx, actor, func(tx *sqlx.Tx) error {
		storeID := actor.Staff.Store.ID
		if err := tx.GetContext(ctx, &c, `SELECT `+salesChannelColumns+` FROM sales_channels WHERE store_id = $1 AND id = $2 FOR UPDATE`, storeID, id); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrSalesChannelNotFound
			}
			return err
		}
		before := c
		if err := applySalesChannel(&c, in); err != nil {
			return err
		}
		now := s.nowFunc().UTC()
		if _, err := tx.ExecContext(ctx, `UPDATE sales_channels SET name = $3, commission_bps = $4, is_active = $5, updated_at = $6
			WHERE store_id = $1 AND id = $2`, storeID, id, c.Name, c.CommissionBPS, c.IsActive, now); err != nil {
			return channelConflict(err)
		}
		c.UpdatedAt = now
		c.Prices = []ChannelPrice{}
		if err := tx.SelectContext(ctx, &c.Prices, `SELECT variant_id, price FROM channel_prices WHERE store_id = $1 AND sales_channel_id = $2
			ORDER BY variant_id`, storeID, id); err != nil {
			return err
		}
		return s.auditPromotion(ctx, tx, actor, "SALES_CHANNEL_CHANGED", "Kanal penjualan diubah: "+c.Name, id,
			map[string]any{"before": before, "after": c}, now)
	})
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// SetChannelPrices upserts or removes channel prices for the given variants (other variants are untouched).
func (s *CatalogService) SetChannelPrices(ctx context.Context, actor *AuthenticatedStaff, id uuid.UUID, prices []ChannelPriceInput) (*SalesChannel, error) {
	if !actor.Can(rbac.CatalogManage) {
		return nil, ErrForbidden
	}
	f := fieldErrors{}
	if len(prices) == 0 || len(prices) > 500 {
		f.add("prices", "isi 1-500 harga")
	}
	seen := map[uuid.UUID]bool{}
	for i, p := range prices {
		if seen[p.VariantID] {
			f.add(fmt.Sprintf("prices[%d].variant_id", i), "varian yang sama muncul dua kali")
		}
		seen[p.VariantID] = true
		if p.Price != nil {
			f.intRange(fmt.Sprintf("prices[%d].price", i), *p.Price, 0, maxItemPrice)
		}
	}
	if err := f.err(); err != nil {
		return nil, err
	}
	err := s.tx(ctx, actor, func(tx *sqlx.Tx) error {
		storeID := actor.Staff.Store.ID
		var name string
		if err := tx.GetContext(ctx, &name, `SELECT name FROM sales_channels WHERE store_id = $1 AND id = $2 FOR UPDATE`, storeID, id); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrSalesChannelNotFound
			}
			return err
		}
		now := s.nowFunc().UTC()
		for i, p := range prices {
			if p.Price == nil {
				if _, err := tx.ExecContext(ctx, `DELETE FROM channel_prices WHERE store_id = $1 AND sales_channel_id = $2 AND variant_id = $3`,
					storeID, id, p.VariantID); err != nil {
					return err
				}
				continue
			}
			var ok bool
			if err := tx.GetContext(ctx, &ok, `SELECT EXISTS (SELECT 1 FROM product_variants WHERE store_id = $1 AND id = $2 AND archived_at IS NULL)`,
				storeID, p.VariantID); err != nil {
				return err
			}
			if !ok {
				return &ValidationError{Fields: map[string]string{fmt.Sprintf("prices[%d].variant_id", i): "varian tidak ada di toko ini"}}
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO channel_prices (store_id, sales_channel_id, variant_id, price, updated_at)
				VALUES ($1, $2, $3, $4, $5) ON CONFLICT (store_id, sales_channel_id, variant_id) DO UPDATE SET price = EXCLUDED.price, updated_at = EXCLUDED.updated_at`,
				storeID, id, p.VariantID, *p.Price, now); err != nil {
				return err
			}
		}
		return s.auditPromotion(ctx, tx, actor, "CHANNEL_PRICES_CHANGED", "Harga kanal diubah: "+name, id, map[string]any{"prices": prices}, now)
	})
	if err != nil {
		return nil, err
	}
	return s.UpdateSalesChannel(ctx, actor, id, SalesChannelInput{})
}

// channelOrder holds what a channel order fixes at creation (X13-X15).
type channelOrder struct {
	ID            uuid.UUID
	Name          string
	CommissionBPS int64
}

// applySalesChannelTx prices an order for a channel: channel prices replace variant prices, and promotions,
// service charge and tax do not apply (X15). The cashier discount is refused separately.
func applySalesChannelTx(ctx context.Context, tx *sqlx.Tx, storeID, channelID uuid.UUID, store *domain.Store, snap *CatalogSnapshot) (*channelOrder, error) {
	var ch channelOrder
	if err := tx.QueryRowxContext(ctx, `SELECT id, name, commission_bps FROM sales_channels WHERE store_id = $1 AND id = $2 AND is_active`,
		storeID, channelID).Scan(&ch.ID, &ch.Name, &ch.CommissionBPS); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("%w: sales channel %s is not active in this store", ErrInvalidOrderInput, channelID)
		}
		return nil, err
	}
	var prices []ChannelPrice
	if err := tx.SelectContext(ctx, &prices, `SELECT variant_id, price FROM channel_prices WHERE store_id = $1 AND sales_channel_id = $2`,
		storeID, channelID); err != nil {
		return nil, err
	}
	variants := make(map[uuid.UUID]domain.ProductVariant, len(snap.Variants))
	for id, v := range snap.Variants {
		variants[id] = v
	}
	for _, p := range prices {
		if v, ok := variants[p.VariantID]; ok {
			v.Price = p.Price
			variants[p.VariantID] = v
		}
	}
	snap.Variants, snap.Promotions = variants, nil
	store.ServiceBps, store.TaxBps = 0, 0
	return &ch, nil
}
