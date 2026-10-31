package domain_test

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/domain"
)

func TestValidateSplitAllocations(t *testing.T) {
	item1ID := uuid.New()
	item2ID := uuid.New()

	orderItems := []domain.OrderItem{
		{ID: item1ID, Quantity: 2, SubtotalAmount: 50000},
		{ID: item2ID, Quantity: 1, SubtotalAmount: 30000},
	}
	orderTotal := int64(92000)

	t.Run("valid by_item split allocation passes", func(t *testing.T) {
		splits := []domain.PaymentSplit{
			{
				SplitNumber:         1,
				SplitMode:           domain.SplitModeByItem,
				SubtotalAmount:      50000,
				TaxAmount:           5000,
				ServiceChargeAmount: 2500,
				TotalAmount:         57500,
				PaidAmount:          57500,
				Status:              domain.SplitBillStatusPaid,
				Items: []domain.PaymentSplitItem{
					{OrderItemID: item1ID, Quantity: 2, AllocatedAmount: 50000},
				},
			},
			{
				SplitNumber:         2,
				SplitMode:           domain.SplitModeByItem,
				SubtotalAmount:      30000,
				TaxAmount:           3000,
				ServiceChargeAmount: 1500,
				TotalAmount:         34500,
				PaidAmount:          0,
				Status:              domain.SplitBillStatusUnpaid,
				Items: []domain.PaymentSplitItem{
					{OrderItemID: item2ID, Quantity: 1, AllocatedAmount: 30000},
				},
			},
		}
		if err := domain.ValidateSplitAllocations(orderTotal, orderItems, splits); err != nil {
			t.Fatalf("expected valid split allocations to pass, got error: %v", err)
		}
	})

	t.Run("mismatched split total sum fails", func(t *testing.T) {
		splits := []domain.PaymentSplit{
			{
				SplitNumber:         1,
				SplitMode:           domain.SplitModeEqual,
				SubtotalAmount:      40000,
				TaxAmount:           4000,
				ServiceChargeAmount: 2000,
				TotalAmount:         46000,
			},
			{
				SplitNumber:         2,
				SplitMode:           domain.SplitModeEqual,
				SubtotalAmount:      40000,
				TaxAmount:           4000,
				ServiceChargeAmount: 1000,
				TotalAmount:         45000,
			},
		}
		if err := domain.ValidateSplitAllocations(orderTotal, orderItems, splits); err == nil {
			t.Fatal("expected error when split total sum does not match orderTotal")
		}
	})

	t.Run("over-allocated item quantity across splits fails", func(t *testing.T) {
		splits := []domain.PaymentSplit{
			{
				SplitNumber:         1,
				SplitMode:           domain.SplitModeByItem,
				SubtotalAmount:      50000,
				TaxAmount:           5000,
				ServiceChargeAmount: 2500,
				TotalAmount:         57500,
				Items: []domain.PaymentSplitItem{
					{OrderItemID: item1ID, Quantity: 2, AllocatedAmount: 50000},
				},
			},
			{
				SplitNumber:         2,
				SplitMode:           domain.SplitModeByItem,
				SubtotalAmount:      30000,
				TaxAmount:           3000,
				ServiceChargeAmount: 1500,
				TotalAmount:         34500,
				Items: []domain.PaymentSplitItem{
					{OrderItemID: item1ID, Quantity: 1, AllocatedAmount: 30000},
				},
			},
		}
		if err := domain.ValidateSplitAllocations(orderTotal, orderItems, splits); err == nil {
			t.Fatal("expected error when item quantity is over-allocated across splits")
		}
	})

	t.Run("duplicate split_number fails", func(t *testing.T) {
		splits := []domain.PaymentSplit{
			{
				SplitNumber:    1,
				SplitMode:      domain.SplitModeCustomAmount,
				SubtotalAmount: 46000,
				TotalAmount:    46000,
			},
			{
				SplitNumber:    1,
				SplitMode:      domain.SplitModeCustomAmount,
				SubtotalAmount: 46000,
				TotalAmount:    46000,
			},
		}
		if err := domain.ValidateSplitAllocations(orderTotal, orderItems, splits); err == nil {
			t.Fatal("expected error on duplicate split_number")
		}
	})
}

