package database

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jmoiron/sqlx"
)

const migrationAdvisoryLockID int64 = 88421901

// Migrator manages version-controlled PostgreSQL schema migrations with advisory locks.
type Migrator struct {
	db *sqlx.DB
}

func NewMigrator(db *sqlx.DB) *Migrator {
	return &Migrator{db: db}
}

func (m *Migrator) acquireLock(ctx context.Context, conn *sqlx.Conn) error {
	if _, err := conn.ExecContext(ctx, "SELECT pg_advisory_lock($1)", migrationAdvisoryLockID); err != nil {
		return fmt.Errorf("failed to acquire migration advisory lock: %w", err)
	}
	return nil
}

func (m *Migrator) releaseLock(ctx context.Context, conn *sqlx.Conn) {
	_, _ = conn.ExecContext(ctx, "SELECT pg_advisory_unlock($1)", migrationAdvisoryLockID)
}

func (m *Migrator) ensureVersionTable(ctx context.Context, conn *sqlx.Conn) error {
	const query = `
	CREATE TABLE IF NOT EXISTS schema_migrations (
		version VARCHAR(255) PRIMARY KEY,
		checksum VARCHAR(64) NOT NULL,
		applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
	);`
	_, err := conn.ExecContext(ctx, query)
	return err
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Up applies all pending *.up.sql migrations in lexicographical order.
func (m *Migrator) Up(ctx context.Context, migrationsDir string) (int, error) {
	conn, err := m.db.Connx(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to acquire dedicated DB connection: %w", err)
	}
	defer func() { _ = conn.Close() }()

	if err := m.acquireLock(ctx, conn); err != nil {
		return 0, err
	}
	defer m.releaseLock(ctx, conn)

	if err := m.ensureVersionTable(ctx, conn); err != nil {
		return 0, fmt.Errorf("failed to ensure schema_migrations table: %w", err)
	}

	entries, err := os.ReadDir(migrationsDir)
	if err != nil {
		return 0, fmt.Errorf("failed to read migrations directory %q: %w", migrationsDir, err)
	}

	var upFiles []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".up.sql") {
			upFiles = append(upFiles, entry.Name())
		}
	}
	sort.Strings(upFiles)

	appliedCount := 0
	for _, fileName := range upFiles {
		version := strings.TrimSuffix(fileName, ".up.sql")
		fullPath := filepath.Join(migrationsDir, fileName)
		content, err := os.ReadFile(fullPath)
		if err != nil {
			return appliedCount, fmt.Errorf("failed to read migration %s: %w", fileName, err)
		}
		checksum := sha256Hex(content)

		var existingChecksum string
		err = conn.GetContext(ctx, &existingChecksum, "SELECT checksum FROM schema_migrations WHERE version = $1", version)
		if err == nil {
			if existingChecksum != checksum {
				return appliedCount, fmt.Errorf("migration checksum mismatch for %s: expected %s, got %s", version, existingChecksum, checksum)
			}
			continue
		}

		tx, err := conn.BeginTxx(ctx, nil)
		if err != nil {
			return appliedCount, fmt.Errorf("failed to begin transaction for %s: %w", fileName, err)
		}

		if _, err := tx.ExecContext(ctx, string(content)); err != nil {
			_ = tx.Rollback()
			return appliedCount, fmt.Errorf("failed to execute migration %s: %w", fileName, err)
		}

		if _, err := tx.ExecContext(ctx, "INSERT INTO schema_migrations (version, checksum) VALUES ($1, $2)", version, checksum); err != nil {
			_ = tx.Rollback()
			return appliedCount, fmt.Errorf("failed to record migration version %s: %w", version, err)
		}

		if err := tx.Commit(); err != nil {
			return appliedCount, fmt.Errorf("failed to commit migration %s: %w", fileName, err)
		}
		appliedCount++
	}

	return appliedCount, nil
}

// DownOne rolls back the latest applied migration. Strictly blocked in production.
func (m *Migrator) DownOne(ctx context.Context, migrationsDir, appEnv string) (string, error) {
	if strings.EqualFold(strings.TrimSpace(appEnv), "production") {
		return "", errors.New("[SECURITY FAIL-CLOSED] destructive down migration is forbidden in production")
	}

	conn, err := m.db.Connx(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to acquire dedicated DB connection: %w", err)
	}
	defer func() { _ = conn.Close() }()

	if err := m.acquireLock(ctx, conn); err != nil {
		return "", err
	}
	defer m.releaseLock(ctx, conn)

	if err := m.ensureVersionTable(ctx, conn); err != nil {
		return "", err
	}

	var latestVersion string
	err = conn.GetContext(ctx, &latestVersion, "SELECT version FROM schema_migrations ORDER BY version DESC LIMIT 1")
	if err != nil {
		return "", fmt.Errorf("no applied migration found to roll back: %w", err)
	}

	downFile := filepath.Join(migrationsDir, latestVersion+".down.sql")
	content, err := os.ReadFile(downFile)
	if err != nil {
		return "", fmt.Errorf("failed to read down migration %s: %w", downFile, err)
	}

	tx, err := conn.BeginTxx(ctx, nil)
	if err != nil {
		return "", err
	}

	if _, err := tx.ExecContext(ctx, string(content)); err != nil {
		_ = tx.Rollback()
		return "", fmt.Errorf("failed to execute down migration %s: %w", latestVersion, err)
	}

	if _, err := tx.ExecContext(ctx, "DELETE FROM schema_migrations WHERE version = $1", latestVersion); err != nil {
		_ = tx.Rollback()
		return "", fmt.Errorf("failed to remove version %s from schema_migrations: %w", latestVersion, err)
	}

	if err := tx.Commit(); err != nil {
		return "", err
	}

	return latestVersion, nil
}

