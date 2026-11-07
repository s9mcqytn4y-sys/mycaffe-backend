package service

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/domain"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/database"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/repository/postgres"
)

// Catalog management (iteration 3 & 4a; catalog C1–C3; owner decisions K1, K3, P1). Products and variants
// are archived rather than deleted so order snapshots and reports keep their references.

const maxItemPrice = 100_000_000 // Rp100 juta per item: rejects typos such as an extra 000

var skuPattern = regexp.MustCompile(`^[A-Z0-9._-]{1,64}$`)

var ErrCatalogNotFound = errors.New("catalog item not found")

type CatalogService struct {
	db      *sqlx.DB
	repo    *postgres.CatalogRepository
	authRep *postgres.AuthRepository
	order   *postgres.OrderRepository
	nowFunc func() time.Time
}

func NewCatalogService(db *sqlx.DB) *CatalogService {
	return &CatalogService{
		db: db, repo: postgres.NewCatalogRepository(), authRep: postgres.NewAuthRepository(),
		order: postgres.NewOrderRepository(), nowFunc: time.Now,
	}
}

// ---------- Read model (catalog C1) ----------

type CatalogStore struct {
	ID               uuid.UUID `json:"id"`
	Slug             string    `json:"slug"`
	Name             string    `json:"name"`
	Currency         string    `json:"currency"`
	TaxBPS           int64     `json:"tax_bps"`
	ServiceBPS       int64     `json:"service_bps"`
	QROrderingPaused bool      `json:"qr_ordering_paused"`
}

type CatalogOption struct {
	ID         uuid.UUID      `json:"id"`
	Name       string         `json:"name"`
	PriceDelta int64          `json:"price_delta"`
	Available  bool           `json:"available"`
	MaxQty     int            `json:"max_quantity"`
	SubGroups  []CatalogGroup `json:"sub_groups"`
	Stock      *int           `json:"stock_quantity,omitempty"`
}

type CatalogGroup struct {
	ID        uuid.UUID       `json:"id"`
	Name      string          `json:"name"`
	Selection string          `json:"selection"`
	Required  bool            `json:"required"`
	Min       int             `json:"min"`
	Max       int             `json:"max"`
	Options   []CatalogOption `json:"options"`
}

type CatalogVariant struct {
	ID        uuid.UUID `json:"id"`
	Name      string    `json:"name"`
	SKU       string    `json:"sku,omitempty"`
	Price     int64     `json:"price"`
	IsDefault bool      `json:"is_default"`
	Available bool      `json:"available"`
}

type CatalogProduct struct {
	ID             uuid.UUID        `json:"id"`
	CategoryID     *uuid.UUID       `json:"category_id"`
	Name           string           `json:"name"`
	Description    string           `json:"description"`
	Type           string           `json:"type"`
	PrepStation    string           `json:"prep_station"`
	ImageURL       *string          `json:"image_url"`
	Available      bool             `json:"available"`
	SoldOutReason  *string          `json:"sold_out_reason"`
	TrackStock     bool             `json:"track_stock"`
	StockQuantity  *int             `json:"stock_quantity,omitempty"`
	Variants       []CatalogVariant `json:"variants"`
	ModifierGroups []CatalogGroup   `json:"modifier_groups"`
	// BundleSlots are the parts of a bundle product to choose from (X4).
	BundleSlots []domain.BundleSlot `json:"bundle_slots,omitempty"`
}

type CatalogCategory struct {
	ID           uuid.UUID `json:"id"`
	Name         string    `json:"name"`
	DisplayOrder int       `json:"display_order"`
	IsActive     bool      `json:"is_active"`
}

// CatalogPromotion is an active promotion as menus and the offline snapshot show it: no usage counter, so
// serving it twice yields the same snapshot version (W20). The server decides which promotion applies.
type CatalogPromotion struct {
	ID          uuid.UUID       `json:"id" db:"id"`
	Name        string          `json:"name" db:"name"`
	Kind        string          `json:"kind" db:"kind"`
	Value       int64           `json:"value" db:"value"`
	ProductIDs  domain.UUIDList `json:"product_ids" db:"product_ids"`
	CategoryIDs domain.UUIDList `json:"category_ids" db:"category_ids"`
	StartsAt    *time.Time      `json:"starts_at,omitempty" db:"starts_at"`
	EndsAt      *time.Time      `json:"ends_at,omitempty" db:"ends_at"`
	DaysMask    int             `json:"days_mask" db:"days_mask"`
	StartMinute *int            `json:"start_minute,omitempty" db:"start_minute"`
	EndMinute   *int            `json:"end_minute,omitempty" db:"end_minute"`
	Quota       *int            `json:"quota,omitempty" db:"quota"`
}

func (p CatalogPromotion) promotion() domain.Promotion {
	return domain.Promotion{ID: p.ID, Name: p.Name, Kind: p.Kind, Value: p.Value, ProductIDs: p.ProductIDs, CategoryIDs: p.CategoryIDs,
		StartsAt: p.StartsAt, EndsAt: p.EndsAt, DaysMask: p.DaysMask, StartMinute: p.StartMinute, EndMinute: p.EndMinute, Quota: p.Quota, IsActive: true}
}

