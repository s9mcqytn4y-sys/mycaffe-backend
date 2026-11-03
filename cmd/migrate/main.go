package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/config"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/database"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/service"
	"golang.org/x/term"
)

func main() {
	action := flag.String("action", "up", "Action: up, down, seed, demo-pins, bootstrap-store, or reset-owner-pin")
	migrationsDir := flag.String("dir", "db/migrations", "Path to SQL migrations directory")
	seedFile := flag.String("seed-file", "db/seeds/development.sql", "Path to development seed SQL file")
	storeSlug := flag.String("store-slug", "", "bootstrap-store/reset-owner-pin: store slug (login code); demo-pins: comma-separated slugs")
	storeName := flag.String("store-name", "", "bootstrap-store: store display name")
	ownerName := flag.String("owner-name", "", "bootstrap-store: owner full name")
	ownerEmail := flag.String("owner-email", "", "bootstrap-store/reset-owner-pin: owner email")
	flag.Parse()

	cfg, err := config.LoadMigrateConfig()
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
		if cfg.DBAppPassword != "" {
			if err := migrator.ProvisionAppRole(ctx, cfg.DBAppUser, cfg.DBAppPassword); err != nil {
				log.Fatalf("[MIGRATE ROLE PROVISION FAILED] %v", err)
			}
			log.Printf("[MIGRATE ROLE PROVISION] Runtime role %q configured with direct login.", cfg.DBAppUser)
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
	case "demo-pins":
		// Demo stack only (L-1): PINs come from the git-ignored .env.demo, never from the repo.
		if cfg.AppEnv != "staging" {
			log.Fatalf("[DEMO PINS] refused: APP_ENV must be staging, got %q", cfg.AppEnv)
		}
		n, err := service.ApplyDemoPINs(ctx, db, strings.Split(*storeSlug, ","), os.Getenv("DEMO_OWNER_PIN"), os.Getenv("DEMO_STAFF_PIN"))
		if err != nil {
			log.Fatalf("[DEMO PINS FAILED] %v (set DEMO_OWNER_PIN and DEMO_STAFF_PIN in .env.demo)", err)
		}
		log.Printf("[DEMO PINS] Seed PIN replaced for %d user(s) in %s.", n, *storeSlug)
	case "bootstrap-store":
		pin := mustReadPIN()
		storeID, ownerID, err := service.BootstrapStore(ctx, db, service.BootstrapStoreInput{
			Slug: *storeSlug, StoreName: *storeName, OwnerName: *ownerName, OwnerEmail: *ownerEmail, PIN: pin,
		})
		if err != nil {
			log.Fatalf("[BOOTSTRAP FAILED] %v", err)
		}
		log.Printf("[BOOTSTRAP] Store %q created (store_id=%s, owner_id=%s). Owner can now log in with store code %q.", *storeSlug, storeID, ownerID, *storeSlug)
	case "reset-owner-pin":
		pin := mustReadPIN()
		if err := service.ResetOwnerPIN(ctx, db, *storeSlug, *ownerEmail, pin); err != nil {
			log.Fatalf("[RESET OWNER PIN FAILED] %v", err)
		}
		log.Printf("[RESET OWNER PIN] PIN reset for owner %q in store %q; all sessions revoked.", *ownerEmail, *storeSlug)
	default:
		log.Fatalf("[MIGRATE FATAL] Unsupported action %q (allowed: up, down, seed, demo-pins, bootstrap-store, reset-owner-pin)", *action)
	}
}

// mustReadPIN reads the PIN twice without echo from a terminal, or one line from piped stdin
// (for scripted use). The PIN never appears in flags, environment, or shell history.
func mustReadPIN() string {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && line == "" {
			log.Fatalf("[PIN] failed to read PIN from stdin: %v", err)
		}
		return strings.TrimSpace(line)
	}
	read := func(prompt string) string {
		fmt.Fprint(os.Stderr, prompt)
		b, err := term.ReadPassword(fd)
		fmt.Fprintln(os.Stderr)
		if err != nil {
			log.Fatalf("[PIN] failed to read PIN: %v", err)
		}
		return strings.TrimSpace(string(b))
	}
	pin := read("PIN owner (6-12 digit): ")
	if read("Ulangi PIN: ") != pin {
		log.Fatal("[PIN] PIN tidak sama")
	}
	return pin
}
