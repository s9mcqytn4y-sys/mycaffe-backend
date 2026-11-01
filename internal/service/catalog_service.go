package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/domain"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/database"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/rbac"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/repository/postgres"
)

// Catalog management (iteration 3, catalog C1–C3; owner decisions K1, K3). Products and variants
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
}

type CatalogCategory struct {
	ID           uuid.UUID `json:"id"`
	Name         string    `json:"name"`
	DisplayOrder int       `json:"display_order"`
	IsActive     bool      `json:"is_active"`
}

type CatalogView struct {
	Store       CatalogStore      `json:"store"`
	Categories  []CatalogCategory `json:"categories"`
	Products    []CatalogProduct  `json:"products"`
	GeneratedAt time.Time         `json:"generated_at"`
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
		store, err := s.order.GetStoreForOrder(ctx, tx, storeID)
		if err != nil {
			return err
		}
		settings, err := postgres.NewSettingsRepository().GetStoreSettings(ctx, tx, storeID)
		if err != nil {
			return err
		}
		rows, err := s.repo.LoadCatalog(ctx, tx, storeID)
		if err != nil {
			return err
		}
		view = buildCatalogView(store, settings, rows, staff)
		view.GeneratedAt = s.nowFunc().UTC()
		return nil
	})
	return view, err
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

// ---------- Categories ----------

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

// ---------- Products & variants ----------

type VariantInput struct {
	Name         *string `json:"name"`
	SKU          *string `json:"sku"`
	Price        *int64  `json:"price"`
	IsDefault    *bool   `json:"is_default"`
	IsAvailable  *bool   `json:"is_available"`
	DisplayOrder *int    `json:"display_order"`
}

type ProductInput struct {
	CategoryID    *uuid.UUID     `json:"category_id"`
	ClearCategory bool           `json:"clear_category"`
	Name          *string        `json:"name"`
	Description   *string        `json:"description"`
	ProductType   *string        `json:"product_type"`
	PrepStation   *string        `json:"prep_station"`
	DisplayOrder  *int           `json:"display_order"`
	IsAvailable   *bool          `json:"is_available"`
	TrackStock    *bool          `json:"track_stock"`
	StockQuantity *int           `json:"stock_quantity"`
	Variants      []VariantInput `json:"variants"` // create only
}

type ProductWithVariants struct {
	domain.Product
	Variants []domain.ProductVariant `json:"variants"`
}

