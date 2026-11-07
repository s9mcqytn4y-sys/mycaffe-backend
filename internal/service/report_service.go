package service

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/database"
)

// Owner reports (B2). Read-only queries over snapshots. A business day is the calendar day in the
// store time zone (no cutoff yet; PRD v2 PD5 proposes 04:00). Order-based figures (day, hour,
// category, item) use the order's creation time; payment-based figures (method, cash) use paid_at,
// so a tab opened before midnight and paid after it lands on different days in the two views.

const maxReportDays = 92

const (
	ReportGroupDay      = "day"
	ReportGroupHour     = "hour"
	ReportGroupMethod   = "method"
	ReportGroupCategory = "category"
	ReportGroupItem     = "item"
)

// reportOrderFilter selects orders that took money; voided (cancelled, never paid) orders are excluded.
const reportOrderFilter = `o.store_id = $1 AND o.created_at >= $2 AND o.created_at < $3
	AND o.payment_status IN ('paid', 'partially_refunded', 'refunded')`

type ReportRange struct {
	From, To   string // inclusive YYYY-MM-DD in the store time zone
	start, end time.Time
	tz         string
}

// SalesRow is one group of the sales report. Day and hour rows carry order totals; method rows carry
// payments; category and item rows carry quantities and line amounts (order-level discount, tax and
// service are not split per line).
type SalesRow struct {
	Key           string `json:"key" db:"key"`
	Orders        int64  `json:"orders,omitempty" db:"orders"`
	Payments      int64  `json:"payments,omitempty" db:"payments"`
	Quantity      int64  `json:"quantity,omitempty" db:"quantity"`
	GrossSales    int64  `json:"gross_sales" db:"gross_sales"`
	Discount      int64  `json:"discount_amount" db:"discount"`
	Tax           int64  `json:"tax_amount" db:"tax"`
	ServiceCharge int64  `json:"service_charge_amount" db:"service"`
	Total         int64  `json:"total_amount" db:"total"`
	Refunded      int64  `json:"refunded_amount" db:"refunded"`
	Net           int64  `json:"net_amount" db:"net"`
}

type SalesReport struct {
	From  string     `json:"from"`
	To    string     `json:"to"`
	Group string     `json:"group"`
	Rows  []SalesRow `json:"rows"`
}

type ReportSummary struct {
	From          string        `json:"from"`
	To            string        `json:"to"`
	Orders        int64         `json:"orders"`
	GrossSales    int64         `json:"gross_sales"`
	Discount      int64         `json:"discount_amount"`
	Tax           int64         `json:"tax_amount"`
	ServiceCharge int64         `json:"service_charge_amount"`
	Total         int64         `json:"total_amount"`
	Refunded      int64         `json:"refunded_amount"`
	Net           int64         `json:"net_amount"`
	VoidCount     int64         `json:"void_count"`
	VoidAmount    int64         `json:"void_amount"`
	RefundsBy     []MethodTotal `json:"refunds_by_method"`
	CashSales     int64         `json:"cash_sales"`
	CashRefunds   int64         `json:"cash_refunds"`
	CashNet       int64         `json:"cash_net"`
	// CashRounding is the net cash rounding (V11) inside CashSales; it is not part of Total.
	CashRounding int64 `json:"cash_rounding"`
	// OutletExpenses are outlet cash fund expenses in the range (V13), outside the drawers.
	OutletExpenses int64 `json:"outlet_expenses"`
}

type MethodTotal struct {
	Method string `json:"method" db:"method"`
	Count  int64  `json:"count" db:"count"`
	Amount int64  `json:"amount" db:"amount"`
}

type ReportService struct {
	db *sqlx.DB
}

func NewReportService(db *sqlx.DB) *ReportService { return &ReportService{db: db} }

