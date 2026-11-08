package service

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/domain"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/pkg/database"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/pkg/rbac"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/repository/postgres"
)

// Registered POS devices (v0.10 PR 10.1, ADR-005 OF2/OF3 with W19/W20). The owner registers each device
// that may sell offline; it gets a credential "<store_id>.<device_id>.<secret>" (hash only), shown once.
// Revocation is the main control: a revoked device loses its credential and its PIN verifier records.

var (
	ErrDeviceNotFound = errors.New("device not found")
	ErrDeviceInvalid  = errors.New("device credential invalid or device revoked")
)

// OfflineVerifierTTL is how long a device-held PIN verifier stays valid (ADR-005 OF2).
const OfflineVerifierTTL = 72 * time.Hour

var devicePrefixPattern = regexp.MustCompile(`^[A-Z][A-Z0-9]{0,3}$`)

// Device is a registered POS device; Credential is only returned when registered.
type Device struct {
	ID         uuid.UUID  `json:"id" db:"id"`
	Name       string     `json:"name" db:"name"`
	Prefix     string     `json:"prefix" db:"prefix"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty" db:"revoked_at"`
	LastSeenAt *time.Time `json:"last_seen_at,omitempty" db:"last_seen_at"`
	LastSyncAt *time.Time `json:"last_sync_at,omitempty" db:"last_sync_at"`
	CreatedAt  time.Time  `json:"created_at" db:"created_at"`
	Credential string     `json:"credential,omitempty" db:"-"`
}

const deviceColumns = `id, name, prefix, revoked_at, last_seen_at, last_sync_at, created_at`

// DeviceInput registers (name + prefix) or renames (name) a device.
type DeviceInput struct {
	Name   *string `json:"name"`
	Prefix *string `json:"prefix"`
}

// DeviceCatalog is the POS catalog plus the version a device sends back with each offline order.
type DeviceCatalog struct {
	Version       string       `json:"catalog_version"`
	FirstServedAt time.Time    `json:"first_served_at"`
	Catalog       *CatalogView `json:"catalog"`
}

type DeviceService struct {
	db       *sqlx.DB
	settings *postgres.SettingsRepository
	catalog  *CatalogService
	nowFunc  func() time.Time
}

func NewDeviceService(db *sqlx.DB, catalog *CatalogService) *DeviceService {
	return &DeviceService{db: db, settings: postgres.NewSettingsRepository(), catalog: catalog, nowFunc: time.Now}
}

func requireDeviceManage(actor *AuthenticatedStaff) error {
	if actor == nil {
		return ErrInvalidSession
	}
	if !actor.Can(rbac.DeviceManage) {
		return ErrForbidden
	}
	return nil
}

