package database

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/jmoiron/sqlx"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/config"
)

// BuildSafeDatabaseURL constructs a PostgreSQL connection URI that safely percent-encodes
// credentials containing URL-reserved characters (@, :, /, ?, #, %).
func BuildSafeDatabaseURL(host, port, user, password, dbName, sslMode string) string {
	if port == "" {
		port = "5432"
	}
	if sslMode == "" {
		sslMode = "disable"
	}
	u := &url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(user, password),
		Host:   fmt.Sprintf("%s:%s", host, port),
		Path:   dbName,
	}
	q := u.Query()
	q.Set("sslmode", sslMode)
	u.RawQuery = q.Encode()
	return u.String()
}

// NewPostgresDB initializes a sqlx connection pool backed by pgx/v5/stdlib.
func NewPostgresDB(cfg *config.Config) (*sqlx.DB, error) {
	if cfg.DatabaseURL == "" {
		return nil, fmt.Errorf("database URL is empty")
	}

	db, err := sqlx.Open("pgx", cfg.DatabaseURL)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(10)
	db.SetConnMaxLifetime(15 * time.Minute)
	db.SetConnMaxIdleTime(5 * time.Minute)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("database ping failed: %w", err)
	}

	return db, nil
}

// WithTenantTx executes fn inside an ACID database transaction with the PostgreSQL
// session-local tenant context (app.current_store_id) set for Row-Level Security (RLS).
func WithTenantTx(ctx context.Context, db *sqlx.DB, storeID uuid.UUID, fn func(tx *sqlx.Tx) error) error {
	if storeID == uuid.Nil {
		return fmt.Errorf("tenant store_id cannot be nil")
	}

	tx, err := db.BeginTxx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	if _, err := tx.ExecContext(ctx, "SELECT set_config('app.current_store_id', $1, true)", storeID.String()); err != nil {
		return fmt.Errorf("failed to set tenant RLS context: %w", err)
	}

	if err := fn(tx); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit tenant transaction: %w", err)
	}

	return nil
}