// parseRange validates from/to (default today) against the store time zone and the 92-day cap.
// Order lists (GET /pos/orders) use the same window.
func parseRange(ctx context.Context, tx *sqlx.Tx, storeID uuid.UUID, from, to string) (*ReportRange, error) {
	var tz string
	if err := tx.GetContext(ctx, &tz, `SELECT timezone FROM stores WHERE id = $1`, storeID); err != nil {
		return nil, fmt.Errorf("failed to load store timezone: %w", err)
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		loc = time.FixedZone("WIB", 7*3600)
	}
	today := time.Now().In(loc).Format(time.DateOnly)
	if from == "" {
		from = today
	}
	if to == "" {
		to = from
	}
	fields := map[string]string{}
	start, err1 := time.ParseInLocation(time.DateOnly, from, loc)
	end, err2 := time.ParseInLocation(time.DateOnly, to, loc)
	switch {
	case err1 != nil:
		fields["from"] = "format tanggal YYYY-MM-DD"
	case err2 != nil:
		fields["to"] = "format tanggal YYYY-MM-DD"
	case end.Before(start):
		fields["to"] = "tidak boleh sebelum from"
	case end.Sub(start) >= maxReportDays*24*time.Hour:
		fields["to"] = fmt.Sprintf("rentang maksimal %d hari", maxReportDays)
	}
	if len(fields) > 0 {
		return nil, &ValidationError{Fields: fields}
	}
	return &ReportRange{From: from, To: to, start: start, end: end.AddDate(0, 0, 1), tz: loc.String()}, nil
}

// Sales returns one row per group key, ordered by key.
func (s *ReportService) Sales(ctx context.Context, storeID uuid.UUID, from, to, group string) (*SalesReport, error) {
	var query string
	switch group {
	case ReportGroupDay, ReportGroupHour:
		bucket := `to_char(o.created_at AT TIME ZONE $4::text, 'YYYY-MM-DD')`
		if group == ReportGroupHour {
			bucket = `to_char(o.created_at AT TIME ZONE $4::text, 'HH24')`
		}
		query = `SELECT ` + bucket + ` AS key, COUNT(*) AS orders, SUM(o.subtotal_amount) AS gross_sales,
			SUM(o.discount_amount) AS discount, SUM(o.tax_amount) AS tax, SUM(o.service_charge_amount) AS service,
			SUM(o.total_amount) AS total, SUM(o.refunded_amount) AS refunded, SUM(o.total_amount - o.refunded_amount) AS net
			FROM orders o WHERE ` + reportOrderFilter + ` GROUP BY 1 ORDER BY 1`
	case ReportGroupCategory, ReportGroupItem:
		key := `COALESCE(NULLIF(oi.snapshot_category_name, ''), 'Tanpa kategori')`
		if group == ReportGroupItem {
			key = `oi.snapshot_name || CASE WHEN oi.snapshot_variant_name <> '' THEN ' (' || oi.snapshot_variant_name || ')' ELSE '' END`
		}
		query = `SELECT ` + key + ` AS key, COUNT(DISTINCT o.id) AS orders, SUM(oi.quantity) AS quantity,
			SUM(oi.subtotal_amount) AS gross_sales, 0 AS discount, 0 AS tax, 0 AS service,
			SUM(oi.subtotal_amount) AS total, 0 AS refunded, SUM(oi.subtotal_amount) AS net
			FROM orders o JOIN order_items oi ON oi.store_id = o.store_id AND oi.order_id = o.id
			WHERE ` + reportOrderFilter + ` AND $4::text <> '' GROUP BY 1 ORDER BY 1`
	case ReportGroupMethod:
		query = `SELECT p.method AS key, COUNT(*) AS payments, SUM(p.amount) AS gross_sales, 0 AS discount, 0 AS tax,
			0 AS service, SUM(p.amount) AS total, SUM(p.refunded_amount) AS refunded, SUM(p.amount - p.refunded_amount) AS net
			FROM payments p WHERE p.store_id = $1 AND p.paid_at >= $2 AND p.paid_at < $3 AND $4::text <> ''
			AND p.status IN ('paid', 'partially_refunded', 'refunded') GROUP BY 1 ORDER BY 1`
	default:
		return nil, &ValidationError{Fields: map[string]string{"group": "harus day, hour, method, category, atau item"}}
	}

	var report *SalesReport
	err := database.WithTenantTx(ctx, s.db, storeID, func(tx *sqlx.Tx) error {
		rng, err := parseRange(ctx, tx, storeID, from, to)
		if err != nil {
			return err
		}
		rows := []SalesRow{}
		if err := tx.SelectContext(ctx, &rows, query, storeID, rng.start, rng.end, rng.tz); err != nil {
			return fmt.Errorf("failed to run %s sales report: %w", group, err)
		}
		report = &SalesReport{From: rng.From, To: rng.To, Group: group, Rows: rows}
		return nil
	})
	return report, err
}

