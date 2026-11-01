package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/domain"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/rbac"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/repository/postgres"
)

// ---------- Modifier groups & options (Iteration 4a, decision P1) ----------

type ModifierGroupInput struct {
	Name          *string `json:"name"`
	SelectionType *string `json:"selection_type"`
	IsRequired    *bool   `json:"is_required"`
	MinSelect     *int    `json:"min_select"`
	MaxSelect     *int    `json:"max_select"`
	DisplayOrder  *int    `json:"display_order"`
	IsActive      *bool   `json:"is_active"`
}

type ModifierOptionInput struct {
	Name          *string `json:"name"`
	PriceDelta    *int64  `json:"price_delta"`
	IsAvailable   *bool   `json:"is_available"`
	TrackStock    *bool   `json:"track_stock"`
	StockQuantity *int    `json:"stock_quantity"`
	MaxQuantity   *int    `json:"max_quantity"`
	DisplayOrder  *int    `json:"display_order"`
}

type AttachModifierGroupInput struct {
	GroupID          uuid.UUID `json:"group_id"`
	DisplayOrder     int       `json:"display_order"`
	OverrideRequired *bool     `json:"override_required,omitempty"`
}

type AttachChildGroupInput struct {
	ChildGroupID uuid.UUID `json:"child_group_id"`
	DisplayOrder int       `json:"display_order"`
}

func (s *CatalogService) CreateModifierGroup(ctx context.Context, actor *AuthenticatedStaff, in ModifierGroupInput) (*domain.ModifierGroup, error) {
	if !actor.Can(rbac.CatalogManage) {
		return nil, ErrForbidden
	}
	if in.Name == nil {
		return nil, &ValidationError{Fields: map[string]string{"name": "wajib diisi"}}
	}
	selType := "single"
	if in.SelectionType != nil {
		selType = *in.SelectionType
	}
	minSel := 0
	if in.MinSelect != nil {
		minSel = *in.MinSelect
	}
	maxSel := 1
	if in.MaxSelect != nil {
		maxSel = *in.MaxSelect
	}
	g := &domain.ModifierGroup{
		ID:            uuid.New(),
		StoreID:       actor.Staff.Store.ID,
		SelectionType: selType,
		MinSelect:     minSel,
		MaxSelect:     maxSel,
		IsActive:      true,
	}
	if err := applyModifierGroup(g, in); err != nil {
		return nil, err
	}
	err := s.tx(ctx, actor, func(tx *sqlx.Tx) error {
		return s.repo.InsertModifierGroup(ctx, tx, g)
	})
	return g, mapConflict(err)
}

func (s *CatalogService) GetModifierGroup(ctx context.Context, actor *AuthenticatedStaff, id uuid.UUID) (*domain.ModifierGroup, error) {
	if !actor.Can(rbac.CatalogManage) {
		return nil, ErrForbidden
	}
	var g *domain.ModifierGroup
	err := s.tx(ctx, actor, func(tx *sqlx.Tx) error {
		var err error
		g, err = s.repo.GetModifierGroup(ctx, tx, actor.Staff.Store.ID, id)
		if err != nil {
			return err
		}
		opts, err := s.repo.ListModifierOptions(ctx, tx, actor.Staff.Store.ID, id, false)
		if err != nil {
			return err
		}
		g.Options = opts
		return nil
	})
	return g, mapConflict(err)
}

func (s *CatalogService) ListModifierGroups(ctx context.Context, actor *AuthenticatedStaff, includeArchived bool) ([]domain.ModifierGroup, error) {
	if !actor.Can(rbac.CatalogManage) {
		return nil, ErrForbidden
	}
	var gs []domain.ModifierGroup
	err := s.tx(ctx, actor, func(tx *sqlx.Tx) error {
		var err error
		gs, err = s.repo.ListModifierGroups(ctx, tx, actor.Staff.Store.ID, includeArchived)
		if err != nil {
			return err
		}
		for i := range gs {
			opts, err := s.repo.ListModifierOptions(ctx, tx, actor.Staff.Store.ID, gs[i].ID, includeArchived)
			if err != nil {
				return err
			}
			gs[i].Options = opts
		}
		return nil
	})
	return gs, mapConflict(err)
}

