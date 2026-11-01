package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jmoiron/sqlx"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/domain"
)

var (
	ErrCategoryNotFound       = errors.New("category not found in tenant context")
	ErrProductNotFound        = errors.New("product not found in tenant context")
	ErrVariantNotFound        = errors.New("variant not found in tenant context")
	ErrModifierGroupNotFound  = errors.New("modifier group not found in tenant context")
	ErrModifierOptionNotFound = errors.New("modifier option not found in tenant context")
)

// UniqueViolation reports the violated constraint/index name when err is a PostgreSQL unique
// violation (SQLSTATE 23505), so services can map it to a field-level 409.
func UniqueViolation(err error) (string, bool) {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return pgErr.ConstraintName, true
	}
	return "", false
}

type CatalogRepository struct{}

func NewCatalogRepository() *CatalogRepository {
	return &CatalogRepository{}
}

// CatalogRows is the full, non-archived catalog of a store, read inside one tenant transaction.
type CatalogRows struct {
	Categories      []domain.Category
	Products        []domain.Product
	Variants        []domain.ProductVariant
	ModifierGroups  []domain.ModifierGroup
	ModifierOptions []domain.ModifierOption
	ProductGroups   []domain.ProductModifierGroup
	OptionGroups    []domain.OptionModifierGroup
}

func (r *CatalogRepository) LoadCatalog(ctx context.Context, tx *sqlx.Tx, storeID uuid.UUID) (*CatalogRows, error) {
	var out CatalogRows
	queries := []struct {
		dest  any
		query string
	}{
		{&out.Categories, `SELECT id, store_id, name, display_order, is_active, created_at, updated_at
			FROM categories WHERE store_id = $1 ORDER BY display_order, name`},
		{&out.Products, `SELECT id, store_id, category_id, name, COALESCE(description, '') AS description, base_price,
			image_url, is_available, track_stock, stock_quantity, product_type, prep_station, display_order,
			archived_at, created_at, updated_at
			FROM products WHERE store_id = $1 AND archived_at IS NULL ORDER BY display_order, name`},
		{&out.Variants, `SELECT id, store_id, product_id, name, sku, price, is_default, is_available, display_order,
			archived_at, created_at, updated_at
			FROM product_variants WHERE store_id = $1 AND archived_at IS NULL ORDER BY product_id, display_order, name`},
		{&out.ModifierGroups, `SELECT id, store_id, name, selection_type, is_required, min_select, max_select,
			display_order, is_active, created_at, updated_at
			FROM modifier_groups WHERE store_id = $1 AND is_active AND archived_at IS NULL ORDER BY display_order, name`},
		{&out.ModifierOptions, `SELECT id, store_id, group_id, name, price_delta, is_available, track_stock,
			stock_quantity, max_quantity, display_order, created_at, updated_at
			FROM modifier_options WHERE store_id = $1 AND archived_at IS NULL ORDER BY group_id, display_order, name`},
		{&out.ProductGroups, `SELECT store_id, product_id, group_id, display_order, override_required, created_at
			FROM product_modifier_groups WHERE store_id = $1 ORDER BY product_id, display_order`},
		{&out.OptionGroups, `SELECT store_id, parent_option_id, child_group_id, display_order, created_at
			FROM option_modifier_groups WHERE store_id = $1 ORDER BY parent_option_id, display_order`},
	}
	for _, q := range queries {
		if err := tx.SelectContext(ctx, q.dest, q.query, storeID); err != nil {
			return nil, fmt.Errorf("failed to load catalog: %w", err)
		}
	}
	return &out, nil
}

func (r *CatalogRepository) InsertCategory(ctx context.Context, tx *sqlx.Tx, c *domain.Category) error {
	return tx.QueryRowxContext(ctx, `
		INSERT INTO categories (id, store_id, name, display_order, is_active) VALUES ($1, $2, $3, $4, $5)
		RETURNING created_at, updated_at
	`, c.ID, c.StoreID, c.Name, c.DisplayOrder, c.IsActive).Scan(&c.CreatedAt, &c.UpdatedAt)
}