func TestCanTransitionOrderStatus(t *testing.T) {
	if !domain.CanTransitionOrderStatus(domain.OrderStatusPendingPayment, domain.OrderStatusQueued) {
		t.Fatal("expected pending_payment -> queued to be allowed")
	}
	if !domain.CanTransitionOrderStatus(domain.OrderStatusReady, domain.OrderStatusServed) {
		t.Fatal("expected ready -> served to be allowed")
	}
	if !domain.CanTransitionOrderStatus(domain.OrderStatusServed, domain.OrderStatusCompleted) {
		t.Fatal("expected served -> completed to be allowed")
	}
	if domain.CanTransitionOrderStatus(domain.OrderStatusCompleted, domain.OrderStatusPreparing) {
		t.Fatal("expected completed -> preparing regression to be rejected")
	}
	if domain.CanTransitionOrderStatus(domain.OrderStatusPreparing, domain.OrderStatusCancelled) {
		t.Fatal("expected preparing -> cancelled without override to be rejected")
	}
}

func TestDomainDBTagsMatchMigrationChainSQL(t *testing.T) {
	sqlText := loadFullMigrationChainSQL(t)

	tableStructs := []struct {
		tableName string
		model     any
	}{
		{"stores", domain.Store{}},
		{"users", domain.User{}},
		{"categories", domain.Category{}},
		{"products", domain.Product{}},
		{"product_modifier_groups", domain.ProductModifierGroup{}},
		{"option_modifier_groups", domain.OptionModifierGroup{}},
		{"store_daily_sequences", domain.StoreDailySequence{}},
		{"orders", domain.Order{}},
		{"order_items", domain.OrderItem{}},
		{"order_item_options", domain.OrderItemOption{}},
		{"modifier_groups", domain.ModifierGroup{}},
		{"modifier_options", domain.ModifierOption{}},
		{"payment_splits", domain.PaymentSplit{}},
		{"payment_split_items", domain.PaymentSplitItem{}},
		{"payments", domain.Payment{}},
		{"payment_notification_logs", domain.PaymentNotificationLog{}},
		{"outbox_events", domain.OutboxEvent{}},
	}

	for _, ts := range tableStructs {
		t.Run(ts.tableName, func(t *testing.T) {
			cols := extractTableColumnsAcrossMigrations(t, sqlText, ts.tableName)
			rt := reflect.TypeOf(ts.model)
			for i := 0; i < rt.NumField(); i++ {
				field := rt.Field(i)
				dbTag := field.Tag.Get("db")
				if dbTag == "" || dbTag == "-" {
					continue
				}
				colName := strings.Split(dbTag, ",")[0]
				if !cols[colName] {
					t.Errorf("struct %s field %s has db tag %q, but column %q was not found in migration chain for table %s",
						rt.Name(), field.Name, dbTag, colName, ts.tableName)
				}
			}
		})
	}
}

func TestDomainStatusesMatchSQLCheckConstraints(t *testing.T) {
	sqlText := loadFullMigrationChainSQL(t)

	checks := []struct {
		constraintName string
		goValues       []string
	}{
		{"chk_users_role", toStrings(domain.ValidUserRoles())},
		{"chk_orders_order_type", toStrings(domain.ValidOrderTypes())},
		{"chk_orders_billing_mode", toStrings(domain.ValidBillingModes())},
		{"chk_orders_status", toStrings(domain.ValidOrderStatuses())},
		{"chk_orders_payment_status", toStrings(domain.ValidOrderPaymentStatuses())},
		{"chk_orders_stock_status", toStrings(domain.ValidOrderStockStatuses())},
		{"chk_payment_splits_mode", toStrings(domain.ValidSplitModes())},
		{"chk_payment_splits_status", toStrings(domain.ValidSplitBillStatuses())},
		{"chk_payments_method", toStrings(domain.ValidPaymentMethods())},
		{"chk_payments_status", toStrings(domain.ValidPaymentAttemptStatuses())},
	}

	for _, tc := range checks {
		t.Run(tc.constraintName, func(t *testing.T) {
			sqlValues := extractCheckInValues(t, sqlText, tc.constraintName)
			sort.Strings(sqlValues)
			goSorted := slices.Clone(tc.goValues)
			sort.Strings(goSorted)

			if !slices.Equal(sqlValues, goSorted) {
				t.Errorf("constraint %s mismatch:\n  SQL CHECK: %v\n  Go Domain: %v",
					tc.constraintName, sqlValues, goSorted)
			}
		})
	}
}