func (s *CatalogService) CreateProduct(ctx context.Context, actor *AuthenticatedStaff, in ProductInput) (*ProductWithVariants, error) {
	if !actor.Can(rbac.CatalogManage) {
		return nil, ErrForbidden
	}
	storeID := actor.Staff.Store.ID
	p := &domain.Product{
		ID: uuid.New(), StoreID: storeID, IsAvailable: true,
		ProductType: domain.ProductTypePrepared, PrepStation: domain.PrepStationBar,
	}
	f := fieldErrors{}
	if in.Name == nil {
		f.add("name", "wajib diisi")
	}
	if len(in.Variants) == 0 {
		f.add("variants", "minimal satu varian (misalnya Reguler dengan harganya)")
	}
	if len(in.Variants) > 20 {
		f.add("variants", "maksimal 20 varian")
	}
	applyProduct(p, in, f)
	variants := make([]domain.ProductVariant, 0, len(in.Variants))
	defaults := 0
	for i, vin := range in.Variants {
		v := domain.ProductVariant{ID: uuid.New(), StoreID: storeID, ProductID: p.ID, IsAvailable: true, DisplayOrder: i}
		prefix := fmt.Sprintf("variants[%d].", i)
		if vin.Name == nil {
			f.add(prefix+"name", "wajib diisi")
		}
		if vin.Price == nil {
			f.add(prefix+"price", "wajib diisi")
		}
		applyVariant(&v, vin, f, prefix)
		if v.IsDefault {
			defaults++
		}
		variants = append(variants, v)
	}
	if len(variants) == 1 {
		variants[0].IsDefault, defaults = true, 1
	}
	if len(variants) > 1 && defaults != 1 {
		f.add("variants", "tepat satu varian harus menjadi default")
	}
	if err := f.err(); err != nil {
		return nil, err
	}
	for _, v := range variants {
		if v.IsDefault {
			p.BasePrice = v.Price
		}
	}
	err := s.tx(ctx, actor, func(tx *sqlx.Tx) error {
		if err := s.checkCategory(ctx, tx, storeID, p.CategoryID); err != nil {
			return err
		}
		if err := s.repo.InsertProduct(ctx, tx, p); err != nil {
			return err
		}
		for i := range variants {
			if err := s.repo.InsertVariant(ctx, tx, &variants[i]); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, mapConflict(err)
	}
	return &ProductWithVariants{Product: *p, Variants: variants}, nil
}

func (s *CatalogService) UpdateProduct(ctx context.Context, actor *AuthenticatedStaff, id uuid.UUID, in ProductInput) (*domain.Product, error) {
	if !actor.Can(rbac.CatalogManage) {
		return nil, ErrForbidden
	}
	if len(in.Variants) > 0 {
		return nil, &ValidationError{Fields: map[string]string{"variants": "ubah varian lewat endpoint varian"}}
	}
	var p *domain.Product
	err := s.tx(ctx, actor, func(tx *sqlx.Tx) error {
		var err error
		if p, err = s.repo.GetProductForUpdate(ctx, tx, actor.Staff.Store.ID, id); err != nil {
			return err
		}
		f := fieldErrors{}
		applyProduct(p, in, f)
		if err := f.err(); err != nil {
			return err
		}
		if err := s.checkCategory(ctx, tx, actor.Staff.Store.ID, p.CategoryID); err != nil {
			return err
		}
		return s.repo.UpdateProduct(ctx, tx, p)
	})
	return p, mapConflict(err)
}

func (s *CatalogService) ArchiveProduct(ctx context.Context, actor *AuthenticatedStaff, id uuid.UUID) error {
	if !actor.Can(rbac.CatalogManage) {
		return ErrForbidden
	}
	return mapConflict(s.tx(ctx, actor, func(tx *sqlx.Tx) error {
		return s.repo.ArchiveProduct(ctx, tx, actor.Staff.Store.ID, id, s.nowFunc().UTC())
	}))
}

// SetAvailability toggles "habis" for a product. Bar items need the beverage toggle permission,
// kitchen items the food toggle permission (ADR-003 D7); the owner holds both.
func (s *CatalogService) SetAvailability(ctx context.Context, actor *AuthenticatedStaff, id uuid.UUID, available bool) error {
	if actor == nil {
		return ErrInvalidSession
	}
	return mapConflict(s.tx(ctx, actor, func(tx *sqlx.Tx) error {
		p, err := s.repo.GetProductForUpdate(ctx, tx, actor.Staff.Store.ID, id)
		if err != nil {
			return err
		}
		needed := rbac.CatalogManage
		switch p.PrepStation {
		case domain.PrepStationBar:
			needed = rbac.AvailabilityToggleBeverage
		case domain.PrepStationKitchen:
			needed = rbac.AvailabilityToggleFood
		}
		if !actor.Can(needed) && !actor.Can(rbac.CatalogManage) {
			return ErrForbidden
		}
		return s.repo.SetProductAvailability(ctx, tx, actor.Staff.Store.ID, id, available)
	}))
}

func (s *CatalogService) CreateVariant(ctx context.Context, actor *AuthenticatedStaff, productID uuid.UUID, in VariantInput) (*domain.ProductVariant, error) {
	if !actor.Can(rbac.CatalogManage) {
		return nil, ErrForbidden
	}
	storeID := actor.Staff.Store.ID
	v := &domain.ProductVariant{ID: uuid.New(), StoreID: storeID, ProductID: productID, IsAvailable: true}
	f := fieldErrors{}
	if in.Name == nil {
		f.add("name", "wajib diisi")
	}
	if in.Price == nil {
		f.add("price", "wajib diisi")
	}
	applyVariant(v, in, f, "")
	if err := f.err(); err != nil {
		return nil, err
	}
	err := s.tx(ctx, actor, func(tx *sqlx.Tx) error {
		if _, err := s.repo.GetProductForUpdate(ctx, tx, storeID, productID); err != nil {
			return err
		}
		if v.IsDefault {
			if err := s.repo.ClearDefaultVariant(ctx, tx, storeID, productID, v.ID); err != nil {
				return err
			}
		}
		if err := s.repo.InsertVariant(ctx, tx, v); err != nil {
			return err
		}
		if v.IsDefault {
			return s.repo.SetBasePrice(ctx, tx, storeID, productID, v.Price)
		}
		return nil
	})
	return v, mapConflict(err)
}

func (s *CatalogService) UpdateVariant(ctx context.Context, actor *AuthenticatedStaff, id uuid.UUID, in VariantInput) (*domain.ProductVariant, error) {
	if !actor.Can(rbac.CatalogManage) {
		return nil, ErrForbidden
	}
	storeID := actor.Staff.Store.ID
	var v *domain.ProductVariant
	err := s.tx(ctx, actor, func(tx *sqlx.Tx) error {
		var err error
		if v, err = s.repo.GetVariantForUpdate(ctx, tx, storeID, id); err != nil {
			return err
		}
		before := *v
		if in.IsDefault != nil && !*in.IsDefault && before.IsDefault {
			return &ValidationError{Fields: map[string]string{"is_default": "jadikan varian lain sebagai default"}}
		}
		f := fieldErrors{}
		applyVariant(v, in, f, "")
		if err := f.err(); err != nil {
			return err
		}
		if v.IsDefault && !before.IsDefault {
			if err := s.repo.ClearDefaultVariant(ctx, tx, storeID, v.ProductID, v.ID); err != nil {
				return err
			}
		}
		if err := s.repo.UpdateVariant(ctx, tx, v); err != nil {
			return err
		}
		if v.IsDefault {
			if err := s.repo.SetBasePrice(ctx, tx, storeID, v.ProductID, v.Price); err != nil {
				return err
			}
		}
		if v.Price != before.Price {
			return s.auditPriceChange(ctx, tx, actor, v, before.Price)
		}
		return nil
	})
	return v, mapConflict(err)
}

func (s *CatalogService) ArchiveVariant(ctx context.Context, actor *AuthenticatedStaff, id uuid.UUID) error {
	if !actor.Can(rbac.CatalogManage) {
		return ErrForbidden
	}
	storeID := actor.Staff.Store.ID
	return mapConflict(s.tx(ctx, actor, func(tx *sqlx.Tx) error {
		v, err := s.repo.GetVariantForUpdate(ctx, tx, storeID, id)
		if err != nil {
			return err
		}
		if v.IsDefault {
			return &ConflictError{Field: "is_default", Message: "varian default tidak bisa diarsipkan; jadikan varian lain sebagai default dulu"}
		}
		return s.repo.ArchiveVariant(ctx, tx, storeID, id, s.nowFunc().UTC())
	}))
}

func applyProduct(p *domain.Product, in ProductInput, f fieldErrors) {
	if in.Name != nil {
		p.Name = f.text("name", *in.Name, 1, 120)
	}
	if in.Description != nil {
		p.Description = f.text("description", *in.Description, 0, 1000)
	}
	if in.ProductType != nil {
		f.oneOf("product_type", *in.ProductType, domain.ProductTypePrepared, domain.ProductTypeReadyToServe)
		p.ProductType = *in.ProductType
	}
	if in.PrepStation != nil {
		f.oneOf("prep_station", *in.PrepStation, domain.PrepStationBar, domain.PrepStationKitchen, domain.PrepStationNone)
		p.PrepStation = *in.PrepStation
	}
	if in.DisplayOrder != nil {
		f.intRange("display_order", int64(*in.DisplayOrder), 0, 10000)
		p.DisplayOrder = *in.DisplayOrder
	}
	if in.IsAvailable != nil {
		p.IsAvailable = *in.IsAvailable
	}
	if in.TrackStock != nil {
		p.TrackStock = *in.TrackStock
	}
	if in.StockQuantity != nil {
		f.intRange("stock_quantity", int64(*in.StockQuantity), 0, 1_000_000)
		p.StockQuantity = *in.StockQuantity
	}
	if in.ClearCategory {
		p.CategoryID = nil
	} else if in.CategoryID != nil {
		p.CategoryID = in.CategoryID
	}
}

func applyVariant(v *domain.ProductVariant, in VariantInput, f fieldErrors, prefix string) {
	if in.Name != nil {
		v.Name = f.text(prefix+"name", *in.Name, 1, 60)
	}
	if in.SKU != nil {
		v.SKU = strings.ToUpper(strings.TrimSpace(*in.SKU))
		if v.SKU != "" && !skuPattern.MatchString(v.SKU) {
			f.add(prefix+"sku", "hanya huruf, angka, titik, garis bawah, atau tanda hubung (maksimal 64)")
		}
	}
	if in.Price != nil {
		f.intRange(prefix+"price", *in.Price, 0, maxItemPrice)
		v.Price = *in.Price
	}
	if in.IsDefault != nil {
		v.IsDefault = *in.IsDefault
	}
	if in.IsAvailable != nil {
		v.IsAvailable = *in.IsAvailable
	}
	if in.DisplayOrder != nil {
		f.intRange(prefix+"display_order", int64(*in.DisplayOrder), 0, 10000)
		v.DisplayOrder = *in.DisplayOrder
	}
}

func (s *CatalogService) checkCategory(ctx context.Context, tx *sqlx.Tx, storeID uuid.UUID, id *uuid.UUID) error {
	if id == nil {
		return nil
	}
	ok, err := s.repo.CategoryExists(ctx, tx, storeID, *id)
	if err != nil {
		return err
	}
	if !ok {
		return &ValidationError{Fields: map[string]string{"category_id": "kategori tidak ditemukan"}}
	}
	return nil
}

func (s *CatalogService) auditPriceChange(ctx context.Context, tx *sqlx.Tx, actor *AuthenticatedStaff, v *domain.ProductVariant, oldPrice int64) error {
	meta, _ := json.Marshal(map[string]any{"variant_id": v.ID, "product_id": v.ProductID, "old_price": oldPrice, "new_price": v.Price, "sku": v.SKU})
	actorID := actor.Staff.UserID
	return postgres.InsertAuditLog(ctx, tx, &domain.AuditLog{
		StoreID: actor.Staff.Store.ID, ActorID: &actorID, ActorName: actor.Staff.FullName, Action: "PRICE_CHANGE",
		Reason:      fmt.Sprintf("Harga varian %q diubah dari Rp%d ke Rp%d", v.Name, oldPrice, v.Price),
		ReferenceID: v.ID.String(), Metadata: string(meta),
	})
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