type CatalogView struct {
	Store       CatalogStore       `json:"store"`
	Categories  []CatalogCategory  `json:"categories"`
	Products    []CatalogProduct   `json:"products"`
	Promotions  []CatalogPromotion `json:"promotions"`
	GeneratedAt time.Time          `json:"generated_at"`
}

// StaffCatalog is the POS view: inactive categories and stock counts included.
func (s *CatalogService) StaffCatalog(ctx context.Context, actor *AuthenticatedStaff) (*CatalogView, error) {
	if actor == nil {
		return nil, ErrInvalidSession
	}
	return s.catalog(ctx, actor.Staff.Store.ID, true)
}

// PublicCatalog is the guest menu for a store slug: inactive categories and stock counts hidden,
// unavailable items kept but flagged sold out (guests see "habis" instead of a missing item).
func (s *CatalogService) PublicCatalog(ctx context.Context, slug string) (*CatalogView, error) {
	storeID, active, err := s.authRep.ResolveStoreForLogin(ctx, s.db, strings.ToLower(strings.TrimSpace(slug)))
	if errors.Is(err, postgres.ErrStoreNotFound) || (err == nil && !active) {
		return nil, ErrCatalogNotFound
	}
	if err != nil {
		return nil, err
	}
	return s.catalog(ctx, storeID, false)
}

func (s *CatalogService) catalog(ctx context.Context, storeID uuid.UUID, staff bool) (*CatalogView, error) {
	var view *CatalogView
	err := database.WithTenantTx(ctx, s.db, storeID, func(tx *sqlx.Tx) error {
		settings, err := postgres.NewSettingsRepository().GetStoreSettings(ctx, tx, storeID)
		if err != nil {
			return err
		}
		if view, err = s.catalogTx(ctx, tx, storeID, settings, staff); err != nil {
			return err
		}
		view.GeneratedAt = s.nowFunc().UTC()
		return nil
	})
	return view, err
}

// catalogTx builds the catalog view inside an open tenant transaction.
func (s *CatalogService) catalogTx(ctx context.Context, tx *sqlx.Tx, storeID uuid.UUID, settings domain.StoreSettings, staff bool) (*CatalogView, error) {
	store, err := s.order.GetStoreForOrder(ctx, tx, storeID)
	if err != nil {
		return nil, err
	}
	rows, err := s.repo.LoadCatalog(ctx, tx, storeID)
	if err != nil {
		return nil, err
	}
	view := buildCatalogView(store, settings, rows, staff)
	var slots []domain.BundleSlot
	if err := tx.SelectContext(ctx, &slots, `SELECT id, store_id, bundle_product_id, name, quantity, display_order, product_ids
		FROM bundle_slots WHERE store_id = $1 ORDER BY bundle_product_id, display_order, id`, storeID); err != nil {
		return nil, fmt.Errorf("failed to load bundle slots: %w", err)
	}
	byBundle := map[uuid.UUID][]domain.BundleSlot{}
	for _, sl := range slots {
		byBundle[sl.BundleProductID] = append(byBundle[sl.BundleProductID], sl)
	}
	for i := range view.Products {
		view.Products[i].BundleSlots = byBundle[view.Products[i].ID]
	}
	view.Promotions = []CatalogPromotion{}
	if err := tx.SelectContext(ctx, &view.Promotions, `SELECT id, name, kind, value, product_ids, category_ids, starts_at, ends_at,
		days_mask, start_minute, end_minute, quota FROM promotions WHERE store_id = $1 AND is_active ORDER BY id`, storeID); err != nil {
		return nil, fmt.Errorf("failed to load promotions: %w", err)
	}
	return view, nil
}

