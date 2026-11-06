package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/domain"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/rbac"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/repository/postgres"
)

// Owner promotions (K7, PR 11.1). Promotions are deactivated, never deleted: order lines keep pointing at
// the promotion that priced them.

// ErrPromotionNotFound answers 404 through the catalog error mapping.
var ErrPromotionNotFound = fmt.Errorf("promotion: %w", ErrCatalogNotFound)

const promotionColumns = `id, store_id, name, kind, value, product_ids, category_ids, starts_at, ends_at,
	days_mask, start_minute, end_minute, quota, used_count, is_active, created_at, updated_at`

// PromotionInput creates or patches a promotion. The Clear* flags remove a period, a happy hour or a quota.
type PromotionInput struct {
	Name        *string      `json:"name"`
	Kind        *string      `json:"kind"`
	Value       *int64       `json:"value"`
	ProductIDs  *[]uuid.UUID `json:"product_ids"`
	CategoryIDs *[]uuid.UUID `json:"category_ids"`
	StartsAt    *time.Time   `json:"starts_at"`
	EndsAt      *time.Time   `json:"ends_at"`
	ClearPeriod bool         `json:"clear_period"`
	DaysMask    *int         `json:"days_mask"`
	StartMinute *int         `json:"start_minute"`
	EndMinute   *int         `json:"end_minute"`
	ClearHours  bool         `json:"clear_hours"`
	Quota       *int         `json:"quota"`
	ClearQuota  bool         `json:"clear_quota"`
	IsActive    *bool        `json:"is_active"`
}

func applyPromotion(p *domain.Promotion, in PromotionInput) error {
	f := fieldErrors{}
	if in.Name != nil {
		p.Name = f.text("name", *in.Name, 1, 80)
	}
	if in.Kind != nil {
		f.oneOf("kind", *in.Kind, domain.PromotionPercent, domain.PromotionAmount)
		p.Kind = *in.Kind
	}
	if in.Value != nil {
		p.Value = *in.Value
	}
	switch p.Kind {
	case domain.PromotionPercent:
		f.intRange("value", p.Value, 1, 10000)
	case domain.PromotionAmount:
		f.intRange("value", p.Value, 1, maxItemPrice)
	}
	if in.ProductIDs != nil {
		p.ProductIDs = domain.UUIDList(*in.ProductIDs)
	}
	if in.CategoryIDs != nil {
		p.CategoryIDs = domain.UUIDList(*in.CategoryIDs)
	}
	if n := len(p.ProductIDs) + len(p.CategoryIDs); n == 0 || n > 200 {
		f.add("product_ids", "pilih 1-200 menu atau kategori")
	}
	if in.ClearPeriod {
		p.StartsAt, p.EndsAt = nil, nil
	}
	if in.StartsAt != nil {
		p.StartsAt = in.StartsAt
	}
	if in.EndsAt != nil {
		p.EndsAt = in.EndsAt
	}
	if p.StartsAt != nil && p.EndsAt != nil && !p.EndsAt.After(*p.StartsAt) {
		f.add("ends_at", "harus setelah starts_at")
	}
	if in.DaysMask != nil {
		f.intRange("days_mask", int64(*in.DaysMask), 1, 127)
		p.DaysMask = *in.DaysMask
	}
	if in.ClearHours {
		p.StartMinute, p.EndMinute = nil, nil
	}
	if in.StartMinute != nil || in.EndMinute != nil {
		if in.StartMinute == nil || in.EndMinute == nil {
			f.add("start_minute", "isi start_minute dan end_minute bersamaan")
		} else {
			f.intRange("start_minute", int64(*in.StartMinute), 0, 1439)
			f.intRange("end_minute", int64(*in.EndMinute), 1, 1440)
			if *in.StartMinute == *in.EndMinute {
				f.add("end_minute", "tidak boleh sama dengan start_minute")
			}
			p.StartMinute, p.EndMinute = in.StartMinute, in.EndMinute
		}
	}
	if in.ClearQuota {
		p.Quota = nil
	}
	if in.Quota != nil {
		f.intRange("quota", int64(*in.Quota), 1, 1_000_000)
		p.Quota = in.Quota
	}
	if in.IsActive != nil {
		p.IsActive = *in.IsActive
	}
	if p.Name == "" {
		f.add("name", "wajib diisi")
	}
	if p.Kind == "" {
		f.add("kind", "wajib diisi")
	}
	return f.err()
}

// checkPromotionTargets refuses product or category ids that are not live items of this store.
func checkPromotionTargets(ctx context.Context, tx *sqlx.Tx, storeID uuid.UUID, p *domain.Promotion) error {
	count := func(table string, ids domain.UUIDList) (int, error) {
		if len(ids) == 0 {
			return 0, nil
		}
		strs := make([]string, len(ids))
		for i, id := range ids {
			strs[i] = id.String()
		}
		var n int
		cond := ""
		if table == "products" {
			cond = " AND archived_at IS NULL"
		}
		err := tx.GetContext(ctx, &n, `SELECT COUNT(DISTINCT id) FROM `+table+` WHERE store_id = $1 AND id = ANY($2::uuid[])`+cond, storeID, strs)
		return n, err
	}
	for _, t := range []struct {
		table, field string
		ids          domain.UUIDList
	}{{"products", "product_ids", p.ProductIDs}, {"categories", "category_ids", p.CategoryIDs}} {
		n, err := count(t.table, t.ids)
		if err != nil {
			return err
		}
		if n != len(uniqueUUIDs(t.ids)) {
			return &ValidationError{Fields: map[string]string{t.field: "berisi menu atau kategori yang tidak ada di toko ini"}}
		}
	}
	return nil
}