func (r *CatalogRepository) GetCategoryForUpdate(ctx context.Context, tx *sqlx.Tx, storeID, id uuid.UUID) (*domain.Category, error) {
	var c domain.Category
	err := tx.GetContext(ctx, &c, `SELECT id, store_id, name, display_order, is_active, created_at, updated_at
		FROM categories WHERE store_id = $1 AND id = $2 FOR UPDATE`, storeID, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrCategoryNotFound
	}
	return &c, err
}

func (r *CatalogRepository) UpdateCategory(ctx context.Context, tx *sqlx.Tx, c *domain.Category) error {
	return tx.QueryRowxContext(ctx, `
		UPDATE categories SET name = $3, display_order = $4, is_active = $5 WHERE store_id = $1 AND id = $2
		RETURNING updated_at
	`, c.StoreID, c.ID, c.Name, c.DisplayOrder, c.IsActive).Scan(&c.UpdatedAt)
}

// DeleteCategory removes a category; products keep existing with category_id set to NULL (FK).
func (r *CatalogRepository) DeleteCategory(ctx context.Context, tx *sqlx.Tx, storeID, id uuid.UUID) error {
	res, err := tx.ExecContext(ctx, `DELETE FROM categories WHERE store_id = $1 AND id = $2`, storeID, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrCategoryNotFound
	}
	return nil
}

func (r *CatalogRepository) CategoryExists(ctx context.Context, tx *sqlx.Tx, storeID, id uuid.UUID) (bool, error) {
	var ok bool
	err := tx.GetContext(ctx, &ok, `SELECT EXISTS (SELECT 1 FROM categories WHERE store_id = $1 AND id = $2)`, storeID, id)
	return ok, err
}

func (r *CatalogRepository) InsertProduct(ctx context.Context, tx *sqlx.Tx, p *domain.Product) error {
	return tx.QueryRowxContext(ctx, `
		INSERT INTO products (id, store_id, category_id, name, description, base_price, is_available, track_stock,
			stock_quantity, product_type, prep_station, display_order)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		RETURNING created_at, updated_at
	`, p.ID, p.StoreID, p.CategoryID, p.Name, p.Description, p.BasePrice, p.IsAvailable, p.TrackStock,
		p.StockQuantity, p.ProductType, p.PrepStation, p.DisplayOrder).Scan(&p.CreatedAt, &p.UpdatedAt)
}

func (r *CatalogRepository) GetProductForUpdate(ctx context.Context, tx *sqlx.Tx, storeID, id uuid.UUID) (*domain.Product, error) {
	var p domain.Product
	err := tx.GetContext(ctx, &p, `SELECT id, store_id, category_id, name, COALESCE(description, '') AS description,
		base_price, image_url, is_available, track_stock, stock_quantity, product_type, prep_station, display_order,
		archived_at, created_at, updated_at
		FROM products WHERE store_id = $1 AND id = $2 AND archived_at IS NULL FOR UPDATE`, storeID, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrProductNotFound
	}
	return &p, err
}

func (r *CatalogRepository) UpdateProduct(ctx context.Context, tx *sqlx.Tx, p *domain.Product) error {
	return tx.QueryRowxContext(ctx, `
		UPDATE products SET category_id = $3, name = $4, description = $5, base_price = $6, is_available = $7,
			track_stock = $8, stock_quantity = $9, product_type = $10, prep_station = $11, display_order = $12
		WHERE store_id = $1 AND id = $2 AND archived_at IS NULL
		RETURNING updated_at
	`, p.StoreID, p.ID, p.CategoryID, p.Name, p.Description, p.BasePrice, p.IsAvailable, p.TrackStock,
		p.StockQuantity, p.ProductType, p.PrepStation, p.DisplayOrder).Scan(&p.UpdatedAt)
}