func toStrings[T ~string](items []T) []string {
	out := make([]string, len(items))
	for i, v := range items {
		out[i] = string(v)
	}
	return out
}

func loadFullMigrationChainSQL(t *testing.T) string {
	t.Helper()
	migrationsDir := filepath.Join("..", "..", "db", "migrations")
	entries, err := os.ReadDir(migrationsDir)
	if err != nil {
		t.Fatalf("failed to read migrations directory %s: %v", migrationsDir, err)
	}
	var upFiles []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".up.sql") {
			upFiles = append(upFiles, entry.Name())
		}
	}
	sort.Strings(upFiles)
	if len(upFiles) == 0 {
		t.Fatal("no *.up.sql files found in db/migrations")
	}

	var b strings.Builder
	for _, fileName := range upFiles {
		raw, err := os.ReadFile(filepath.Join(migrationsDir, fileName))
		if err != nil {
			t.Fatalf("failed to read %s: %v", fileName, err)
		}
		b.Write(raw)
		b.WriteString("\n")
	}
	return b.String()
}

func extractTableColumnsAcrossMigrations(t *testing.T, sqlText, tableName string) map[string]bool {
	t.Helper()
	cols := make(map[string]bool)

	header := "CREATE TABLE IF NOT EXISTS " + tableName + " ("
	startIdx := strings.Index(sqlText, header)
	if startIdx == -1 {
		t.Fatalf("CREATE TABLE IF NOT EXISTS %s not found in migration chain", tableName)
	}
	rest := sqlText[startIdx+len(header):]
	endIdx := strings.Index(rest, "\n);")
	if endIdx == -1 {
		t.Fatalf("closing ); for table %s not found in migration chain", tableName)
	}
	createBlock := rest[:endIdx]

	lineRe := regexp.MustCompile(`(?m)^\s*([a-z_][a-z0-9_]*)\s+`)
	for _, match := range lineRe.FindAllStringSubmatch(createBlock, -1) {
		word := match[1]
		if word != "constraint" && word != "primary" && word != "unique" && word != "foreign" {
			cols[word] = true
		}
	}

	alterPattern := `(?is)ALTER\s+TABLE\s+` + regexp.QuoteMeta(tableName) + `\s+ADD\s+COLUMN(?:\s+IF\s+NOT\s+EXISTS)?\s+([a-z_][a-z0-9_]*)\b`
	alterRe := regexp.MustCompile(alterPattern)
	for _, match := range alterRe.FindAllStringSubmatch(sqlText, -1) {
		cols[match[1]] = true
	}

	return cols
}

func extractCheckInValues(t *testing.T, sqlText, constraintName string) []string {
	t.Helper()
	pattern := `CONSTRAINT\s+` + regexp.QuoteMeta(constraintName) + `\s+CHECK\s*\([a-z_]+\s+IN\s*\(([^)]+)\)\)`
	re := regexp.MustCompile(pattern)
	matches := re.FindAllStringSubmatch(sqlText, -1)
	if len(matches) == 0 {
		t.Fatalf("CONSTRAINT %s CHECK (... IN (...)) not found in migration chain", constraintName)
	}
	lastMatch := matches[len(matches)-1][1]
	quoteRe := regexp.MustCompile(`'([^']+)'`)
	var vals []string
	for _, m := range quoteRe.FindAllStringSubmatch(lastMatch, -1) {
		vals = append(vals, m[1])
	}
	return vals
}


func TestStockReservationMigrationUsesSafeLegacyDefault(t *testing.T) {
	migrationPath := filepath.Join("..", "..", "db", "migrations", "20261030000003_add_orders_stock_reservation.up.sql")
	raw, err := os.ReadFile(migrationPath)
	if err != nil {
		t.Fatalf("failed to read stock reservation migration: %v", err)
	}

	pattern := regexp.MustCompile(`(?i)ADD COLUMN IF NOT EXISTS stock_status\\s+VARCHAR\\(20\\)\\s+NOT NULL\\s+DEFAULT\\s+'not_tracked'`)
	if !pattern.Match(raw) {
		t.Fatal("legacy orders must default to stock_status='not_tracked'; only newly created orders may explicitly reserve stock")
	}
}
