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
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/database"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/rbac"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/repository/postgres"
)

// Ingredients and recipes (K2, PR 11.3), owner only (inventory.manage, X8). Ingredients are deactivated,
// never deleted; recipes are replaced as a whole per variant or modifier option.

var ErrIngredientNotFound = fmt.Errorf("ingredient: %w", ErrCatalogNotFound)

type Ingredient struct {
	ID            uuid.UUID `json:"id" db:"id"`
	Name          string    `json:"name" db:"name"`
	Unit          string    `json:"unit" db:"unit"`
	StockQuantity int64     `json:"stock_quantity" db:"stock_quantity"`
	MinStock      int64     `json:"min_stock" db:"min_stock"`
	CostPer1000   int64     `json:"cost_per_1000" db:"cost_per_1000"`
	IsActive      bool      `json:"is_active" db:"is_active"`
	Low           bool      `json:"low" db:"low"`
	UpdatedAt     time.Time `json:"updated_at" db:"updated_at"`
}

const ingredientColumns = `id, name, unit, stock_quantity, min_stock, cost_per_1000, is_active, stock_quantity < min_stock AS low, updated_at`

// IngredientInput creates (name, unit, optional opening stock and its total cost) or patches an ingredient.
type IngredientInput struct {
	Name            *string `json:"name"`
	Unit            *string `json:"unit"`
	MinStock        *int64  `json:"min_stock"`
	CostPer1000     *int64  `json:"cost_per_1000"`
	IsActive        *bool   `json:"is_active"`
	OpeningQuantity int64   `json:"opening_quantity"`
	OpeningCost     int64   `json:"opening_cost"`
}

// RecipeItem is one ingredient of a recipe; a modifier option may use a negative quantity (X6).
type RecipeItem struct {
	IngredientID uuid.UUID `json:"ingredient_id" db:"ingredient_id"`
	Name         string    `json:"name,omitempty" db:"name"`
	Unit         string    `json:"unit,omitempty" db:"unit"`
	Quantity     int64     `json:"quantity" db:"quantity"`
}

type InventoryService struct {
	db      *sqlx.DB
	nowFunc func() time.Time
}

func NewInventoryService(db *sqlx.DB) *InventoryService {
	return &InventoryService{db: db, nowFunc: time.Now}
}

func (s *InventoryService) tx(ctx context.Context, actor *AuthenticatedStaff, fn func(tx *sqlx.Tx) error) error {
	if actor == nil {
		return ErrInvalidSession
	}
	if !actor.Can(rbac.InventoryManage) {
		return ErrForbidden
	}
	return database.WithTenantTx(ctx, s.db, actor.Staff.Store.ID, fn)
}

func (s *InventoryService) ListIngredients(ctx context.Context, actor *AuthenticatedStaff, lowOnly bool) ([]Ingredient, error) {
	out := []Ingredient{}
	err := s.tx(ctx, actor, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &out, `SELECT `+ingredientColumns+` FROM ingredients WHERE store_id = $1
			AND (NOT $2 OR (is_active AND stock_quantity < min_stock)) ORDER BY is_active DESC, lower(name)`, actor.Staff.Store.ID, lowOnly)
	})
	return out, err
}