func (s *CatalogService) UpdateModifierGroup(ctx context.Context, actor *AuthenticatedStaff, id uuid.UUID, in ModifierGroupInput) (*domain.ModifierGroup, error) {
	if !actor.Can(rbac.CatalogManage) {
		return nil, ErrForbidden
	}
	var g *domain.ModifierGroup
	err := s.tx(ctx, actor, func(tx *sqlx.Tx) error {
		var err error
		g, err = s.repo.GetModifierGroupForUpdate(ctx, tx, actor.Staff.Store.ID, id)
		if err != nil {
			return err
		}
		if err := applyModifierGroup(g, in); err != nil {
			return err
		}
		return s.repo.UpdateModifierGroup(ctx, tx, g)
	})
	return g, mapConflict(err)
}

func (s *CatalogService) ArchiveModifierGroup(ctx context.Context, actor *AuthenticatedStaff, id uuid.UUID, forceUnlink bool) error {
	if !actor.Can(rbac.CatalogManage) {
		return ErrForbidden
	}
	return mapConflict(s.tx(ctx, actor, func(tx *sqlx.Tx) error {
		count, err := s.repo.CountProductsLinkedToGroup(ctx, tx, actor.Staff.Store.ID, id)
		if err != nil {
			return err
		}
		if count > 0 && !forceUnlink {
			return &ConflictError{
				Field:   "group_id",
				Message: fmt.Sprintf("grup modifier masih terhubung ke %d menu; gunakan force=true untuk memutus relasi", count),
			}
		}
		optCount, err := s.repo.CountParentOptionsLinkedToGroup(ctx, tx, actor.Staff.Store.ID, id)
		if err != nil {
			return err
		}
		if optCount > 0 && !forceUnlink {
			return &ConflictError{
				Field:   "group_id",
				Message: fmt.Sprintf("grup modifier masih menjadi sub-grup dari %d opsi modifier; gunakan force=true untuk memutus relasi", optCount),
			}
		}
		return s.repo.ArchiveModifierGroup(ctx, tx, actor.Staff.Store.ID, id, s.nowFunc().UTC(), forceUnlink)
	}))
}

func (s *CatalogService) AttachModifierGroupToProduct(ctx context.Context, actor *AuthenticatedStaff, productID uuid.UUID, in AttachModifierGroupInput) error {
	if !actor.Can(rbac.CatalogManage) {
		return ErrForbidden
	}
	if in.GroupID == uuid.Nil {
		return &ValidationError{Fields: map[string]string{"group_id": "wajib diisi"}}
	}
	return mapConflict(s.tx(ctx, actor, func(tx *sqlx.Tx) error {
		if _, err := s.repo.GetProductForUpdate(ctx, tx, actor.Staff.Store.ID, productID); err != nil {
			return err
		}
		group, err := s.repo.GetModifierGroup(ctx, tx, actor.Staff.Store.ID, in.GroupID)
		if err != nil {
			return err
		}
		if group.ArchivedAt != nil {
			return &ValidationError{Fields: map[string]string{"group_id": "grup modifier telah diarsipkan"}}
		}
		return s.repo.AttachModifierGroupToProduct(ctx, tx, &domain.ProductModifierGroup{
			StoreID:          actor.Staff.Store.ID,
			ProductID:        productID,
			GroupID:          in.GroupID,
			DisplayOrder:     in.DisplayOrder,
			OverrideRequired: in.OverrideRequired,
		})
	}))
}

func (s *CatalogService) DetachModifierGroupFromProduct(ctx context.Context, actor *AuthenticatedStaff, productID, groupID uuid.UUID) error {
	if !actor.Can(rbac.CatalogManage) {
		return ErrForbidden
	}
	return mapConflict(s.tx(ctx, actor, func(tx *sqlx.Tx) error {
		return s.repo.DetachModifierGroupFromProduct(ctx, tx, actor.Staff.Store.ID, productID, groupID)
	}))
}

func (s *CatalogService) CreateModifierOption(ctx context.Context, actor *AuthenticatedStaff, groupID uuid.UUID, in ModifierOptionInput) (*domain.ModifierOption, error) {
	if !actor.Can(rbac.CatalogManage) {
		return nil, ErrForbidden
	}
	if in.Name == nil {
		return nil, &ValidationError{Fields: map[string]string{"name": "wajib diisi"}}
	}
	delta := int64(0)
	if in.PriceDelta != nil {
		delta = *in.PriceDelta
	}
	maxQty := 1
	if in.MaxQuantity != nil {
		maxQty = *in.MaxQuantity
	}
	opt := &domain.ModifierOption{
		ID:          uuid.New(),
		StoreID:     actor.Staff.Store.ID,
		GroupID:     groupID,
		PriceDelta:  delta,
		IsAvailable: true,
		MaxQuantity: maxQty,
	}
	if err := applyModifierOption(opt, in); err != nil {
		return nil, err
	}
	err := s.tx(ctx, actor, func(tx *sqlx.Tx) error {
		group, err := s.repo.GetModifierGroup(ctx, tx, actor.Staff.Store.ID, groupID)
		if err != nil {
			return err
		}
		if group.ArchivedAt != nil {
			return &ValidationError{Fields: map[string]string{"group_id": "grup modifier telah diarsipkan"}}
		}
		return s.repo.InsertModifierOption(ctx, tx, opt)
	})
	return opt, mapConflict(err)
}

