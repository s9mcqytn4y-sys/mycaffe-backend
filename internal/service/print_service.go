package service

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/domain"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/database"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/rbac"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/repository/postgres"
)

// Print queue (v0.9 PR 9.4, decision W8). Owners register printers per station; each gets an agent
// token "<store_id>.<printer_id>.<secret>" (hash only). Jobs are rendered on the server when a KDS
// ticket is created (payment settled or round submitted), so a duplicate webhook never prints twice.
// A print agent on the cafe LAN claims a job, prints it and acknowledges it.

var (
	ErrPrinterNotFound     = errors.New("printer not found")
	ErrPrinterTokenInvalid = errors.New("printer token invalid or printer inactive")
	ErrPrintJobNotFound    = errors.New("print job not found or not claimed by this printer")
	ErrNoPrinter           = errors.New("no active printer for this print")
)

const (
	printMaxAttempts = 5
	printClaimLease  = 60 * time.Second
)

// Printer is a configured printer; AgentToken is only returned when created or rotated.
type Printer struct {
	ID            uuid.UUID  `json:"id" db:"id"`
	Name          string     `json:"name" db:"name"`
	Station       string     `json:"station" db:"station"`
	Format        string     `json:"format" db:"format"`
	PaperWidthMM  int        `json:"paper_width_mm" db:"paper_width_mm"`
	LabelHeightMM *int       `json:"label_height_mm,omitempty" db:"label_height_mm"`
	Copies        int        `json:"copies" db:"copies"`
	Footer        string     `json:"footer" db:"footer"`
	IsActive      bool       `json:"is_active" db:"is_active"`
	LastSeenAt    *time.Time `json:"last_seen_at,omitempty" db:"last_seen_at"`
	CreatedAt     time.Time  `json:"created_at" db:"created_at"`
	AgentToken    string     `json:"agent_token,omitempty" db:"-"`
}

const printerColumns = `id, name, station, format, paper_width_mm, label_height_mm, copies, footer, is_active, last_seen_at, created_at`

// PrinterInput creates or patches a printer; the format follows the station (label = TSPL).
type PrinterInput struct {
	Name          *string `json:"name"`
	Station       *string `json:"station"`
	PaperWidthMM  *int    `json:"paper_width_mm"`
	LabelHeightMM *int    `json:"label_height_mm"`
	Copies        *int    `json:"copies"`
	Footer        *string `json:"footer"`
	IsActive      *bool   `json:"is_active"`
}

// AgentJob is what a print agent receives: raw printer bytes, base64 encoded.
type AgentJob struct {
	ID       uuid.UUID `json:"id"`
	Kind     string    `json:"kind"`
	Format   string    `json:"format"`
	Payload  string    `json:"payload_base64"`
	Attempts int       `json:"attempts"`
}

type PrintService struct {
	db      *sqlx.DB
	orders  *postgres.OrderRepository
	nowFunc func() time.Time
}

func NewPrintService(db *sqlx.DB) *PrintService {
	return &PrintService{db: db, orders: postgres.NewOrderRepository(), nowFunc: time.Now}
}