func uniqueUUIDs(ids domain.UUIDList) map[uuid.UUID]bool {
	m := make(map[uuid.UUID]bool, len(ids))
	for _, id := range ids {
		m[id] = true
	}
	return m
}

func (s *CatalogService) ListPromotions(ctx context.Context, actor *AuthenticatedStaff) ([]domain.Promotion, error) {
	if !actor.Can(rbac.CatalogManage) {
		return nil, ErrForbidden
	}
	out := []domain.Promotion{}
	err := s.tx(ctx, actor, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &out, `SELECT `+promotionColumns+` FROM promotions WHERE store_id = $1
			ORDER BY is_active DESC, created_at DESC`, actor.Staff.Store.ID)
	})
	return out, err
}

func (s *CatalogService) CreatePromotion(ctx context.Context, actor *AuthenticatedStaff, in PromotionInput) (*domain.Promotion, error) {
	if !actor.Can(rbac.CatalogManage) {
		return nil, ErrForbidden
	}
	now := s.nowFunc().UTC()
	p := &domain.Promotion{ID: uuid.New(), StoreID: actor.Staff.Store.ID, DaysMask: 127, IsActive: true, CreatedAt: now, UpdatedAt: now,
		ProductIDs: domain.UUIDList{}, CategoryIDs: domain.UUIDList{}}
	if err := applyPromotion(p, in); err != nil {
		return nil, err
	}
	err := s.tx(ctx, actor, func(tx *sqlx.Tx) error {
		if err := checkPromotionTargets(ctx, tx, p.StoreID, p); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO promotions (id, store_id, name, kind, value, product_ids, category_ids, starts_at, ends_at,
			days_mask, start_minute, end_minute, quota, is_active, created_by, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $16)`,
			p.ID, p.StoreID, p.Name, p.Kind, p.Value, p.ProductIDs, p.CategoryIDs, p.StartsAt, p.EndsAt,
			p.DaysMask, p.StartMinute, p.EndMinute, p.Quota, p.IsActive, actor.Staff.UserID, now); err != nil {
			return fmt.Errorf("failed to create promotion: %w", err)
		}
		return s.auditPromotion(ctx, tx, actor, "PROMOTION_CREATED", "Promo dibuat: "+p.Name, p.ID, map[string]any{"after": p}, now)
	})
	if err != nil {
		return nil, err
	}
	return p, nil
}

func (s *CatalogService) UpdatePromotion(ctx context.Context, actor *AuthenticatedStaff, id uuid.UUID, in PromotionInput) (*domain.Promotion, error) {
	if !actor.Can(rbac.CatalogManage) {
		return nil, ErrForbidden
	}
	var p domain.Promotion
	err := s.tx(ctx, actor, func(tx *sqlx.Tx) error {
		err := tx.GetContext(ctx, &p, `SELECT `+promotionColumns+` FROM promotions WHERE store_id = $1 AND id = $2 FOR UPDATE`, actor.Staff.Store.ID, id)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrPromotionNotFound
		}
		if err != nil {
			return err
		}
		before := p
		if err := applyPromotion(&p, in); err != nil {
			return err
		}
		if err := checkPromotionTargets(ctx, tx, p.StoreID, &p); err != nil {
			return err
		}
		now := s.nowFunc().UTC()
		p.UpdatedAt = now
		if _, err := tx.ExecContext(ctx, `UPDATE promotions SET name = $3, kind = $4, value = $5, product_ids = $6, category_ids = $7,
			starts_at = $8, ends_at = $9, days_mask = $10, start_minute = $11, end_minute = $12, quota = $13, is_active = $14, updated_at = $15
			WHERE store_id = $1 AND id = $2`, p.StoreID, p.ID, p.Name, p.Kind, p.Value, p.ProductIDs, p.CategoryIDs,
			p.StartsAt, p.EndsAt, p.DaysMask, p.StartMinute, p.EndMinute, p.Quota, p.IsActive, now); err != nil {
			return fmt.Errorf("failed to update promotion: %w", err)
		}
		return s.auditPromotion(ctx, tx, actor, "PROMOTION_UPDATED", "Promo diubah: "+p.Name, p.ID, map[string]any{"before": before, "after": p}, now)
	})
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func (s *CatalogService) auditPromotion(ctx context.Context, tx *sqlx.Tx, actor *AuthenticatedStaff, action, reason string, id uuid.UUID, meta map[string]any, now time.Time) error {
	payload, _ := json.Marshal(meta)
	actorID := actor.Staff.UserID
	return postgres.InsertAuditLog(ctx, tx, &domain.AuditLog{StoreID: actor.Staff.Store.ID, ActorID: &actorID, ActorName: actor.Staff.FullName,
		Action: action, Reason: truncate(strings.TrimSpace(reason), 500), ReferenceID: id.String(), Metadata: string(payload), CreatedAt: now})
}
