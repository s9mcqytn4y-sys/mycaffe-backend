package database_test

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/config"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/database"
)

func TestBuildSafeDatabaseURL_SpecialCharacters(t *testing.T) {
	specialPassword := "p@ss:w/rd?with#special%chars&more=1"
	dsn := database.BuildSafeDatabaseURL("db.mycaffe.internal", "5432", "mycaffe_app", specialPassword, "mycaffe_pos", "require")

	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("expected valid URL from BuildSafeDatabaseURL, got error: %v", err)
	}

	if parsed.Scheme != "postgres" {
		t.Errorf("expected scheme postgres, got %q", parsed.Scheme)
	}
	if parsed.Host != "db.mycaffe.internal:5432" {
		t.Errorf("expected host db.mycaffe.internal:5432, got %q", parsed.Host)
	}
	if parsed.User.Username() != "mycaffe_app" {
		t.Errorf("expected username mycaffe_app, got %q", parsed.User.Username())
	}
	decodedPass, ok := parsed.User.Password()
	if !ok || decodedPass != specialPassword {
		t.Errorf("expected decoded password %q, got %q", specialPassword, decodedPass)
	}
	if parsed.Query().Get("sslmode") != "require" {
		t.Errorf("expected sslmode=require, got %q", parsed.Query().Get("sslmode"))
	}
}

func TestNewPostgresDB_EmptyURLFailsClosed(t *testing.T) {
	cfg := &config.Config{
		DatabaseURL: "",
	}
	_, err := database.NewPostgresDB(cfg)
	if err == nil {
		t.Fatal("expected error when DatabaseURL is empty, got nil")
	}
}

func TestWithTenantTx_NilStoreIDFailsClosed(t *testing.T) {
	err := database.WithTenantTx(context.Background(), nil, uuid.Nil, nil)
	if err == nil || !strings.Contains(err.Error(), "tenant store_id cannot be nil") {
		t.Fatalf("expected nil store_id error, got: %v", err)
	}
}

func TestMigrator_ProductionGuards(t *testing.T) {
	migrator := database.NewMigrator(nil)

	if _, err := migrator.DownOne(context.Background(), "db/migrations", "production"); err == nil {
		t.Fatal("expected DownOne to fail closed in production, got nil")
	}

	if err := migrator.Seed(context.Background(), "db/seeds/development.sql", "production"); err == nil {
		t.Fatal("expected Seed to fail closed in production, got nil")
	}
}