func applyPrinter(p *Printer, in PrinterInput) error {
	f := fieldErrors{}
	if in.Name != nil {
		p.Name = f.text("name", *in.Name, 1, 50)
	}
	if in.Station != nil {
		f.oneOf("station", *in.Station, "bar", "kitchen", "checker", "label")
		p.Station = *in.Station
	}
	if in.Footer != nil {
		p.Footer = f.text("footer", *in.Footer, 0, 100)
	}
	if in.Copies != nil {
		f.intRange("copies", int64(*in.Copies), 1, 5)
		p.Copies = *in.Copies
	}
	if in.IsActive != nil {
		p.IsActive = *in.IsActive
	}
	if in.PaperWidthMM != nil {
		p.PaperWidthMM = *in.PaperWidthMM
	}
	if in.LabelHeightMM != nil {
		p.LabelHeightMM = in.LabelHeightMM
	}
	// Defaults follow the station when it changes kind, unless the caller set the size.
	if p.Station == "label" {
		p.Format = "tspl"
		if in.PaperWidthMM == nil && (p.PaperWidthMM < 25 || p.PaperWidthMM > 110) {
			p.PaperWidthMM = 40
		}
		if p.LabelHeightMM == nil {
			h := 30
			p.LabelHeightMM = &h
		}
		f.intRange("paper_width_mm", int64(p.PaperWidthMM), 25, 110)
		f.intRange("label_height_mm", int64(*p.LabelHeightMM), 15, 100)
	} else {
		p.Format = "escpos"
		p.LabelHeightMM = nil
		if in.PaperWidthMM == nil && p.PaperWidthMM != 58 && p.PaperWidthMM != 80 {
			p.PaperWidthMM = 58
		}
		if p.PaperWidthMM != 58 && p.PaperWidthMM != 80 {
			f.add("paper_width_mm", "harus 58 atau 80")
		}
	}
	if p.Name == "" {
		f.add("name", "wajib diisi")
	}
	return f.err()
}