func buildCatalogView(store *domain.Store, settings domain.StoreSettings, rows *postgres.CatalogRows, staff bool) *CatalogView {
	view := &CatalogView{
		Store: CatalogStore{
			ID: store.ID, Slug: store.Slug, Name: store.Name, Currency: store.Currency,
			TaxBPS: store.TaxBps, ServiceBPS: store.ServiceBps, QROrderingPaused: settings.QROrderingPaused,
		},
		Categories: []CatalogCategory{},
		Products:   []CatalogProduct{},
	}
	activeCategory := map[uuid.UUID]bool{}
	for _, c := range rows.Categories {
		activeCategory[c.ID] = c.IsActive
		if staff || c.IsActive {
			view.Categories = append(view.Categories, CatalogCategory{ID: c.ID, Name: c.Name, DisplayOrder: c.DisplayOrder, IsActive: c.IsActive})
		}
	}

	options := map[uuid.UUID][]domain.ModifierOption{}
	for _, o := range rows.ModifierOptions {
		options[o.GroupID] = append(options[o.GroupID], o)
	}
	groupsByID := map[uuid.UUID]domain.ModifierGroup{}
	for _, g := range rows.ModifierGroups {
		groupsByID[g.ID] = g
	}
	childGroups := map[uuid.UUID][]uuid.UUID{}
	for _, og := range rows.OptionGroups {
		childGroups[og.ParentOptionID] = append(childGroups[og.ParentOptionID], og.ChildGroupID)
	}
	var buildGroup func(g domain.ModifierGroup, required bool, depth int) CatalogGroup
	buildGroup = func(g domain.ModifierGroup, required bool, depth int) CatalogGroup {
		cg := CatalogGroup{ID: g.ID, Name: g.Name, Selection: g.SelectionType, Required: required, Min: g.MinSelect, Max: g.MaxSelect, Options: []CatalogOption{}}
		for _, o := range options[g.ID] {
			co := CatalogOption{ID: o.ID, Name: o.Name, PriceDelta: o.PriceDelta, Available: o.IsAvailable && (!o.TrackStock || o.StockQuantity > 0), MaxQty: o.MaxQuantity, SubGroups: []CatalogGroup{}}
			if staff && o.TrackStock {
				q := o.StockQuantity
				co.Stock = &q
			}
			if depth == 0 { // sub-addons are one level deep (ADR / pricing engine rule)
				for _, childID := range childGroups[o.ID] {
					if child, ok := groupsByID[childID]; ok {
						co.SubGroups = append(co.SubGroups, buildGroup(child, child.IsRequired, 1))
					}
				}
			}
			cg.Options = append(cg.Options, co)
		}
		return cg
	}
	productGroups := map[uuid.UUID][]domain.ProductModifierGroup{}
	for _, pg := range rows.ProductGroups {
		productGroups[pg.ProductID] = append(productGroups[pg.ProductID], pg)
	}
	variants := map[uuid.UUID][]domain.ProductVariant{}
	for _, v := range rows.Variants {
		variants[v.ProductID] = append(variants[v.ProductID], v)
	}

	for _, p := range rows.Products {
		if !staff && p.CategoryID != nil && !activeCategory[*p.CategoryID] {
			continue
		}
		cp := CatalogProduct{
			ID: p.ID, CategoryID: p.CategoryID, Name: p.Name, Description: p.Description, Type: p.ProductType,
			PrepStation: p.PrepStation, ImageURL: p.ImageURL, TrackStock: p.TrackStock,
			Variants: []CatalogVariant{}, ModifierGroups: []CatalogGroup{},
		}
		anyVariantAvailable := false
		for _, v := range variants[p.ID] {
			cp.Variants = append(cp.Variants, CatalogVariant{ID: v.ID, Name: v.Name, SKU: v.SKU, Price: v.Price, IsDefault: v.IsDefault, Available: v.IsAvailable})
			anyVariantAvailable = anyVariantAvailable || v.IsAvailable
		}
		cp.Available = p.IsAvailable && anyVariantAvailable && (!p.TrackStock || p.StockQuantity > 0)
		if !cp.Available {
			reason := "habis"
			if !p.IsAvailable {
				reason = "tidak tersedia"
			}
			cp.SoldOutReason = &reason
		}
		if staff && p.TrackStock {
			q := p.StockQuantity
			cp.StockQuantity = &q
		}
		for _, link := range productGroups[p.ID] {
			g, ok := groupsByID[link.GroupID]
			if !ok {
				continue
			}
			required := g.IsRequired
			if link.OverrideRequired != nil {
				required = *link.OverrideRequired
			}
			cp.ModifierGroups = append(cp.ModifierGroups, buildGroup(g, required, 0))
		}
		view.Products = append(view.Products, cp)
	}
	return view
}

func (s *CatalogService) tx(ctx context.Context, actor *AuthenticatedStaff, fn func(tx *sqlx.Tx) error) error {
	return database.WithTenantTx(ctx, s.db, actor.Staff.Store.ID, fn)
}

// mapConflict turns not-found and unique violations into the service's typed errors.
func mapConflict(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, postgres.ErrCategoryNotFound) || errors.Is(err, postgres.ErrProductNotFound) ||
		errors.Is(err, postgres.ErrVariantNotFound) || errors.Is(err, postgres.ErrModifierGroupNotFound) ||
		errors.Is(err, postgres.ErrModifierOptionNotFound) {
		return ErrCatalogNotFound
	}
	if constraint, ok := postgres.UniqueViolation(err); ok {
		switch constraint {
		case "uq_products_store_name_active":
			return &ConflictError{Field: "name", Message: "nama menu sudah dipakai"}
		case "uq_product_variants_sku":
			return &ConflictError{Field: "sku", Message: "SKU sudah dipakai menu lain"}
		case "uq_product_variants_name":
			return &ConflictError{Field: "name", Message: "nama varian sudah ada di menu ini"}
		case "uq_modifier_groups_store_name_active":
			return &ConflictError{Field: "name", Message: "nama grup modifier sudah dipakai"}
		case "uq_modifier_options_group_name_active":
			return &ConflictError{Field: "name", Message: "nama opsi modifier sudah ada di grup ini"}
		}
		return &ConflictError{Field: "", Message: "data duplikat"}
	}
	return err
}