// Seed executes a development seed file. Strictly blocked in production.
func (m *Migrator) Seed(ctx context.Context, seedFilePath, appEnv string) error {
	if strings.EqualFold(strings.TrimSpace(appEnv), "production") {
		return errors.New("[SECURITY FAIL-CLOSED] development seed execution is strictly forbidden in production")
	}

	content, err := os.ReadFile(seedFilePath)
	if err != nil {
		return fmt.Errorf("failed to read seed file %s: %w", seedFilePath, err)
	}

	tx, err := m.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, string(content)); err != nil {
		return fmt.Errorf("failed to execute seed SQL: %w", err)
	}

	return tx.Commit()
}

// ProvisionAppRole configures the runtime application role (e.g. mycaffe_app) with a login password
// and enforces NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS so it can authenticate directly via pg_hba.
func (m *Migrator) ProvisionAppRole(ctx context.Context, appUser, appPassword string) error {
	appUser = strings.TrimSpace(appUser)
	if appUser == "" {
		appUser = "mycaffe_app"
	}
	for i := 0; i < len(appUser); i++ {
		c := appUser[i]
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9' && i > 0) || c == '_') {
			return fmt.Errorf("invalid SQL identifier for app role: %q", appUser)
		}
	}
	if appPassword == "" {
		return nil
	}

	var callerPerms struct {
		RolSuper      bool `db:"rolsuper"`
		RolCreateRole bool `db:"rolcreaterole"`
	}
	if err := m.db.GetContext(ctx, &callerPerms, "SELECT rolsuper, rolcreaterole FROM pg_roles WHERE rolname = current_user"); err != nil {
		return fmt.Errorf("failed to inspect caller role capabilities: %w", err)
	}

	// When connected as least-privilege mycaffe_migrator (NOSUPERUSER NOCREATEROLE),
	// 00-init-roles.sh has already provisioned mycaffe_app with its password.
	// Verify mycaffe_app exists with login enabled and without superuser/bypassrls.
	if !callerPerms.RolSuper && !callerPerms.RolCreateRole {
		var appRoleFlags struct {
			RolCanLogin  bool `db:"rolcanlogin"`
			RolSuper     bool `db:"rolsuper"`
			RolBypassRLS bool `db:"rolbypassrls"`
		}
		if err := m.db.GetContext(ctx, &appRoleFlags, "SELECT rolcanlogin, rolsuper, rolbypassrls FROM pg_roles WHERE rolname = $1", appUser); err != nil {
			return fmt.Errorf("runtime role %q not found and caller lacks CREATEROLE to provision it: %w", appUser, err)
		}
		if !appRoleFlags.RolCanLogin || appRoleFlags.RolSuper || appRoleFlags.RolBypassRLS {
			return fmt.Errorf("runtime role %q has invalid security flags (canlogin=%v, super=%v, bypassrls=%v)",
				appUser, appRoleFlags.RolCanLogin, appRoleFlags.RolSuper, appRoleFlags.RolBypassRLS)
		}
		return nil
	}

	tx, err := m.db.BeginTxx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction for role provisioning: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx,
		"SELECT set_config('app.provision_role', $1, true), set_config('app.provision_password', $2, true)",
		appUser, appPassword,
	); err != nil {
		return fmt.Errorf("failed to stage role credentials: %w", err)
	}

	const doBlock = `
	DO $$
	DECLARE
		r_name TEXT := current_setting('app.provision_role', true);
		r_pass TEXT := current_setting('app.provision_password', true);
	BEGIN
		IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = r_name) THEN
			EXECUTE format('CREATE ROLE %I WITH LOGIN PASSWORD %L NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS', r_name, r_pass);
		ELSE
			EXECUTE format('ALTER ROLE %I WITH LOGIN PASSWORD %L NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS', r_name, r_pass);
		END IF;
	END $$;`

	if _, err := tx.ExecContext(ctx, doBlock); err != nil {
		return fmt.Errorf("failed to provision role %s: %w", appUser, err)
	}

	return tx.Commit()
}