// Summary returns the range totals, voids, refunds by method and the cash figures that match the sum of
// the Z-reports (cash sales minus cash refunds from cash_movements).
func (s *ReportService) Summary(ctx context.Context, storeID uuid.UUID, from, to string) (*ReportSummary, error) {
	var sum *ReportSummary
	err := database.WithTenantTx(ctx, s.db, storeID, func(tx *sqlx.Tx) error {
		rng, err := parseRange(ctx, tx, storeID, from, to)
		if err != nil {
			return err
		}
		sum = &ReportSummary{From: rng.From, To: rng.To, RefundsBy: []MethodTotal{}}
		if err := tx.QueryRowxContext(ctx, `SELECT COUNT(*), COALESCE(SUM(o.subtotal_amount), 0), COALESCE(SUM(o.discount_amount), 0),
			COALESCE(SUM(o.tax_amount), 0), COALESCE(SUM(o.service_charge_amount), 0), COALESCE(SUM(o.total_amount), 0),
			COALESCE(SUM(o.refunded_amount), 0) FROM orders o WHERE `+reportOrderFilter, storeID, rng.start, rng.end).
			Scan(&sum.Orders, &sum.GrossSales, &sum.Discount, &sum.Tax, &sum.ServiceCharge, &sum.Total, &sum.Refunded); err != nil {
			return fmt.Errorf("failed to sum orders: %w", err)
		}
		sum.Net = sum.Total - sum.Refunded
		// VOID_ORDER audits reference the order number; voids count by the time they happened.
		if err := tx.QueryRowxContext(ctx, `SELECT COUNT(*), COALESCE(SUM(o.total_amount), 0) FROM audit_logs a
			JOIN orders o ON o.store_id = a.store_id AND o.order_number = a.reference_id
			WHERE a.store_id = $1 AND a.action = 'VOID_ORDER' AND a.created_at >= $2 AND a.created_at < $3`,
			storeID, rng.start, rng.end).Scan(&sum.VoidCount, &sum.VoidAmount); err != nil {
			return fmt.Errorf("failed to sum voids: %w", err)
		}
		if err := tx.SelectContext(ctx, &sum.RefundsBy, `SELECT p.method, COUNT(*) AS count, SUM(p.refunded_amount) AS amount FROM audit_logs a
			JOIN payments p ON p.store_id = a.store_id AND p.id::text = a.reference_id
			WHERE a.store_id = $1 AND a.action = 'REFUND' AND a.created_at >= $2 AND a.created_at < $3
			GROUP BY p.method ORDER BY p.method`, storeID, rng.start, rng.end); err != nil {
			return fmt.Errorf("failed to sum refunds: %w", err)
		}
		if err := tx.QueryRowxContext(ctx, `SELECT
			COALESCE(SUM(amount) FILTER (WHERE movement_type = 'sale'), 0),
			COALESCE(SUM(amount) FILTER (WHERE movement_type = 'refund'), 0)
			FROM cash_movements WHERE store_id = $1 AND created_at >= $2 AND created_at < $3`,
			storeID, rng.start, rng.end).Scan(&sum.CashSales, &sum.CashRefunds); err != nil {
			return fmt.Errorf("failed to sum cash movements: %w", err)
		}
		sum.CashNet = sum.CashSales - sum.CashRefunds
		if err := tx.GetContext(ctx, &sum.CashRounding, `SELECT COALESCE(SUM(rounding_amount), 0) FROM payments
			WHERE store_id = $1 AND paid_at >= $2 AND paid_at < $3`, storeID, rng.start, rng.end); err != nil {
			return fmt.Errorf("failed to sum cash rounding: %w", err)
		}
		if err := tx.GetContext(ctx, &sum.OutletExpenses, `SELECT COALESCE(SUM(amount), 0) FROM outlet_cash_movements
			WHERE store_id = $1 AND movement_type = 'expense' AND created_at >= $2 AND created_at < $3`, storeID, rng.start, rng.end); err != nil {
			return fmt.Errorf("failed to sum outlet expenses: %w", err)
		}
		return nil
	})
	return sum, err
}