// ArchiveProduct hides a product and its variants; order snapshots keep referencing them.
func (r *CatalogRepository) ArchiveProduct(ctx context.Context, tx *sqlx.Tx, storeID, id uuid.UUID, now time.Time) error {
	if _, err := tx.ExecContext(ctx, `UPDATE product_variants SET archived_at = $3
		WHERE store_id = $1 AND product_id = $2 AND archived_at IS NULL`, storeID, id, now); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `UPDATE products SET archived_at = $3, is_available = false
		WHERE store_id = $1 AND id = $2 AND archived_at IS NULL`, storeID, id, now)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrProductNotFound
	}
	return nil
}

func (r *CatalogRepository) InsertVariant(ctx context.Context, tx *sqlx.Tx, v *domain.ProductVariant) error {
	return tx.QueryRowxContext(ctx, `
		INSERT INTO product_variants (id, store_id, product_id, name, sku, price, is_default, is_available, display_order)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING created_at, updated_at
	`, v.ID, v.StoreID, v.ProductID, v.Name, v.SKU, v.Price, v.IsDefault, v.IsAvailable, v.DisplayOrder).Scan(&v.CreatedAt, &v.UpdatedAt)
}

func (r *CatalogRepository) GetVariantForUpdate(ctx context.Context, tx *sqlx.Tx, storeID, id uuid.UUID) (*domain.ProductVariant, error) {
	var v domain.ProductVariant
	err := tx.GetContext(ctx, &v, `SELECT id, store_id, product_id, name, sku, price, is_default, is_available,
		display_order, archived_at, created_at, updated_at
		FROM product_variants WHERE store_id = $1 AND id = $2 AND archived_at IS NULL FOR UPDATE`, storeID, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrVariantNotFound
	}
	return &v, err
}

func (r *CatalogRepository) ListActiveVariants(ctx context.Context, tx *sqlx.Tx, storeID, productID uuid.UUID) ([]domain.ProductVariant, error) {
	var vs []domain.ProductVariant
	err := tx.SelectContext(ctx, &vs, `SELECT id, store_id, product_id, name, sku, price, is_default, is_available,
		display_order, archived_at, created_at, updated_at
		FROM product_variants WHERE store_id = $1 AND product_id = $2 AND archived_at IS NULL
		ORDER BY display_order, name`, storeID, productID)
	return vs, err
}

func (r *CatalogRepository) UpdateVariant(ctx context.Context, tx *sqlx.Tx, v *domain.ProductVariant) error {
	return tx.QueryRowxContext(ctx, `
		UPDATE product_variants SET name = $3, sku = $4, price = $5, is_default = $6, is_available = $7, display_order = $8
		WHERE store_id = $1 AND id = $2 AND archived_at IS NULL
		RETURNING updated_at
	`, v.StoreID, v.ID, v.Name, v.SKU, v.Price, v.IsDefault, v.IsAvailable, v.DisplayOrder).Scan(&v.UpdatedAt)
}

// ClearDefaultVariant unsets the current default so another variant can take it (partial unique index).
func (r *CatalogRepository) ClearDefaultVariant(ctx context.Context, tx *sqlx.Tx, storeID, productID, except uuid.UUID) error {
	_, err := tx.ExecContext(ctx, `UPDATE product_variants SET is_default = false
		WHERE store_id = $1 AND product_id = $2 AND id <> $3 AND is_default AND archived_at IS NULL`, storeID, productID, except)
	return err
}

func (r *CatalogRepository) ArchiveVariant(ctx context.Context, tx *sqlx.Tx, storeID, id uuid.UUID, now time.Time) error {
	_, err := tx.ExecContext(ctx, `UPDATE product_variants SET archived_at = $3, is_default = false
		WHERE store_id = $1 AND id = $2 AND archived_at IS NULL`, storeID, id, now)
	return err
}