// RegisterDevice creates a device and returns its credential once.
func (s *DeviceService) RegisterDevice(ctx context.Context, actor *AuthenticatedStaff, in DeviceInput) (*Device, error) {
	if err := requireDeviceManage(actor); err != nil {
		return nil, err
	}
	f := fieldErrors{}
	d := &Device{ID: uuid.New(), CreatedAt: s.nowFunc().UTC()}
	if in.Name == nil {
		f.add("name", "wajib diisi")
	} else {
		d.Name = f.text("name", *in.Name, 1, 50)
	}
	if in.Prefix == nil {
		f.add("prefix", "wajib diisi")
	} else if d.Prefix = strings.ToUpper(strings.TrimSpace(*in.Prefix)); !devicePrefixPattern.MatchString(d.Prefix) {
		f.add("prefix", "1-4 huruf/angka, diawali huruf (mis. X1)")
	}
	if err := f.err(); err != nil {
		return nil, err
	}
	secret, hash, err := newRefreshSecret()
	if err != nil {
		return nil, err
	}
	storeID := actor.Staff.Store.ID
	err = database.WithTenantTx(ctx, s.db, storeID, func(tx *sqlx.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO devices (id, store_id, name, prefix, credential_hash, registered_by, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $7)`, d.ID, storeID, d.Name, d.Prefix, hash, actor.Staff.UserID, d.CreatedAt); err != nil {
			if c, ok := postgres.UniqueViolation(err); ok && c == "uq_devices_store_prefix" {
				return &ConflictError{Field: "prefix", Message: "prefix sudah dipakai perangkat lain"}
			}
			return fmt.Errorf("failed to register device: %w", err)
		}
		return s.audit(ctx, tx, actor, "DEVICE_REGISTERED", "Perangkat POS didaftarkan", d.ID, map[string]any{"name": d.Name, "prefix": d.Prefix})
	})
	if err != nil {
		return nil, err
	}
	d.Credential = formatRefreshToken(storeID, d.ID, secret)
	return d, nil
}

func (s *DeviceService) ListDevices(ctx context.Context, actor *AuthenticatedStaff) ([]Device, error) {
	if err := requireDeviceManage(actor); err != nil {
		return nil, err
	}
	out := []Device{}
	err := database.WithTenantTx(ctx, s.db, actor.Staff.Store.ID, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &out, `SELECT `+deviceColumns+` FROM devices WHERE store_id = $1
			ORDER BY revoked_at IS NOT NULL, prefix`, actor.Staff.Store.ID)
	})
	return out, err
}

// RenameDevice changes the display name; the prefix is fixed because printed offline numbers carry it.
func (s *DeviceService) RenameDevice(ctx context.Context, actor *AuthenticatedStaff, id uuid.UUID, in DeviceInput) (*Device, error) {
	if err := requireDeviceManage(actor); err != nil {
		return nil, err
	}
	f := fieldErrors{}
	name := ""
	if in.Name == nil {
		f.add("name", "wajib diisi")
	} else {
		name = f.text("name", *in.Name, 1, 50)
	}
	if in.Prefix != nil {
		f.add("prefix", "tidak bisa diubah; daftarkan perangkat baru")
	}
	if err := f.err(); err != nil {
		return nil, err
	}
	var d Device
	storeID := actor.Staff.Store.ID
	err := database.WithTenantTx(ctx, s.db, storeID, func(tx *sqlx.Tx) error {
		if err := loadDeviceForUpdate(ctx, tx, storeID, id, &d); err != nil {
			return err
		}
		before := d.Name
		d.Name = name
		if _, err := tx.ExecContext(ctx, `UPDATE devices SET name = $3, updated_at = $4 WHERE store_id = $1 AND id = $2`,
			storeID, id, name, s.nowFunc().UTC()); err != nil {
			return err
		}
		return s.audit(ctx, tx, actor, "DEVICE_RENAMED", "Nama perangkat POS diubah", id, map[string]any{"before": before, "after": name})
	})
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// RevokeDevice stops the device credential at once and drops its PIN verifier records. Idempotent.
func (s *DeviceService) RevokeDevice(ctx context.Context, actor *AuthenticatedStaff, id uuid.UUID) (*Device, error) {
	if err := requireDeviceManage(actor); err != nil {
		return nil, err
	}
	var d Device
	storeID := actor.Staff.Store.ID
	err := database.WithTenantTx(ctx, s.db, storeID, func(tx *sqlx.Tx) error {
		if err := loadDeviceForUpdate(ctx, tx, storeID, id, &d); err != nil {
			return err
		}
		if d.RevokedAt != nil {
			return nil
		}
		now := s.nowFunc().UTC()
		d.RevokedAt = &now
		if _, err := tx.ExecContext(ctx, `UPDATE devices SET revoked_at = $3, revoked_by = $4, updated_at = $3 WHERE store_id = $1 AND id = $2`,
			storeID, id, now, actor.Staff.UserID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM device_pin_verifiers WHERE store_id = $1 AND device_id = $2`, storeID, id); err != nil {
			return err
		}
		return s.audit(ctx, tx, actor, "DEVICE_REVOKED", "Perangkat POS dicabut", id, map[string]any{"name": d.Name, "prefix": d.Prefix})
	})
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// Catalog serves the POS catalog to a device and stores the pricing snapshot of what it served (W20):
// version = SHA-256 of the canonical pricing content, inserted once, staleness counted from first serve.
func (s *DeviceService) Catalog(ctx context.Context, credential string) (*DeviceCatalog, error) {
	storeID, deviceID, secret, ok := parseRefreshToken(credential)
	if !ok {
		return nil, ErrDeviceInvalid
	}
	var out *DeviceCatalog
	err := database.WithTenantTx(ctx, s.db, storeID, func(tx *sqlx.Tx) error {
		now := s.nowFunc().UTC()
		if _, err := authenticateDeviceTx(ctx, tx, storeID, deviceID, secret, now); err != nil {
			return err
		}
		settings, err := s.settings.GetStoreSettings(ctx, tx, storeID)
		if err != nil {
			return err
		}
		view, err := s.catalog.catalogTx(ctx, tx, storeID, settings, true)
		if err != nil {
			return err
		}
		view.GeneratedAt = now
		snapshot, err := json.Marshal(pricingSnapshot(view, settings))
		if err != nil {
			return err
		}
		sum := sha256.Sum256(snapshot)
		version := hex.EncodeToString(sum[:])
		if _, err := tx.ExecContext(ctx, `INSERT INTO catalog_versions (store_id, version, snapshot, first_served_at)
			VALUES ($1, $2, $3, $4) ON CONFLICT (store_id, version) DO NOTHING`, storeID, version, snapshot, now); err != nil {
			return fmt.Errorf("failed to store catalog snapshot: %w", err)
		}
		out = &DeviceCatalog{Version: version, Catalog: view}
		return tx.GetContext(ctx, &out.FirstServedAt, `SELECT first_served_at FROM catalog_versions WHERE store_id = $1 AND version = $2`, storeID, version)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// snapshotOption/Group/Product keep only what re-pricing needs (W16/W20): stock and availability change
// all day and must not create new versions.
type snapshotOption struct {
	ID         uuid.UUID       `json:"id"`
	Name       string          `json:"name"`
	PriceDelta int64           `json:"price_delta"`
	MaxQty     int             `json:"max_quantity"`
	SubGroups  []snapshotGroup `json:"sub_groups"`
}

type snapshotGroup struct {
	ID        uuid.UUID        `json:"id"`
	Selection string           `json:"selection"`
	Required  bool             `json:"required"`
	Min       int              `json:"min"`
	Max       int              `json:"max"`
	Options   []snapshotOption `json:"options"`
}

type snapshotVariant struct {
	ID    uuid.UUID `json:"id"`
	Name  string    `json:"name"`
	Price int64     `json:"price"`
}

type snapshotProduct struct {
	ID             uuid.UUID         `json:"id"`
	Name           string            `json:"name"`
	PrepStation    string            `json:"prep_station"`
	TrackStock     bool              `json:"track_stock"`
	Variants       []snapshotVariant `json:"variants"`
	ModifierGroups []snapshotGroup   `json:"modifier_groups"`
}

type catalogSnapshot struct {
	TaxBPS                int64             `json:"tax_bps"`
	ServiceBPS            int64             `json:"service_bps"`
	CashRoundingStep      int               `json:"cash_rounding_step"`
	CashierMaxDiscountBPS int64             `json:"cashier_max_discount_bps"`
	Products              []snapshotProduct `json:"products"`
	// Promotions travel with the snapshot so an offline order is priced as it was sold (X3).
	Promotions []CatalogPromotion `json:"promotions"`
}

func pricingSnapshot(view *CatalogView, settings domain.StoreSettings) catalogSnapshot {
	var groups func([]CatalogGroup) []snapshotGroup
	groups = func(in []CatalogGroup) []snapshotGroup {
		out := make([]snapshotGroup, 0, len(in))
		for _, g := range in {
			sg := snapshotGroup{ID: g.ID, Selection: g.Selection, Required: g.Required, Min: g.Min, Max: g.Max, Options: make([]snapshotOption, 0, len(g.Options))}
			for _, o := range g.Options {
				sg.Options = append(sg.Options, snapshotOption{ID: o.ID, Name: o.Name, PriceDelta: o.PriceDelta, MaxQty: o.MaxQty, SubGroups: groups(o.SubGroups)})
			}
			out = append(out, sg)
		}
		return out
	}
	snap := catalogSnapshot{
		TaxBPS: view.Store.TaxBPS, ServiceBPS: view.Store.ServiceBPS,
		CashRoundingStep: settings.CashRoundingStep, CashierMaxDiscountBPS: settings.CashierMaxDiscountBPS,
		Products:   make([]snapshotProduct, 0, len(view.Products)),
		Promotions: view.Promotions,
	}
	for _, p := range view.Products {
		sp := snapshotProduct{ID: p.ID, Name: p.Name, PrepStation: p.PrepStation, TrackStock: p.TrackStock,
			Variants: make([]snapshotVariant, 0, len(p.Variants)), ModifierGroups: groups(p.ModifierGroups)}
		for _, v := range p.Variants {
			sp.Variants = append(sp.Variants, snapshotVariant{ID: v.ID, Name: v.Name, Price: v.Price})
		}
		snap.Products = append(snap.Products, sp)
	}
	return snap
}

// authenticateDeviceTx checks a device credential inside the tenant transaction (the store id in the
// credential only selects the tenant) and records last_seen_at at most once a minute.
func authenticateDeviceTx(ctx context.Context, tx *sqlx.Tx, storeID, deviceID uuid.UUID, secret string, now time.Time) (*Device, error) {
	var row struct {
		Device
		Hash        string `db:"credential_hash"`
		StoreActive bool   `db:"store_active"`
	}
	err := tx.GetContext(ctx, &row, `SELECT d.id, d.name, d.prefix, d.revoked_at, d.last_seen_at, d.last_sync_at, d.created_at,
			d.credential_hash, s.is_active AS store_active
		FROM devices d JOIN stores s ON s.id = d.store_id WHERE d.store_id = $1 AND d.id = $2`, storeID, deviceID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrDeviceInvalid
	}
	if err != nil {
		return nil, err
	}
	if row.RevokedAt != nil || !row.StoreActive || !constantTimeEqual(hashRefreshSecret(secret), row.Hash) {
		return nil, ErrDeviceInvalid
	}
	if _, err := tx.ExecContext(ctx, `UPDATE devices SET last_seen_at = $3 WHERE store_id = $1 AND id = $2
		AND (last_seen_at IS NULL OR last_seen_at < $3::timestamptz - INTERVAL '1 minute')`, storeID, deviceID, now); err != nil {
		return nil, err
	}
	return &row.Device, nil
}

func loadDeviceForUpdate(ctx context.Context, tx *sqlx.Tx, storeID, id uuid.UUID, d *Device) error {
	err := tx.GetContext(ctx, d, `SELECT `+deviceColumns+` FROM devices WHERE store_id = $1 AND id = $2 FOR UPDATE`, storeID, id)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrDeviceNotFound
	}
	return err
}

func (s *DeviceService) audit(ctx context.Context, tx *sqlx.Tx, actor *AuthenticatedStaff, action, reason string, ref uuid.UUID, meta map[string]any) error {
	payload, _ := json.Marshal(meta)
	actorID := actor.Staff.UserID
	return postgres.InsertAuditLog(ctx, tx, &domain.AuditLog{
		StoreID: actor.Staff.Store.ID, ActorID: &actorID, ActorName: actor.Staff.FullName, Action: action,
		Reason: reason, ReferenceID: ref.String(), Metadata: string(payload), CreatedAt: s.nowFunc().UTC(),
	})
}