func (s *CatalogService) UpdateModifierOption(ctx context.Context, actor *AuthenticatedStaff, groupID, optionID uuid.UUID, in ModifierOptionInput) (*domain.ModifierOption, error) {
	if !actor.Can(rbac.CatalogManage) {
		return nil, ErrForbidden
	}
	var opt *domain.ModifierOption
	err := s.tx(ctx, actor, func(tx *sqlx.Tx) error {
		var err error
		opt, err = s.repo.GetModifierOptionForUpdate(ctx, tx, actor.Staff.Store.ID, optionID)
		if err != nil {
			return err
		}
		if opt.GroupID != groupID {
			return ErrCatalogNotFound
		}
		oldDelta := opt.PriceDelta
		if err := applyModifierOption(opt, in); err != nil {
			return err
		}
		if err := s.repo.UpdateModifierOption(ctx, tx, opt); err != nil {
			return err
		}
		if in.PriceDelta != nil && *in.PriceDelta != oldDelta {
			meta, _ := json.Marshal(map[string]any{"option_id": opt.ID, "group_id": opt.GroupID, "old_delta": oldDelta, "new_delta": opt.PriceDelta})
			actorID := actor.Staff.UserID
			if err := postgres.InsertAuditLog(ctx, tx, &domain.AuditLog{
				StoreID:     actor.Staff.Store.ID,
				ActorID:     &actorID,
				ActorName:   actor.Staff.FullName,
				Action:      "PRICE_CHANGE",
				Reason:      fmt.Sprintf("Harga delta opsi modifier %q diubah dari Rp%d ke Rp%d", opt.Name, oldDelta, opt.PriceDelta),
				ReferenceID: opt.ID.String(),
				Metadata:    string(meta),
			}); err != nil {
				return fmt.Errorf("failed to audit modifier price change: %w", err)
			}
		}
		return nil
	})
	return opt, mapConflict(err)
}

func (s *CatalogService) ArchiveModifierOption(ctx context.Context, actor *AuthenticatedStaff, groupID, optionID uuid.UUID) error {
	if !actor.Can(rbac.CatalogManage) {
		return ErrForbidden
	}
	return mapConflict(s.tx(ctx, actor, func(tx *sqlx.Tx) error {
		opt, err := s.repo.GetModifierOptionForUpdate(ctx, tx, actor.Staff.Store.ID, optionID)
		if err != nil {
			return err
		}
		if opt.GroupID != groupID {
			return ErrCatalogNotFound
		}
		return s.repo.ArchiveModifierOption(ctx, tx, actor.Staff.Store.ID, optionID, s.nowFunc().UTC())
	}))
}

func (s *CatalogService) AttachChildGroupToOption(ctx context.Context, actor *AuthenticatedStaff, optionID uuid.UUID, in AttachChildGroupInput) error {
	if !actor.Can(rbac.CatalogManage) {
		return ErrForbidden
	}
	if in.ChildGroupID == uuid.Nil {
		return &ValidationError{Fields: map[string]string{"child_group_id": "wajib diisi"}}
	}
	return mapConflict(s.tx(ctx, actor, func(tx *sqlx.Tx) error {
		opt, err := s.repo.GetModifierOptionForUpdate(ctx, tx, actor.Staff.Store.ID, optionID)
		if err != nil {
			return err
		}
		if opt.ArchivedAt != nil {
			return &ValidationError{Fields: map[string]string{"parent_option_id": "opsi modifier telah diarsipkan"}}
		}
		childGroup, err := s.repo.GetModifierGroupForUpdate(ctx, tx, actor.Staff.Store.ID, in.ChildGroupID)
		if err != nil {
			return err
		}
		if childGroup.ArchivedAt != nil {
			return &ValidationError{Fields: map[string]string{"child_group_id": "grup modifier telah diarsipkan"}}
		}
		// Cycle prevention: option cannot have its own parent group as child
		if opt.GroupID == childGroup.ID {
			return &ValidationError{Fields: map[string]string{"child_group_id": "grup modifier tidak boleh menjadi sub-grup dari opsinya sendiri"}}
		}
		// Depth 1 check 1: Child group must not have options that themselves have child groups
		hasNested, err := s.repo.HasNestedChildGroups(ctx, tx, actor.Staff.Store.ID, childGroup.ID)
		if err != nil {
			return err
		}
		if hasNested {
			return &ValidationError{Fields: map[string]string{"child_group_id": "grup turunan tidak boleh memiliki sub-grup lagi (maksimal kedalaman 1 level)"}}
		}
		// Depth 1 check 2: The parent option's group must not already be attached as a child group
		isChild, err := s.repo.IsAttachedAsChildGroup(ctx, tx, actor.Staff.Store.ID, opt.GroupID)
		if err != nil {
			return err
		}
		if isChild {
			return &ValidationError{Fields: map[string]string{"parent_option_id": "opsi pada sub-grup tidak dapat memiliki sub-grup lagi (maksimal kedalaman 1 level)"}}
		}

		return s.repo.AttachChildGroupToOption(ctx, tx, &domain.OptionModifierGroup{
			StoreID:        actor.Staff.Store.ID,
			ParentOptionID: optionID,
			ChildGroupID:   in.ChildGroupID,
			DisplayOrder:   in.DisplayOrder,
		})
	}))
}