// SetBasePrice keeps products.base_price equal to the default variant's price for legacy readers.
func (r *CatalogRepository) SetBasePrice(ctx context.Context, tx *sqlx.Tx, storeID, productID uuid.UUID, price int64) error {
	_, err := tx.ExecContext(ctx, `UPDATE products SET base_price = $3 WHERE store_id = $1 AND id = $2`, storeID, productID, price)
	return err
}

func (r *CatalogRepository) SetProductAvailability(ctx context.Context, tx *sqlx.Tx, storeID, id uuid.UUID, available bool) error {
	res, err := tx.ExecContext(ctx, `UPDATE products SET is_available = $3 WHERE store_id = $1 AND id = $2 AND archived_at IS NULL`,
		storeID, id, available)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrProductNotFound
	}
	return nil
}

// ---------- Modifier groups & options (Iteration 4a, decision P1) ----------

func (r *CatalogRepository) InsertModifierGroup(ctx context.Context, tx *sqlx.Tx, g *domain.ModifierGroup) error {
	return tx.QueryRowxContext(ctx, `
		INSERT INTO modifier_groups (id, store_id, name, selection_type, is_required, min_select, max_select, display_order, is_active)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING created_at, updated_at
	`, g.ID, g.StoreID, g.Name, g.SelectionType, g.IsRequired, g.MinSelect, g.MaxSelect, g.DisplayOrder, g.IsActive).Scan(&g.CreatedAt, &g.UpdatedAt)
}

func (r *CatalogRepository) GetModifierGroupForUpdate(ctx context.Context, tx *sqlx.Tx, storeID, id uuid.UUID) (*domain.ModifierGroup, error) {
	var g domain.ModifierGroup
	err := tx.GetContext(ctx, &g, `SELECT id, store_id, name, selection_type, is_required, min_select, max_select,
		display_order, is_active, archived_at, created_at, updated_at
		FROM modifier_groups WHERE store_id = $1 AND id = $2 AND archived_at IS NULL FOR UPDATE`, storeID, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrModifierGroupNotFound
	}
	return &g, err
}

func (r *CatalogRepository) GetModifierGroup(ctx context.Context, tx *sqlx.Tx, storeID, id uuid.UUID) (*domain.ModifierGroup, error) {
	var g domain.ModifierGroup
	err := tx.GetContext(ctx, &g, `SELECT id, store_id, name, selection_type, is_required, min_select, max_select,
		display_order, is_active, archived_at, created_at, updated_at
		FROM modifier_groups WHERE store_id = $1 AND id = $2 AND archived_at IS NULL`, storeID, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrModifierGroupNotFound
	}
	return &g, err
}

func (r *CatalogRepository) ListModifierGroups(ctx context.Context, tx *sqlx.Tx, storeID uuid.UUID, includeArchived bool) ([]domain.ModifierGroup, error) {
	var gs []domain.ModifierGroup
	err := tx.SelectContext(ctx, &gs, `SELECT id, store_id, name, selection_type, is_required, min_select, max_select,
		display_order, is_active, archived_at, created_at, updated_at
		FROM modifier_groups WHERE store_id = $1 AND ($2 OR archived_at IS NULL)
		ORDER BY display_order, name`, storeID, includeArchived)
	return gs, err
}

func (r *CatalogRepository) UpdateModifierGroup(ctx context.Context, tx *sqlx.Tx, g *domain.ModifierGroup) error {
	return tx.QueryRowxContext(ctx, `
		UPDATE modifier_groups
		SET name = $3, selection_type = $4, is_required = $5, min_select = $6, max_select = $7, display_order = $8, is_active = $9
		WHERE store_id = $1 AND id = $2 AND archived_at IS NULL
		RETURNING updated_at
	`, g.StoreID, g.ID, g.Name, g.SelectionType, g.IsRequired, g.MinSelect, g.MaxSelect, g.DisplayOrder, g.IsActive).Scan(&g.UpdatedAt)
}