// CreatePrinter registers a printer and returns its agent token once.
func (s *PrintService) CreatePrinter(ctx context.Context, actor *AuthenticatedStaff, in PrinterInput) (*Printer, error) {
	if err := requireStoreSettings(actor); err != nil {
		return nil, err
	}
	if in.Station == nil {
		return nil, &ValidationError{Fields: map[string]string{"station": "wajib diisi"}}
	}
	p := &Printer{ID: uuid.New(), Copies: 1, IsActive: true, CreatedAt: s.nowFunc().UTC()}
	if err := applyPrinter(p, in); err != nil {
		return nil, err
	}
	secret, hash, err := newRefreshSecret()
	if err != nil {
		return nil, err
	}
	storeID := actor.Staff.Store.ID
	err = database.WithTenantTx(ctx, s.db, storeID, func(tx *sqlx.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO printers (id, store_id, name, station, format, paper_width_mm, label_height_mm,
			copies, footer, agent_token_hash, is_active, created_by, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $13)`,
			p.ID, storeID, p.Name, p.Station, p.Format, p.PaperWidthMM, p.LabelHeightMM, p.Copies, p.Footer, hash, p.IsActive,
			actor.Staff.UserID, p.CreatedAt); err != nil {
			return fmt.Errorf("failed to create printer: %w", err)
		}
		return s.audit(ctx, tx, actor, "PRINTER_CREATED", "Printer didaftarkan", p.ID, map[string]any{"name": p.Name, "station": p.Station})
	})
	if err != nil {
		return nil, err
	}
	p.AgentToken = formatRefreshToken(storeID, p.ID, secret)
	return p, nil
}

func (s *PrintService) ListPrinters(ctx context.Context, actor *AuthenticatedStaff) ([]Printer, error) {
	if err := requireStoreSettings(actor); err != nil {
		return nil, err
	}
	out := []Printer{}
	err := database.WithTenantTx(ctx, s.db, actor.Staff.Store.ID, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &out, `SELECT `+printerColumns+` FROM printers WHERE store_id = $1 ORDER BY station, lower(name)`, actor.Staff.Store.ID)
	})
	return out, err
}

func (s *PrintService) UpdatePrinter(ctx context.Context, actor *AuthenticatedStaff, id uuid.UUID, in PrinterInput) (*Printer, error) {
	if err := requireStoreSettings(actor); err != nil {
		return nil, err
	}
	var p Printer
	storeID := actor.Staff.Store.ID
	err := database.WithTenantTx(ctx, s.db, storeID, func(tx *sqlx.Tx) error {
		if err := s.loadPrinter(ctx, tx, storeID, id, &p); err != nil {
			return err
		}
		before := p
		if err := applyPrinter(&p, in); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE printers SET name = $3, station = $4, format = $5, paper_width_mm = $6,
			label_height_mm = $7, copies = $8, footer = $9, is_active = $10, updated_at = $11 WHERE store_id = $1 AND id = $2`,
			storeID, id, p.Name, p.Station, p.Format, p.PaperWidthMM, p.LabelHeightMM, p.Copies, p.Footer, p.IsActive, s.nowFunc().UTC()); err != nil {
			return fmt.Errorf("failed to update printer: %w", err)
		}
		return s.audit(ctx, tx, actor, "PRINTER_UPDATED", "Pengaturan printer diubah", id, map[string]any{"before": before, "after": p})
	})
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// RotateToken replaces the agent token (agent reinstalled or token lost); the old one stops at once.
func (s *PrintService) RotateToken(ctx context.Context, actor *AuthenticatedStaff, id uuid.UUID) (*Printer, error) {
	if err := requireStoreSettings(actor); err != nil {
		return nil, err
	}
	secret, hash, err := newRefreshSecret()
	if err != nil {
		return nil, err
	}
	var p Printer
	storeID := actor.Staff.Store.ID
	err = database.WithTenantTx(ctx, s.db, storeID, func(tx *sqlx.Tx) error {
		if err := s.loadPrinter(ctx, tx, storeID, id, &p); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE printers SET agent_token_hash = $3, updated_at = $4 WHERE store_id = $1 AND id = $2`,
			storeID, id, hash, s.nowFunc().UTC()); err != nil {
			return err
		}
		return s.audit(ctx, tx, actor, "PRINTER_TOKEN_ROTATED", "Token agen printer diganti", id, nil)
	})
	if err != nil {
		return nil, err
	}
	p.AgentToken = formatRefreshToken(storeID, id, secret)
	return &p, nil
}

func (s *PrintService) loadPrinter(ctx context.Context, tx *sqlx.Tx, storeID, id uuid.UUID, p *Printer) error {
	err := tx.GetContext(ctx, p, `SELECT `+printerColumns+` FROM printers WHERE store_id = $1 AND id = $2 FOR UPDATE`, storeID, id)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrPrinterNotFound
	}
	return err
}

// Claim hands the agent its oldest due job: pending, or claimed but not acknowledged within the
// lease. Jobs past printMaxAttempts without an ack go dead. nil when there is nothing to print.
func (s *PrintService) Claim(ctx context.Context, token string) (*AgentJob, error) {
	var job *AgentJob
	err := s.agentTx(ctx, token, func(tx *sqlx.Tx, storeID uuid.UUID, p *Printer, now time.Time) error {
		var dead []struct {
			ID       uuid.UUID `db:"id"`
			Kind     string    `db:"kind"`
			Attempts int       `db:"attempts"`
		}
		if err := tx.SelectContext(ctx, &dead, `UPDATE print_jobs SET status = 'dead', claimed_at = NULL,
				last_error = COALESCE(last_error, 'agen tidak mengonfirmasi cetakan')
			WHERE store_id = $1 AND printer_id = $2 AND status = 'claimed' AND claimed_at < $3 AND attempts >= $4
			RETURNING id, kind, attempts`, storeID, p.ID, now.Add(-printClaimLease), printMaxAttempts); err != nil {
			return err
		}
		for _, d := range dead {
			if err := s.event(ctx, tx, storeID, "print_job.failed", d.ID, map[string]any{"printer_id": p.ID, "kind": d.Kind, "attempts": d.Attempts}, now); err != nil {
				return err
			}
		}
		var row struct {
			ID       uuid.UUID `db:"id"`
			Kind     string    `db:"kind"`
			Payload  []byte    `db:"payload"`
			Attempts int       `db:"attempts"`
		}
		err := tx.GetContext(ctx, &row, `UPDATE print_jobs SET status = 'claimed', claimed_at = $3, attempts = attempts + 1
			WHERE id = (
				SELECT id FROM print_jobs
				WHERE store_id = $1 AND printer_id = $2
				  AND ((status = 'pending' AND next_attempt_at <= $3) OR (status = 'claimed' AND claimed_at < $4))
				ORDER BY created_at FOR UPDATE SKIP LOCKED LIMIT 1)
			RETURNING id, kind, payload, attempts`, storeID, p.ID, now, now.Add(-printClaimLease))
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		job = &AgentJob{ID: row.ID, Kind: row.Kind, Format: p.Format, Payload: base64.StdEncoding.EncodeToString(row.Payload), Attempts: row.Attempts}
		return nil
	})
	return job, err
}

// Ack records the agent's result. A failure retries with a growing delay until printMaxAttempts.
// Acknowledging a job that is already printed is a no-op.
func (s *PrintService) Ack(ctx context.Context, token string, jobID uuid.UUID, printed bool, errMsg string) error {
	return s.agentTx(ctx, token, func(tx *sqlx.Tx, storeID uuid.UUID, p *Printer, now time.Time) error {
		var job struct {
			Status    string     `db:"status"`
			Kind      string     `db:"kind"`
			Attempts  int        `db:"attempts"`
			CreatedAt time.Time  `db:"created_at"`
			ClaimedAt *time.Time `db:"claimed_at"`
		}
		err := tx.GetContext(ctx, &job, `SELECT status, kind, attempts, created_at, claimed_at FROM print_jobs
			WHERE store_id = $1 AND printer_id = $2 AND id = $3 FOR UPDATE`, storeID, p.ID, jobID)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrPrintJobNotFound
		}
		if err != nil {
			return err
		}
		if job.Status == "printed" && printed {
			return nil
		}
		if job.Status != "claimed" {
			return ErrPrintJobNotFound
		}
		meta := map[string]any{"printer_id": p.ID, "kind": job.Kind, "attempts": job.Attempts}
		if printed {
			if _, err := tx.ExecContext(ctx, `UPDATE print_jobs SET status = 'printed', printed_at = $3, claimed_at = NULL
				WHERE store_id = $1 AND id = $2`, storeID, jobID, now); err != nil {
				return err
			}
			meta["latency_ms"] = now.Sub(job.CreatedAt).Milliseconds()
			return s.event(ctx, tx, storeID, "print_job.printed", jobID, meta, now)
		}
		errMsg = strings.TrimSpace(errMsg)
		if errMsg == "" {
			errMsg = "agen melaporkan gagal cetak"
		}
		if len(errMsg) > 500 {
			errMsg = errMsg[:500]
		}
		if job.Attempts >= printMaxAttempts {
			if _, err := tx.ExecContext(ctx, `UPDATE print_jobs SET status = 'dead', claimed_at = NULL, last_error = $3
				WHERE store_id = $1 AND id = $2`, storeID, jobID, errMsg); err != nil {
				return err
			}
			return s.event(ctx, tx, storeID, "print_job.failed", jobID, meta, now)
		}
		_, err = tx.ExecContext(ctx, `UPDATE print_jobs SET status = 'pending', claimed_at = NULL, last_error = $3, next_attempt_at = $4
			WHERE store_id = $1 AND id = $2`, storeID, jobID, errMsg, now.Add(time.Duration(job.Attempts)*10*time.Second))
		return err
	})
}

// agentTx authenticates a printer token inside the tenant transaction and records last_seen_at
// (at most once a minute).
func (s *PrintService) agentTx(ctx context.Context, token string, fn func(tx *sqlx.Tx, storeID uuid.UUID, p *Printer, now time.Time) error) error {
	storeID, printerID, secret, ok := parseRefreshToken(token)
	if !ok {
		return ErrPrinterTokenInvalid
	}
	return database.WithTenantTx(ctx, s.db, storeID, func(tx *sqlx.Tx) error {
		var row struct {
			Printer
			TokenHash   string `db:"agent_token_hash"`
			StoreActive bool   `db:"store_active"`
		}
		err := tx.GetContext(ctx, &row, `SELECT p.id, p.name, p.station, p.format, p.paper_width_mm, p.label_height_mm, p.copies,
				p.footer, p.is_active, p.last_seen_at, p.created_at, p.agent_token_hash, s.is_active AS store_active
			FROM printers p JOIN stores s ON s.id = p.store_id WHERE p.store_id = $1 AND p.id = $2`, storeID, printerID)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrPrinterTokenInvalid
		}
		if err != nil {
			return err
		}
		if !row.IsActive || !row.StoreActive || !constantTimeEqual(hashRefreshSecret(secret), row.TokenHash) {
			return ErrPrinterTokenInvalid
		}
		now := s.nowFunc().UTC()
		if _, err := tx.ExecContext(ctx, `UPDATE printers SET last_seen_at = $3 WHERE store_id = $1 AND id = $2
			AND (last_seen_at IS NULL OR last_seen_at < $3::timestamptz - INTERVAL '1 minute')`, storeID, printerID, now); err != nil {
			return err
		}
		return fn(tx, storeID, &row.Printer, now)
	})
}

// Reprint queues a new copy of an order's ticket, checker slip or labels (all rounds).
func (s *PrintService) Reprint(ctx context.Context, actor *AuthenticatedStaff, orderID uuid.UUID, kind string) (int, error) {
	if actor == nil {
		return 0, ErrInvalidSession
	}
	if !actor.Can(rbac.OrderPOSCreate) {
		return 0, ErrForbidden
	}
	if kind != "ticket" && kind != "checker" && kind != "label" {
		return 0, &ValidationError{Fields: map[string]string{"kind": "harus ticket, checker, atau label"}}
	}
	storeID, userID := actor.Staff.Store.ID, actor.Staff.UserID
	var n int
	err := database.WithTenantTx(ctx, s.db, storeID, func(tx *sqlx.Tx) error {
		order, _, err := s.orders.GetOrderByIDReadOnly(ctx, tx, storeID, orderID)
		if errors.Is(err, postgres.ErrOrderNotFound) {
			return ErrOrderNotFound
		}
		if err != nil {
			return err
		}
		if n, err = enqueuePrintJobsTx(ctx, tx, s.orders, order, printScope{allRounds: true, kind: kind, reprintBy: &userID}, s.nowFunc().UTC()); err != nil {
			return err
		}
		if n == 0 {
			return ErrNoPrinter
		}
		return s.audit(ctx, tx, actor, "PRINT_REPRINT", "Cetak ulang "+kind, orderID, map[string]any{"kind": kind, "jobs": n})
	})
	return n, err
}

// printScope selects what enqueuePrintJobsTx prints. stations limits station tickets and labels to
// the KDS stations just created; nil means every station.
type printScope struct {
	roundID   *uuid.UUID
	allRounds bool
	stations  map[string]bool
	kind      string
	reprintBy *uuid.UUID
}

var stationTitles = map[string]string{"bar": "BAR", "kitchen": "DAPUR", "checker": "CHECKER"}

// enqueuePrintJobsTx renders and queues one job per active printer that has lines to print. Stores
// without printers pay one indexed query.
func enqueuePrintJobsTx(ctx context.Context, tx *sqlx.Tx, orders *postgres.OrderRepository, order *domain.Order, sc printScope, now time.Time) (int, error) {
	var printers []Printer
	if err := tx.SelectContext(ctx, &printers, `SELECT `+printerColumns+` FROM printers WHERE store_id = $1 AND is_active`, order.StoreID); err != nil {
		return 0, fmt.Errorf("failed to load printers: %w", err)
	}
	if len(printers) == 0 {
		return 0, nil
	}
	all, err := receiptLinesTx(ctx, tx, order.StoreID, order.ID)
	if err != nil {
		return 0, err
	}
	var lines []ReceiptLine
	for _, l := range all {
		if sc.allRounds || (l.roundID == nil && sc.roundID == nil) || (l.roundID != nil && sc.roundID != nil && *l.roundID == *sc.roundID) {
			lines = append(lines, l)
		}
	}
	store, err := orders.GetStoreForOrder(ctx, tx, order.StoreID)
	if err != nil {
		return 0, err
	}
	base := PrintTicket{QueueNumber: order.QueueNumber, TableNumber: order.TableNumber, OrderNumber: order.OrderNumber,
		CustomerName: order.CustomerName, LocalTime: formatStoreTime(now, store.Timezone), Reprint: sc.reprintBy != nil}
	if sc.roundID != nil {
		if err := tx.GetContext(ctx, &base.RoundNumber, `SELECT round_number FROM order_rounds WHERE store_id = $1 AND id = $2`, order.StoreID, *sc.roundID); err != nil {
			return 0, fmt.Errorf("failed to load round number: %w", err)
		}
	}
	pick := func(station string) []ReceiptLine {
		var out []ReceiptLine
		for _, l := range lines {
			if l.station == station {
				out = append(out, l)
			}
		}
		return out
	}
	n := 0
	for _, p := range printers {
		kind, sel := "ticket", []ReceiptLine(nil)
		switch p.Station {
		case "bar", "kitchen":
			if sc.stations == nil || sc.stations[p.Station] {
				sel = pick(p.Station)
			}
		case "checker":
			kind, sel = "checker", lines
		case "label":
			if sc.stations == nil || sc.stations["bar"] {
				kind, sel = "label", pick("bar")
			}
		}
		if len(sel) == 0 || (sc.kind != "" && sc.kind != kind) {
			continue
		}
		t := base
		t.Title, t.Lines = stationTitles[p.Station], sel
		var payload []byte
		if p.Format == "tspl" {
			payload, err = RenderLabels(&t, p.PaperWidthMM, *p.LabelHeightMM, p.Copies)
		} else {
			payload, err = RenderStationTicket(&t, p.PaperWidthMM, p.Copies, p.Footer)
		}
		if err != nil {
			// A bad printer setting must never block a payment or a round: skip it and say why.
			log.Printf("[PRINT] store_id=%s printer_id=%s render error: %v", order.StoreID, p.ID, err)
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO print_jobs (store_id, printer_id, order_id, round_id, kind, payload,
			next_attempt_at, reprinted_by_user_id, created_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $7)`,
			order.StoreID, p.ID, order.ID, sc.roundID, kind, payload, now, sc.reprintBy); err != nil {
			return 0, fmt.Errorf("failed to queue print job: %w", err)
		}
		n++
	}
	return n, nil
}

func (s *PrintService) event(ctx context.Context, tx *sqlx.Tx, storeID uuid.UUID, event string, jobID uuid.UUID, meta map[string]any, now time.Time) error {
	payload, _ := json.Marshal(meta)
	return s.orders.InsertOutboxEvent(ctx, tx, &domain.OutboxEvent{
		ID: uuid.New(), StoreID: storeID, EventType: event, AggregateType: "print_job",
		AggregateID: jobID.String(), Payload: string(payload), CreatedAt: now,
	})
}

func (s *PrintService) audit(ctx context.Context, tx *sqlx.Tx, actor *AuthenticatedStaff, action, reason string, ref uuid.UUID, meta map[string]any) error {
	payload, _ := json.Marshal(meta)
	actorID := actor.Staff.UserID
	return postgres.InsertAuditLog(ctx, tx, &domain.AuditLog{
		StoreID: actor.Staff.Store.ID, ActorID: &actorID, ActorName: actor.Staff.FullName, Action: action,
		Reason: reason, ReferenceID: ref.String(), Metadata: string(payload), CreatedAt: s.nowFunc().UTC(),
	})
}