func (s *CatalogService) DetachChildGroupFromOption(ctx context.Context, actor *AuthenticatedStaff, optionID, childGroupID uuid.UUID) error {
	if !actor.Can(rbac.CatalogManage) {
		return ErrForbidden
	}
	return mapConflict(s.tx(ctx, actor, func(tx *sqlx.Tx) error {
		return s.repo.DetachChildGroupFromOption(ctx, tx, actor.Staff.Store.ID, optionID, childGroupID)
	}))
}

func applyModifierGroup(g *domain.ModifierGroup, in ModifierGroupInput) error {
	f := fieldErrors{}
	if in.Name != nil {
		g.Name = f.text("name", *in.Name, 1, 100)
	}
	if in.SelectionType != nil {
		sel := strings.ToLower(strings.TrimSpace(*in.SelectionType))
		if sel != "single" && sel != "multiple" {
			f.add("selection_type", "harus single atau multiple")
		} else {
			g.SelectionType = sel
		}
	}
	if in.IsRequired != nil {
		g.IsRequired = *in.IsRequired
	}
	if in.MinSelect != nil {
		f.intRange("min_select", int64(*in.MinSelect), 0, 100)
		g.MinSelect = *in.MinSelect
	}
	if in.MaxSelect != nil {
		f.intRange("max_select", int64(*in.MaxSelect), 1, 100)
		g.MaxSelect = *in.MaxSelect
	}
	if g.MinSelect > g.MaxSelect {
		f.add("min_select", "min_select tidak boleh lebih besar dari max_select")
	}
	if g.SelectionType == "single" {
		if g.MaxSelect > 1 {
			f.add("max_select", "grup dengan selection_type single hanya memperbolehkan max_select 1")
		}
		if g.MinSelect > 1 {
			f.add("min_select", "grup dengan selection_type single hanya memperbolehkan min_select maksimal 1")
		}
	}
	if in.DisplayOrder != nil {
		f.intRange("display_order", int64(*in.DisplayOrder), 0, 10000)
		g.DisplayOrder = *in.DisplayOrder
	}
	if in.IsActive != nil {
		g.IsActive = *in.IsActive
	}
	return f.err()
}

func applyModifierOption(opt *domain.ModifierOption, in ModifierOptionInput) error {
	f := fieldErrors{}
	if in.Name != nil {
		opt.Name = f.text("name", *in.Name, 1, 100)
	}
	if in.PriceDelta != nil {
		f.intRange("price_delta", *in.PriceDelta, -100_000_000, 100_000_000)
		opt.PriceDelta = *in.PriceDelta
	}
	if in.IsAvailable != nil {
		opt.IsAvailable = *in.IsAvailable
	}
	if in.TrackStock != nil {
		opt.TrackStock = *in.TrackStock
	}
	if in.StockQuantity != nil {
		f.intRange("stock_quantity", int64(*in.StockQuantity), 0, 1_000_000)
		opt.StockQuantity = *in.StockQuantity
	}
	if in.MaxQuantity != nil {
		f.intRange("max_quantity", int64(*in.MaxQuantity), 1, 100)
		opt.MaxQuantity = *in.MaxQuantity
	}
	if in.DisplayOrder != nil {
		f.intRange("display_order", int64(*in.DisplayOrder), 0, 10000)
		opt.DisplayOrder = *in.DisplayOrder
	}
	return f.err()
}
