package main

import (
	"context"
	"flag"
	"log"
	"time"

	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/config"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/database"
)

func main() {
	action := flag.String("action", "up", "Migration action: up, down, or seed")
	migrationsDir := flag.String("dir", "db/migrations", "Path to SQL migrations directory")
	seedFile := flag.String("seed-file", "db/seeds/development.sql", "Path to development seed SQL file")
	flag.Parse()

	cfg, err := config.LoadConfig()
	if err != nil {
		log.Fatalf("[MIGRATE FATAL] Config error: %v", err)
	}

	dsn := cfg.DatabaseMigrationURL
	if dsn == "" {
		dsn = cfg.DatabaseURL
	}
	if dsn == "" {
		log.Fatalf("[MIGRATE FATAL] DATABASE_MIGRATION_URL or DATABASE_URL must be set")
	}

	migCfg := *cfg
	migCfg.DatabaseURL = dsn

	db, err := database.NewPostgresDB(&migCfg)
	if err != nil {
		log.Fatalf("[MIGRATE FATAL] Database connection failed: %v", err)
	}
	defer func() { _ = db.Close() }()

	migrator := database.NewMigrator(db)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	switch *action {
	case "up":
		count, err := migrator.Up(ctx, *migrationsDir)
		if err != nil {
			log.Fatalf("[MIGRATE UP FAILED] %v", err)
		}
		log.Printf("[MIGRATE UP] Applied %d migration(s) successfully.", count)
	case "down":
		version, err := migrator.DownOne(ctx, *migrationsDir, cfg.AppEnv)
		if err != nil {
			log.Fatalf("[MIGRATE DOWN FAILED] %v", err)
		}
		log.Printf("[MIGRATE DOWN] Rolled back migration %s successfully.", version)
	case "seed":
		if err := migrator.Seed(ctx, *seedFile, cfg.AppEnv); err != nil {
			log.Fatalf("[MIGRATE SEED FAILED] %v", err)
		}
		log.Printf("[MIGRATE SEED] Seed file %s applied successfully.", *seedFile)
	default:
		log.Fatalf("[MIGRATE FATAL] Unsupported action %q (allowed: up, down, seed)", *action)
	}
}