func (s *InventoryService) CreateIngredient(ctx context.Context, actor *AuthenticatedStaff, in IngredientInput) (*Ingredient, error) {
	f := fieldErrors{}
	name, unit := "", ""
	if in.Name == nil {
		f.add("name", "wajib diisi")
	} else {
		name = f.text("name", *in.Name, 1, 80)
	}
	if in.Unit == nil {
		f.add("unit", "wajib diisi")
	} else {
		unit = *in.Unit
		f.oneOf("unit", unit, "g", "ml", "pcs")
	}
	var minStock, cost int64
	if in.MinStock != nil {
		minStock = *in.MinStock
		f.intRange("min_stock", minStock, 0, 100_000_000)
	}
	f.intRange("opening_quantity", in.OpeningQuantity, 0, 100_000_000)
	f.intRange("opening_cost", in.OpeningCost, 0, 10_000_000_000)
	if in.OpeningCost > 0 && in.OpeningQuantity == 0 {
		f.add("opening_cost", "isi opening_quantity bila ada biaya awal")
	}
	if in.OpeningQuantity > 0 {
		cost = roundHalfUpDiv(in.OpeningCost*1000, in.OpeningQuantity)
	} else if in.CostPer1000 != nil {
		cost = *in.CostPer1000
		f.intRange("cost_per_1000", cost, 0, 10_000_000_000)
	}
	if err := f.err(); err != nil {
		return nil, err
	}
	now := s.nowFunc().UTC()
	id := uuid.New()
	var out Ingredient
	err := s.tx(ctx, actor, func(tx *sqlx.Tx) error {
		storeID := actor.Staff.Store.ID
		if _, err := tx.ExecContext(ctx, `INSERT INTO ingredients (id, store_id, name, unit, stock_quantity, min_stock, cost_per_1000, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $8)`, id, storeID, name, unit, in.OpeningQuantity, minStock, cost, now); err != nil {
			if c, ok := postgres.UniqueViolation(err); ok && c == "uq_ingredients_store_name_active" {
				return &ConflictError{Field: "name", Message: "nama bahan sudah dipakai"}
			}
			return fmt.Errorf("failed to create ingredient: %w", err)
		}
		if in.OpeningQuantity > 0 {
			if _, err := tx.ExecContext(ctx, `INSERT INTO ingredient_movements (store_id, ingredient_id, kind, quantity, cost_per_1000, actor_id, note, created_at)
				VALUES ($1, $2, 'opening', $3, $4, $5, 'Saldo awal', $6)`, storeID, id, in.OpeningQuantity, cost, actor.Staff.UserID, now); err != nil {
				return err
			}
		}
		if err := s.audit(ctx, tx, actor, "INGREDIENT_CREATED", "Bahan dibuat: "+name, id, map[string]any{"unit": unit, "opening_quantity": in.OpeningQuantity, "cost_per_1000": cost}, now); err != nil {
			return err
		}
		return tx.GetContext(ctx, &out, `SELECT `+ingredientColumns+` FROM ingredients WHERE store_id = $1 AND id = $2`, storeID, id)
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// UpdateIngredient renames, sets the minimum, a manual cost (until purchases set it, PR 11.4) or deactivates.
func (s *InventoryService) UpdateIngredient(ctx context.Context, actor *AuthenticatedStaff, id uuid.UUID, in IngredientInput) (*Ingredient, error) {
	if in.Unit != nil {
		return nil, &ValidationError{Fields: map[string]string{"unit": "satuan tidak bisa diubah; buat bahan baru"}}
	}
	var out Ingredient
	err := s.tx(ctx, actor, func(tx *sqlx.Tx) error {
		storeID := actor.Staff.Store.ID
		err := tx.GetContext(ctx, &out, `SELECT `+ingredientColumns+` FROM ingredients WHERE store_id = $1 AND id = $2 FOR UPDATE`, storeID, id)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrIngredientNotFound
		}
		if err != nil {
			return err
		}
		before := out
		f := fieldErrors{}
		if in.Name != nil {
			out.Name = f.text("name", *in.Name, 1, 80)
		}
		if in.MinStock != nil {
			out.MinStock = *in.MinStock
			f.intRange("min_stock", out.MinStock, 0, 100_000_000)
		}
		if in.CostPer1000 != nil {
			out.CostPer1000 = *in.CostPer1000
			f.intRange("cost_per_1000", out.CostPer1000, 0, 10_000_000_000)
		}
		if in.IsActive != nil {
			out.IsActive = *in.IsActive
		}
		if err := f.err(); err != nil {
			return err
		}
		now := s.nowFunc().UTC()
		if _, err := tx.ExecContext(ctx, `UPDATE ingredients SET name = $3, min_stock = $4, cost_per_1000 = $5, is_active = $6, updated_at = $7
			WHERE store_id = $1 AND id = $2`, storeID, id, out.Name, out.MinStock, out.CostPer1000, out.IsActive, now); err != nil {
			if c, ok := postgres.UniqueViolation(err); ok && c == "uq_ingredients_store_name_active" {
				return &ConflictError{Field: "name", Message: "nama bahan sudah dipakai"}
			}
			return err
		}
		out.Low = out.StockQuantity < out.MinStock
		return s.audit(ctx, tx, actor, "INGREDIENT_UPDATED", "Bahan diubah: "+out.Name, id, map[string]any{"before": before, "after": out}, now)
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// recipeTarget is "variant" (product_variants) or "option" (modifier_options).
func recipeTarget(kind string) (table, column string, err error) {
	switch kind {
	case "variant":
		return "product_variants", "variant_id", nil
	case "option":
		return "modifier_options", "modifier_option_id", nil
	}
	return "", "", ErrCatalogNotFound
}

func (s *InventoryService) GetRecipe(ctx context.Context, actor *AuthenticatedStaff, kind string, id uuid.UUID) ([]RecipeItem, error) {
	_, column, err := recipeTarget(kind)
	if err != nil {
		return nil, err
	}
	out := []RecipeItem{}
	err = s.tx(ctx, actor, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &out, `SELECT r.ingredient_id, i.name, i.unit, r.quantity FROM recipe_items r
			JOIN ingredients i ON i.store_id = r.store_id AND i.id = r.ingredient_id
			WHERE r.store_id = $1 AND r.`+column+` = $2 ORDER BY lower(i.name)`, actor.Staff.Store.ID, id)
	})
	return out, err
}

// SetRecipe replaces the recipe of a variant or a modifier option; an empty list removes it.
func (s *InventoryService) SetRecipe(ctx context.Context, actor *AuthenticatedStaff, kind string, id uuid.UUID, items []RecipeItem) ([]RecipeItem, error) {
	table, column, err := recipeTarget(kind)
	if err != nil {
		return nil, err
	}
	f := fieldErrors{}
	if len(items) > 30 {
		f.add("items", "maksimal 30 bahan per resep")
	}
	seen := map[uuid.UUID]bool{}
	for i, it := range items {
		key := fmt.Sprintf("items[%d].quantity", i)
		switch {
		case it.Quantity == 0 || it.Quantity > 100000 || it.Quantity < -100000:
			f.add(key, "harus selain 0, maksimal 100000")
		case kind == "variant" && it.Quantity < 0:
			f.add(key, "resep varian hanya boleh menambah bahan")
		}
		if seen[it.IngredientID] {
			f.add(fmt.Sprintf("items[%d].ingredient_id", i), "bahan dobel")
		}
		seen[it.IngredientID] = true
	}
	if err := f.err(); err != nil {
		return nil, err
	}
	err = s.tx(ctx, actor, func(tx *sqlx.Tx) error {
		storeID := actor.Staff.Store.ID
		var exists bool
		if err := tx.GetContext(ctx, &exists, `SELECT EXISTS (SELECT 1 FROM `+table+` WHERE store_id = $1 AND id = $2 AND archived_at IS NULL)`, storeID, id); err != nil {
			return err
		}
		if !exists {
			return ErrCatalogNotFound
		}
		ids := make([]string, 0, len(items))
		for _, it := range items {
			ids = append(ids, it.IngredientID.String())
		}
		var active int
		if err := tx.GetContext(ctx, &active, `SELECT COUNT(*) FROM ingredients WHERE store_id = $1 AND id = ANY($2::uuid[]) AND is_active`, storeID, ids); err != nil {
			return err
		}
		if active != len(items) {
			return &ValidationError{Fields: map[string]string{"items": "berisi bahan yang tidak ada atau nonaktif"}}
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM recipe_items WHERE store_id = $1 AND `+column+` = $2`, storeID, id); err != nil {
			return err
		}
		for _, it := range items {
			if _, err := tx.ExecContext(ctx, `INSERT INTO recipe_items (store_id, `+column+`, ingredient_id, quantity) VALUES ($1, $2, $3, $4)`,
				storeID, id, it.IngredientID, it.Quantity); err != nil {
				return fmt.Errorf("failed to save recipe: %w", err)
			}
		}
		return s.audit(ctx, tx, actor, "RECIPE_CHANGED", "Resep "+kind+" diubah", id, map[string]any{"kind": kind, "items": items}, s.nowFunc().UTC())
	})
	if err != nil {
		return nil, err
	}
	return s.GetRecipe(ctx, actor, kind, id)
}

// SetProductUnitCost sets the unit cost of a ready-to-serve product (X9) until purchasing keeps it (11.4).
func (s *InventoryService) SetProductUnitCost(ctx context.Context, actor *AuthenticatedStaff, productID uuid.UUID, unitCost int64) error {
	if unitCost < 0 || unitCost > maxItemPrice {
		return &ValidationError{Fields: map[string]string{"unit_cost": "harus 0 - 100.000.000"}}
	}
	return s.tx(ctx, actor, func(tx *sqlx.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE products SET unit_cost = $3 WHERE store_id = $1 AND id = $2 AND archived_at IS NULL AND product_type <> 'bundle'`,
			actor.Staff.Store.ID, productID, unitCost)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return ErrCatalogNotFound
		}
		return s.audit(ctx, tx, actor, "INGREDIENT_UPDATED", "Biaya per unit produk diubah", productID, map[string]any{"unit_cost": unitCost}, s.nowFunc().UTC())
	})
}

func (s *InventoryService) audit(ctx context.Context, tx *sqlx.Tx, actor *AuthenticatedStaff, action, reason string, ref uuid.UUID, meta map[string]any, now time.Time) error {
	payload, _ := json.Marshal(meta)
	actorID := actor.Staff.UserID
	return postgres.InsertAuditLog(ctx, tx, &domain.AuditLog{StoreID: actor.Staff.Store.ID, ActorID: &actorID, ActorName: actor.Staff.FullName,
		Action: action, Reason: truncate(strings.TrimSpace(reason), 500), ReferenceID: ref.String(), Metadata: string(payload), CreatedAt: now})
}
