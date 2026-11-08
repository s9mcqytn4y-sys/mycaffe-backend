package service

import (
	"context"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/domain"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/pkg/rbac"
)

// ---------- Categories (Catalog C1) ----------

type CategoryInput struct {
	Name         *string `json:"name"`
	DisplayOrder *int    `json:"display_order"`
	IsActive     *bool   `json:"is_active"`
}

func (s *CatalogService) CreateCategory(ctx context.Context, actor *AuthenticatedStaff, in CategoryInput) (*domain.Category, error) {
	if !actor.Can(rbac.CatalogManage) {
		return nil, ErrForbidden
	}
	c := &domain.Category{ID: uuid.New(), StoreID: actor.Staff.Store.ID, IsActive: true}
	if in.Name == nil {
		return nil, &ValidationError{Fields: map[string]string{"name": "wajib diisi"}}
	}
	if err := applyCategory(c, in); err != nil {
		return nil, err
	}
	err := s.tx(ctx, actor, func(tx *sqlx.Tx) error { return s.repo.InsertCategory(ctx, tx, c) })
	return c, mapConflict(err)
}

func (s *CatalogService) UpdateCategory(ctx context.Context, actor *AuthenticatedStaff, id uuid.UUID, in CategoryInput) (*domain.Category, error) {
	if !actor.Can(rbac.CatalogManage) {
		return nil, ErrForbidden
	}
	var c *domain.Category
	err := s.tx(ctx, actor, func(tx *sqlx.Tx) error {
		var err error
		if c, err = s.repo.GetCategoryForUpdate(ctx, tx, actor.Staff.Store.ID, id); err != nil {
			return err
		}
		if err := applyCategory(c, in); err != nil {
			return err
		}
		return s.repo.UpdateCategory(ctx, tx, c)
	})
	return c, mapConflict(err)
}

func (s *CatalogService) DeleteCategory(ctx context.Context, actor *AuthenticatedStaff, id uuid.UUID) error {
	if !actor.Can(rbac.CatalogManage) {
		return ErrForbidden
	}
	return mapConflict(s.tx(ctx, actor, func(tx *sqlx.Tx) error { return s.repo.DeleteCategory(ctx, tx, actor.Staff.Store.ID, id) }))
}

func applyCategory(c *domain.Category, in CategoryInput) error {
	f := fieldErrors{}
	if in.Name != nil {
		c.Name = f.text("name", *in.Name, 1, 100)
	}
	if in.DisplayOrder != nil {
		f.intRange("display_order", int64(*in.DisplayOrder), 0, 10000)
		c.DisplayOrder = *in.DisplayOrder
	}
	if in.IsActive != nil {
		c.IsActive = *in.IsActive
	}
	return f.err()
}
