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
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/auth"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/database"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/repository/postgres"
)

// Operator-only staff administration used by cmd/migrate (owner decisions I5/I6). These run
// with the migrator connection on the server; there is no HTTP route for them.

const operatorActorName = "operator (CLI)"

var (
	storeSlugPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{1,98})[a-z0-9]$`)

	ErrInvalidStoreSlug = errors.New("store slug harus 3-100 karakter huruf kecil, angka, atau tanda hubung")
	ErrInvalidEmail     = errors.New("email tidak valid")
	ErrInvalidName      = errors.New("nama wajib diisi (maksimal 255 karakter)")
	ErrNotOwner         = errors.New("user bukan owner toko ini")
)

type BootstrapStoreInput struct {
	Slug       string
	StoreName  string
	OwnerName  string
	OwnerEmail string
	PIN        string
}

// BootstrapStore creates a tenant and its first owner in one transaction and audits it.
func BootstrapStore(ctx context.Context, db *sqlx.DB, in BootstrapStoreInput) (uuid.UUID, uuid.UUID, error) {
	slug := strings.ToLower(strings.TrimSpace(in.Slug))
	email := strings.TrimSpace(in.OwnerEmail)
	storeName, ownerName := strings.TrimSpace(in.StoreName), strings.TrimSpace(in.OwnerName)
	switch {
	case !storeSlugPattern.MatchString(slug):
		return uuid.Nil, uuid.Nil, ErrInvalidStoreSlug
	case !isPlausibleEmail(email):
		return uuid.Nil, uuid.Nil, ErrInvalidEmail
	case storeName == "" || len(storeName) > 255 || ownerName == "" || len(ownerName) > 255:
		return uuid.Nil, uuid.Nil, ErrInvalidName
	}
	if err := auth.ValidatePINPolicy(in.PIN); err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	pinHash, err := auth.HashPIN(in.PIN)
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}

	storeID, ownerID := uuid.New(), uuid.New()
	err = database.WithTenantTx(ctx, db, storeID, func(tx *sqlx.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO stores (id, slug, name) VALUES ($1, $2, $3)`, storeID, slug, storeName); err != nil {
			return fmt.Errorf("failed to create store (slug may already exist): %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO users (id, store_id, email, full_name, role, pin_hash, is_active, pin_set_at)
			VALUES ($1, $2, $3, $4, 'owner', $5, true, NOW())
		`, ownerID, storeID, email, ownerName, pinHash); err != nil {
			return fmt.Errorf("failed to create owner: %w", err)
		}
		meta, _ := json.Marshal(map[string]any{"slug": slug, "owner_id": ownerID, "owner_email": email})
		return postgres.InsertAuditLog(ctx, tx, &domain.AuditLog{
			StoreID:     storeID,
			ActorName:   operatorActorName,
			Action:      "STORE_BOOTSTRAP",
			Reason:      "Toko dan owner pertama dibuat oleh operator",
			ReferenceID: slug,
			Metadata:    string(meta),
		})
	})
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	return storeID, ownerID, nil
}

// ResetOwnerPIN is the break-glass path when no other owner can reset the PIN. It clears the
// lockout, revokes every session of that owner, and writes STAFF_PIN_RESET.
func ResetOwnerPIN(ctx context.Context, db *sqlx.DB, slug, email, pin string) error {
	if err := auth.ValidatePINPolicy(pin); err != nil {
		return err
	}
	pinHash, err := auth.HashPIN(pin)
	if err != nil {
		return err
	}
	repo := postgres.NewAuthRepository()
	storeID, _, err := repo.ResolveStoreForLogin(ctx, db, slug)
	if err != nil {
		return err
	}
	return database.WithTenantTx(ctx, db, storeID, func(tx *sqlx.Tx) error {
		user, err := repo.GetUserByEmailForUpdate(ctx, tx, storeID, strings.TrimSpace(email))
		if err != nil {
			return err
		}
		if user.Role != domain.RoleOwner {
			return ErrNotOwner
		}
		if err := repo.UpdateUserPIN(ctx, tx, storeID, user.ID, pinHash); err != nil {
			return err
		}
		revoked, err := repo.RevokeAllUserSessions(ctx, tx, storeID, user.ID, "pin_reset", time.Now().UTC())
		if err != nil {
			return err
		}
		meta, _ := json.Marshal(map[string]any{"revoked_sessions": revoked})
		return postgres.InsertAuditLog(ctx, tx, &domain.AuditLog{
			StoreID:     storeID,
			ActorName:   operatorActorName,
			Action:      "STAFF_PIN_RESET",
			Reason:      "PIN owner direset lewat jalur darurat operator",
			ReferenceID: user.ID.String(),
			Metadata:    string(meta),
		})
	})
}

// seedDevPIN is the PIN db/seeds/development.sql gives every seeded user.
const seedDevPIN = "482913"

var ErrDemoPINs = errors.New("demo PINs must be two different, policy-compliant PINs other than the seed PIN")

// ApplyDemoPINs replaces the published seed PIN of every user in the given stores before the
// demo is reachable through a public tunnel (L-1): owners get ownerPIN, other staff staffPIN.
// It runs after each `seed` (which resets the seed PIN), so no sessions are revoked or audited:
// the effective demo PIN does not change between runs.
func ApplyDemoPINs(ctx context.Context, db *sqlx.DB, storeSlugs []string, ownerPIN, staffPIN string) (int64, error) {
	if ownerPIN == staffPIN || ownerPIN == seedDevPIN || staffPIN == seedDevPIN ||
		auth.ValidatePINPolicy(ownerPIN) != nil || auth.ValidatePINPolicy(staffPIN) != nil {
		return 0, ErrDemoPINs
	}
	ownerHash, err := auth.HashPIN(ownerPIN)
	if err != nil {
		return 0, err
	}
	staffHash, err := auth.HashPIN(staffPIN)
	if err != nil {
		return 0, err
	}
	res, err := db.ExecContext(ctx, `
		UPDATE users u SET pin_hash = CASE WHEN u.role = 'owner' THEN $2 ELSE $3 END
		FROM stores s WHERE s.id = u.store_id AND s.slug = ANY($1)`, storeSlugs, ownerHash, staffHash)
	if err != nil {
		return 0, fmt.Errorf("failed to apply demo PINs: %w", err)
	}
	return res.RowsAffected()
}