func (r *CatalogRepository) CountProductsLinkedToGroup(ctx context.Context, tx *sqlx.Tx, storeID, groupID uuid.UUID) (int, error) {
	var count int
	err := tx.GetContext(ctx, &count, `SELECT COUNT(*) FROM product_modifier_groups WHERE store_id = $1 AND group_id = $2`, storeID, groupID)
	return count, err
}

func (r *CatalogRepository) CountParentOptionsLinkedToGroup(ctx context.Context, tx *sqlx.Tx, storeID, groupID uuid.UUID) (int, error) {
	var count int
	err := tx.GetContext(ctx, &count, `SELECT COUNT(*) FROM option_modifier_groups WHERE store_id = $1 AND child_group_id = $2`, storeID, groupID)
	return count, err
}

func (r *CatalogRepository) ArchiveModifierGroup(ctx context.Context, tx *sqlx.Tx, storeID, id uuid.UUID, now time.Time, forceUnlink bool) error {
	if forceUnlink {
		if _, err := tx.ExecContext(ctx, `DELETE FROM product_modifier_groups WHERE store_id = $1 AND group_id = $2`, storeID, id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM option_modifier_groups WHERE store_id = $1 AND child_group_id = $2`, storeID, id); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE modifier_options SET archived_at = $3, is_available = false
		WHERE store_id = $1 AND group_id = $2 AND archived_at IS NULL`, storeID, id, now); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `UPDATE modifier_groups SET archived_at = $3, is_active = false
		WHERE store_id = $1 AND id = $2 AND archived_at IS NULL`, storeID, id, now)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrModifierGroupNotFound
	}
	return nil
}

