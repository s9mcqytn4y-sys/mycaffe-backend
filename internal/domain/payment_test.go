package domain_test

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
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
	// Subtotal = 80,000, Tax = 8,000, Service = 4,000 -> Total = 92,000
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
				TotalAmount:         45000, // Sum = 91,000 != 92,000
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
					// item1ID already had 2 of 2 allocated in split #1
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

func TestDomainDBTagsMatchInitSchemaSQL(t *testing.T) {
	migrationPath := filepath.Join("..", "..", "db", "migrations", "20261028000001_init_schema.up.sql")
	rawSQL, err := os.ReadFile(migrationPath)
	if err != nil {
		t.Fatalf("failed to read migration file %s: %v", migrationPath, err)
	}
	sqlText := string(rawSQL)

	tableStructs := []struct {
		tableName string
		model     any
	}{
		{"orders", domain.Order{}},
		{"order_items", domain.OrderItem{}},
		{"order_item_options", domain.OrderItemOption{}},
		{"modifier_groups", domain.ModifierGroup{}},
		{"modifier_options", domain.ModifierOption{}},
		{"payment_splits", domain.PaymentSplit{}},
		{"payment_split_items", domain.PaymentSplitItem{}},
		{"payments", domain.Payment{}},
		{"payment_notification_logs", domain.PaymentNotificationLog{}},
	}

	for _, ts := range tableStructs {
		t.Run(ts.tableName, func(t *testing.T) {
			tableBlock := extractCreateTableBlock(t, sqlText, ts.tableName)
			rt := reflect.TypeOf(ts.model)
			for i := 0; i < rt.NumField(); i++ {
				field := rt.Field(i)
				dbTag := field.Tag.Get("db")
				if dbTag == "" || dbTag == "-" {
					continue
				}
				colName := strings.Split(dbTag, ",")[0]
				colPattern := regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(colName) + `\b`)
				if !colPattern.MatchString(tableBlock) {
					t.Errorf("struct %s field %s has db tag %q, but column %q was not found in CREATE TABLE %s",
						rt.Name(), field.Name, dbTag, colName, ts.tableName)
				}
			}
		})
	}
}

func extractCreateTableBlock(t *testing.T, sqlText, tableName string) string {
	t.Helper()
	header := "CREATE TABLE IF NOT EXISTS " + tableName + " ("
	startIdx := strings.Index(sqlText, header)
	if startIdx == -1 {
		t.Fatalf("CREATE TABLE IF NOT EXISTS %s not found in migration SQL", tableName)
	}
	rest := sqlText[startIdx+len(header):]
	endIdx := strings.Index(rest, "\n);")
	if endIdx == -1 {
		t.Fatalf("closing ); for table %s not found in migration SQL", tableName)
	}
	return rest[:endIdx]
}
