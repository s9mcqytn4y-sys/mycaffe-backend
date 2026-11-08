package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/domain"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/pkg/rbac"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/repository/postgres"
)

// Bundle slots (X4, PR 11.2): the owner defines a bundle's parts as a whole list; editing replaces it.

// BundleSlotInput is one part of a bundle: Quantity units of one of ProductIDs.
type BundleSlotInput struct {
	Name       string      `json:"name"`
	Quantity   int         `json:"quantity"`
	ProductIDs []uuid.UUID `json:"product_ids"`
}

// SetBundleSlots replaces the slots of a bundle product.
func (s *CatalogService) SetBundleSlots(ctx context.Context, actor *AuthenticatedStaff, bundleID uuid.UUID, in []BundleSlotInput) ([]domain.BundleSlot, error) {
	if !actor.Can(rbac.CatalogManage) {
		return nil, ErrForbidden
	}
	f := fieldErrors{}
	if len(in) == 0 || len(in) > 10 {
		f.add("slots", "isi 1-10 bagian paket")
	}
	slots := make([]domain.BundleSlot, len(in))
	for i, sl := range in {
		prefix := fmt.Sprintf("slots[%d].", i)
		slots[i] = domain.BundleSlot{ID: uuid.New(), StoreID: actor.Staff.Store.ID, BundleProductID: bundleID, DisplayOrder: i,
			Name: f.text(prefix+"name", sl.Name, 1, 50), Quantity: sl.Quantity, ProductIDs: domain.UUIDList(sl.ProductIDs)}
		f.intRange(prefix+"quantity", int64(sl.Quantity), 1, 10)
		if n := len(uniqueUUIDs(slots[i].ProductIDs)); n == 0 || n > 50 || n != len(sl.ProductIDs) {
			f.add(prefix+"product_ids", "pilih 1-50 menu berbeda")
		}
	}
	if err := f.err(); err != nil {
		return nil, err
	}
	storeID := actor.Staff.Store.ID
	err := s.tx(ctx, actor, func(tx *sqlx.Tx) error {
		var productType string
		err := tx.GetContext(ctx, &productType, `SELECT product_type FROM products WHERE store_id = $1 AND id = $2 AND archived_at IS NULL FOR UPDATE`, storeID, bundleID)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrCatalogNotFound
		}
		if err != nil {
			return err
		}
		if productType != domain.ProductTypeBundle {
			return &ValidationError{Fields: map[string]string{"product_type": "menu ini bukan paket"}}
		}
		for i := range slots {
			strs := make([]string, len(slots[i].ProductIDs))
			for j, id := range slots[i].ProductIDs {
				strs[j] = id.String()
			}
			var n int
			if err := tx.GetContext(ctx, &n, `SELECT COUNT(*) FROM products WHERE store_id = $1 AND id = ANY($2::uuid[])
				AND archived_at IS NULL AND product_type <> 'bundle'`, storeID, strs); err != nil {
				return err
			}
			if n != len(strs) {
				return &ValidationError{Fields: map[string]string{fmt.Sprintf("slots[%d].product_ids", i): "berisi menu yang tidak ada, diarsipkan, atau paket lain"}}
			}
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM bundle_slots WHERE store_id = $1 AND bundle_product_id = $2`, storeID, bundleID); err != nil {
			return err
		}
		for _, sl := range slots {
			if _, err := tx.ExecContext(ctx, `INSERT INTO bundle_slots (id, store_id, bundle_product_id, name, quantity, display_order, product_ids)
				VALUES ($1, $2, $3, $4, $5, $6, $7)`, sl.ID, storeID, bundleID, sl.Name, sl.Quantity, sl.DisplayOrder, sl.ProductIDs); err != nil {
				return fmt.Errorf("failed to save bundle slot: %w", err)
			}
		}
		actorID := actor.Staff.UserID
		meta, _ := json.Marshal(map[string]any{"slots": slots})
		return postgres.InsertAuditLog(ctx, tx, &domain.AuditLog{StoreID: storeID, ActorID: &actorID, ActorName: actor.Staff.FullName,
			Action: "BUNDLE_CHANGED", Reason: "Isi paket diubah", ReferenceID: bundleID.String(), Metadata: string(meta), CreatedAt: s.nowFunc().UTC()})
	})
	if err != nil {
		return nil, err
	}
	return slots, nil
}