func (r *CatalogRepository) AttachModifierGroupToProduct(ctx context.Context, tx *sqlx.Tx, link *domain.ProductModifierGroup) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO product_modifier_groups (store_id, product_id, group_id, display_order, override_required)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (store_id, product_id, group_id) DO UPDATE
		SET display_order = EXCLUDED.display_order, override_required = EXCLUDED.override_required
	`, link.StoreID, link.ProductID, link.GroupID, link.DisplayOrder, link.OverrideRequired)
	return err
}

func (r *CatalogRepository) DetachModifierGroupFromProduct(ctx context.Context, tx *sqlx.Tx, storeID, productID, groupID uuid.UUID) error {
	res, err := tx.ExecContext(ctx, `DELETE FROM product_modifier_groups WHERE store_id = $1 AND product_id = $2 AND group_id = $3`, storeID, productID, groupID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrModifierGroupNotFound
	}
	return nil
}

func (r *CatalogRepository) InsertModifierOption(ctx context.Context, tx *sqlx.Tx, opt *domain.ModifierOption) error {
	return tx.QueryRowxContext(ctx, `
		INSERT INTO modifier_options (id, store_id, group_id, name, price_delta, is_available, track_stock, stock_quantity, max_quantity, display_order)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		RETURNING created_at, updated_at
	`, opt.ID, opt.StoreID, opt.GroupID, opt.Name, opt.PriceDelta, opt.IsAvailable, opt.TrackStock, opt.StockQuantity, opt.MaxQuantity, opt.DisplayOrder).Scan(&opt.CreatedAt, &opt.UpdatedAt)
}

func (r *CatalogRepository) GetModifierOptionForUpdate(ctx context.Context, tx *sqlx.Tx, storeID, id uuid.UUID) (*domain.ModifierOption, error) {
	var opt domain.ModifierOption
	err := tx.GetContext(ctx, &opt, `SELECT id, store_id, group_id, name, price_delta, is_available, track_stock,
		stock_quantity, max_quantity, display_order, archived_at, created_at, updated_at
		FROM modifier_options WHERE store_id = $1 AND id = $2 AND archived_at IS NULL FOR UPDATE`, storeID, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrModifierOptionNotFound
	}
	return &opt, err
}

func (r *CatalogRepository) ListModifierOptions(ctx context.Context, tx *sqlx.Tx, storeID, groupID uuid.UUID, includeArchived bool) ([]domain.ModifierOption, error) {
	var opts []domain.ModifierOption
	err := tx.SelectContext(ctx, &opts, `SELECT id, store_id, group_id, name, price_delta, is_available, track_stock,
		stock_quantity, max_quantity, display_order, archived_at, created_at, updated_at
		FROM modifier_options WHERE store_id = $1 AND group_id = $2 AND ($3 OR archived_at IS NULL)
		ORDER BY display_order, name`, storeID, groupID, includeArchived)
	return opts, err
}

func (r *CatalogRepository) UpdateModifierOption(ctx context.Context, tx *sqlx.Tx, opt *domain.ModifierOption) error {
	return tx.QueryRowxContext(ctx, `
		UPDATE modifier_options
		SET name = $3, price_delta = $4, is_available = $5, track_stock = $6, stock_quantity = $7, max_quantity = $8, display_order = $9
		WHERE store_id = $1 AND id = $2 AND archived_at IS NULL
		RETURNING updated_at
	`, opt.StoreID, opt.ID, opt.Name, opt.PriceDelta, opt.IsAvailable, opt.TrackStock, opt.StockQuantity, opt.MaxQuantity, opt.DisplayOrder).Scan(&opt.UpdatedAt)
}

func (r *CatalogRepository) ArchiveModifierOption(ctx context.Context, tx *sqlx.Tx, storeID, id uuid.UUID, now time.Time) error {
	res, err := tx.ExecContext(ctx, `UPDATE modifier_options SET archived_at = $3, is_available = false
		WHERE store_id = $1 AND id = $2 AND archived_at IS NULL`, storeID, id, now)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrModifierOptionNotFound
	}
	return nil
}

func (r *CatalogRepository) AttachChildGroupToOption(ctx context.Context, tx *sqlx.Tx, link *domain.OptionModifierGroup) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO option_modifier_groups (store_id, parent_option_id, child_group_id, display_order)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (store_id, parent_option_id, child_group_id) DO UPDATE
		SET display_order = EXCLUDED.display_order
	`, link.StoreID, link.ParentOptionID, link.ChildGroupID, link.DisplayOrder)
	return err
}

func (r *CatalogRepository) DetachChildGroupFromOption(ctx context.Context, tx *sqlx.Tx, storeID, parentOptionID, childGroupID uuid.UUID) error {
	res, err := tx.ExecContext(ctx, `DELETE FROM option_modifier_groups WHERE store_id = $1 AND parent_option_id = $2 AND child_group_id = $3`,
		storeID, parentOptionID, childGroupID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrModifierGroupNotFound
	}
	return nil
}

func (r *CatalogRepository) HasNestedChildGroups(ctx context.Context, tx *sqlx.Tx, storeID, groupID uuid.UUID) (bool, error) {
	var has bool
	err := tx.GetContext(ctx, &has, `
		SELECT EXISTS (
			SELECT 1 FROM option_modifier_groups omg
			JOIN modifier_options mo ON mo.id = omg.parent_option_id
			WHERE mo.store_id = $1 AND mo.group_id = $2
		)
	`, storeID, groupID)
	return has, err
}

func (r *CatalogRepository) IsAttachedAsChildGroup(ctx context.Context, tx *sqlx.Tx, storeID, groupID uuid.UUID) (bool, error) {
	var isChild bool
	err := tx.GetContext(ctx, &isChild, `
		SELECT EXISTS (
			SELECT 1 FROM option_modifier_groups
			WHERE store_id = $1 AND child_group_id = $2
		)
	`, storeID, groupID)
	return isChild, err
}