// GrossProfitRow is one menu item's revenue (line subtotal after promotions, before the cashier discount and
// refunds, like the item sales report) against the COGS stored at payment (X10).
type GrossProfitRow struct {
	Key           string `json:"key" db:"key"`
	Quantity      int64  `json:"quantity" db:"quantity"`
	Revenue       int64  `json:"revenue" db:"revenue"`
	COGS          int64  `json:"cogs" db:"cogs"`
	GrossProfit   int64  `json:"gross_profit" db:"gross_profit"`
	MarginBPS     int64  `json:"margin_bps" db:"margin_bps"`
	LinesNoCOGS   int64  `json:"lines_without_cogs" db:"lines_without_cogs"`
	RevenueNoCOGS int64  `json:"revenue_without_cogs" db:"revenue_without_cogs"`
}

type GrossProfitReport struct {
	From        string           `json:"from"`
	To          string           `json:"to"`
	Revenue     int64            `json:"revenue"`
	COGS        int64            `json:"cogs"`
	GrossProfit int64            `json:"gross_profit"`
	MarginBPS   int64            `json:"margin_bps"`
	Rows        []GrossProfitRow `json:"rows"`
}

// GrossProfit groups paid order lines by menu item. Lines paid before ingredient costing existed have no COGS;
// they count as zero cost and are reported per row (lines_without_cogs) so an inflated margin is visible.
func (s *ReportService) GrossProfit(ctx context.Context, storeID uuid.UUID, from, to string) (*GrossProfitReport, error) {
	var report *GrossProfitReport
	err := database.WithTenantTx(ctx, s.db, storeID, func(tx *sqlx.Tx) error {
		rng, err := parseRange(ctx, tx, storeID, from, to)
		if err != nil {
			return err
		}
		rows := []GrossProfitRow{}
		if err := tx.SelectContext(ctx, &rows, `SELECT oi.snapshot_name || CASE WHEN oi.snapshot_variant_name <> '' THEN ' (' || oi.snapshot_variant_name || ')' ELSE '' END AS key,
			SUM(oi.quantity) AS quantity, SUM(oi.subtotal_amount) AS revenue, COALESCE(SUM(oi.cogs_amount), 0) AS cogs,
			SUM(oi.subtotal_amount) - COALESCE(SUM(oi.cogs_amount), 0) AS gross_profit, 0 AS margin_bps,
			COUNT(*) FILTER (WHERE oi.cogs_amount IS NULL) AS lines_without_cogs,
			COALESCE(SUM(oi.subtotal_amount) FILTER (WHERE oi.cogs_amount IS NULL), 0) AS revenue_without_cogs
			FROM orders o JOIN order_items oi ON oi.store_id = o.store_id AND oi.order_id = o.id
			WHERE `+reportOrderFilter+` GROUP BY 1 ORDER BY 4 DESC, 1`, storeID, rng.start, rng.end); err != nil {
			return fmt.Errorf("failed to run gross profit report: %w", err)
		}
		report = &GrossProfitReport{From: rng.From, To: rng.To, Rows: rows}
		for i := range rows {
			rows[i].MarginBPS = marginBPS(rows[i].GrossProfit, rows[i].Revenue)
			report.Revenue += rows[i].Revenue
			report.COGS += rows[i].COGS
		}
		report.GrossProfit = report.Revenue - report.COGS
		report.MarginBPS = marginBPS(report.GrossProfit, report.Revenue)
		return nil
	})
	return report, err
}

// marginBPS is profit / revenue in basis points, rounded half up away from zero; 0 when nothing was sold.
func marginBPS(profit, revenue int64) int64 {
	if revenue <= 0 {
		return 0
	}
	if profit < 0 {
		return -roundHalfUpDiv(-profit*10_000, revenue)
	}
	return roundHalfUpDiv(profit*10_000, revenue)
}
