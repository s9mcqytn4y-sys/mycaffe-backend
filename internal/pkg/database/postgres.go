package database

import (
	"context"
	"fmt"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/jmoiron/sqlx"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/config"
)

func NewPostgresDB(cfg *config.Config) (*sqlx.DB, error) {
	if cfg.DatabaseURL == "" {
		return nil, fmt.Errorf("database URL is empty")
	}

	db, err := sqlx.Open("pgx", cfg.DatabaseURL)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Connection pool settings
	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(10)
	db.SetConnMaxLifetime(15 * time.Minute)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		// Log warning but allow server to boot if offline development
		return db, fmt.Errorf("database ping failed: %w", err)
	}

	return db, nil
}
