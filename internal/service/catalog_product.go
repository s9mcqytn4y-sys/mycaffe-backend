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
	// ImageURL is rejected: product images are set only by a verified media upload (B3).
	ImageURL json.RawMessage `json:"image_url"`
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
	if in.ImageURL != nil {
		f.add("image_url", "diatur lewat unggah media (/api/v1/media/uploads)")
	}
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
