package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/config"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/domain"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/handler"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/pkg/database"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/pkg/rbac"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/repository/postgres"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/service"
)

// TestInvoicesIntegration covers v0.11.1 PR 11.9 (X18): business customers, charging orders to an account,
// billing them on one invoice, instalments, the PDF, and the refund guard.
func TestInvoicesIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_APP_DATABASE_URL"))
	if dsn == "" {
		t.Skip("TEST_APP_DATABASE_URL not set; skipping invoice integration test")
	}
	db, err := database.NewPostgresDB(&config.Config{DatabaseURL: dsn})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	storeID := uuid.MustParse("11111111-1111-4111-8111-111111111111")

	gin.SetMode(gin.TestMode)
	authSvc := service.NewAuthService(db, postgres.NewAuthRepository(), "integration-test-jwt-secret-at-least-32-chars", service.AuthPolicy{
		AccessTokenTTL: 15 * time.Minute, RefreshTokenTTL: 12 * time.Hour, SessionIdleTimeout: 2 * time.Hour,
		MaxSessions: 3, PINMaxFailures: 5, PINLockout: 15 * time.Minute, MaxConcurrentHashes: 8,
	})
	ah := handler.NewAuthHandler(authSvc)
	orderRepo, paymentRepo := postgres.NewOrderRepository(), postgres.NewPaymentRepository()
	pos := handler.NewPOSHandler(service.NewOrderService(db, orderRepo), service.NewPaymentService(db, paymentRepo, orderRepo, nil), service.NewSettingsService(db))
	pos.SetOperational(service.NewOperationalService(db, orderRepo, paymentRepo, nil, nil, nil))
	ch := handler.NewCatalogHandler(service.NewCatalogService(db))
	sh := handler.NewShiftHandler(service.NewShiftService(db))
	ih := handler.NewInvoiceHandler(service.NewInvoiceService(db))

	r := gin.New()
	r.POST("/login", ah.Login)
	r.POST("/pos/drawers", ah.RequirePermission(rbac.CashDrawerManage), sh.CreateDrawer)
	r.POST("/pos/shifts", ah.RequireStaff(), sh.OpenShift)
	r.POST("/pos/shifts/:id/force-close", ah.RequireStaff(), sh.ForceCloseShift)
	r.POST("/pos/orders", ah.RequirePermission(rbac.OrderPOSCreate), pos.CreateOrder)
	r.POST("/pos/orders/:id/payments", ah.RequireStaff(), pos.InitiatePayment)
	r.POST("/pos/orders/:id/payments/:payment_id/refund", ah.RequirePermission(rbac.PaymentRefund), pos.RefundPayment)
	r.GET("/pos/business-customers", ah.RequirePermission(rbac.PaymentTenderDirect), ih.ListChargeableCustomers)
	r.POST("/catalog/products", ah.RequirePermission(rbac.CatalogManage), ch.CreateProduct)
	r.DELETE("/catalog/products/:id", ah.RequirePermission(rbac.CatalogManage), ch.ArchiveProduct)
	inv := r.Group("/invoices", ah.RequirePermission(rbac.InvoiceManage))
	inv.GET("/customers", ih.ListBusinessCustomers)
	inv.POST("/customers", ih.CreateBusinessCustomer)
	inv.PATCH("/customers/:id", ih.UpdateBusinessCustomer)
	inv.GET("/uninvoiced-orders", ih.ListUninvoicedOrders)
	inv.GET("", ih.ListInvoices)
	inv.POST("", ih.CreateInvoice)
	inv.GET("/:id", ih.GetInvoice)
	inv.GET("/:id/pdf", ih.InvoicePDF)
	inv.POST("/:id/payments", ih.PayInvoice)

	call := func(method, path, authz string, body any) *httptest.ResponseRecorder {
		var buf bytes.Buffer
		if body != nil {
			_ = json.NewEncoder(&buf).Encode(body)
		}
		req := httptest.NewRequest(method, path, &buf)
		req.Header.Set("Content-Type", "application/json")
		if authz != "" {
			req.Header.Set("Authorization", authz)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	login := func(email string) string {
		t.Helper()
		res := call("POST", "/login", "", map[string]string{"store_slug": "central-cafe", "email": email, "pin": "482913"})
		if res.Code != http.StatusOK {
			t.Fatalf("login %s failed: %d", email, res.Code)
		}
		var s struct {
			AccessToken string `json:"access_token"`
		}
		_ = json.Unmarshal(res.Body.Bytes(), &s)
		return "Bearer " + s.AccessToken
	}
	inStore := func(fn func(tx *sqlx.Tx) error) {
		t.Helper()
		if err := database.WithTenantTx(ctx, db, storeID, fn); err != nil {
			t.Fatalf("tenant tx failed: %v", err)
		}
	}
	owner, cashier := login("abdulaziz543333@gmail.com"), login("cashier.central@mycaffe.local")
	suffix := uuid.NewString()[:6]

	res := call("POST", "/catalog/products", owner, map[string]any{"name": "Katering " + suffix, "product_type": "prepared",
		"prep_station": "bar", "variants": []map[string]any{{"name": "Reguler", "price": 30000, "is_default": true}}})
	if res.Code != http.StatusCreated {
		t.Fatalf("create product failed: %d: %s", res.Code, res.Body.String())
	}
	var product struct {
		ID uuid.UUID `json:"id"`
	}
	_ = json.Unmarshal(res.Body.Bytes(), &product)
	t.Cleanup(func() { call("DELETE", "/catalog/products/"+product.ID.String(), owner, nil) })

	newCustomer := func(name string, terms int) uuid.UUID {
		t.Helper()
		res := call("POST", "/invoices/customers", owner, map[string]any{"name": name + " " + suffix, "tax_id": "01.234", "email": "ap@example.com",
			"phone": "081234567890", "payment_terms_days": terms})
		if res.Code != http.StatusCreated {
			t.Fatalf("create customer failed: %d: %s", res.Code, res.Body.String())
		}
		var out struct {
			ID uuid.UUID `json:"id"`
		}
		_ = json.Unmarshal(res.Body.Bytes(), &out)
		return out.ID
	}
	var acme, other uuid.UUID
	t.Run("OwnerManagesBusinessCustomers", func(t *testing.T) {
		if res := call("GET", "/invoices/customers", cashier, nil); res.Code != http.StatusForbidden {
			t.Fatalf("cashier must not manage business customers, got %d", res.Code)
		}
		acme, other = newCustomer("PT Acme", 30), newCustomer("PT Lain", 7)
		if res := call("POST", "/invoices/customers", owner, map[string]any{"name": "PT Acme " + suffix}); res.Code != http.StatusConflict {
			t.Fatalf("a duplicate name must be 409, got %d", res.Code)
		}
		if res := call("POST", "/invoices/customers", owner, map[string]any{"name": "X " + suffix, "email": "bukan-email"}); res.Code != http.StatusUnprocessableEntity {
			t.Fatalf("a bad e-mail must be 422, got %d", res.Code)
		}
		picker := call("GET", "/pos/business-customers", cashier, nil)
		if picker.Code != http.StatusOK || !strings.Contains(picker.Body.String(), acme.String()) || strings.Contains(picker.Body.String(), "ap@example.com") {
			t.Fatalf("the POS picker lists names without contacts: %d: %s", picker.Code, picker.Body.String())
		}
	})

	var drawer domain.CashDrawer
	_ = json.Unmarshal(call("POST", "/pos/drawers", owner, map[string]any{"name": "Laci Invoice " + suffix}).Body.Bytes(), &drawer)
	var opened struct {
		Shift domain.Shift `json:"shift"`
	}
	if res := call("POST", "/pos/shifts", cashier, map[string]any{"drawer_id": drawer.ID, "opening_float": 0}); res.Code != http.StatusCreated {
		t.Fatalf("open shift failed: %d: %s", res.Code, res.Body.String())
	} else {
		_ = json.Unmarshal(res.Body.Bytes(), &opened)
	}
	t.Cleanup(func() {
		call("POST", fmt.Sprintf("/pos/shifts/%s/force-close", opened.Shift.ID), owner, map[string]any{"reason": "Akhir test invoice"})
	})
	newOrder := func() domain.Order {
		t.Helper()
		res := call("POST", "/pos/orders", cashier, map[string]any{"idempotency_key": "inv-" + uuid.NewString(), "order_type": "takeaway", "customer_name": "Kantor",
			"items": []map[string]any{{"product_id": product.ID, "quantity": 1}}})
		if res.Code != http.StatusCreated {
			t.Fatalf("create order failed: %d: %s", res.Code, res.Body.String())
		}
		var out struct {
			Order domain.Order `json:"order"`
		}
		_ = json.Unmarshal(res.Body.Bytes(), &out)
		return out.Order
	}
	charge := func(orderID uuid.UUID, body map[string]any) *httptest.ResponseRecorder {
		body["idempotency_key"], body["method"] = "pay-"+uuid.NewString(), "invoice"
		return call("POST", fmt.Sprintf("/pos/orders/%s/payments", orderID), cashier, body)
	}

	first, second := newOrder(), newOrder()
	var firstPayment uuid.UUID
	t.Run("CashierChargesOrdersToAnAccount", func(t *testing.T) {
		if res := charge(first.ID, map[string]any{}); res.Code < 400 || res.Code >= 500 {
			t.Fatalf("a charge needs a business customer, got %d", res.Code)
		}
		if res := charge(first.ID, map[string]any{"business_customer_id": uuid.New()}); res.Code < 400 || res.Code >= 500 {
			t.Fatalf("an unknown customer must be refused, got %d", res.Code)
		}
		res := charge(first.ID, map[string]any{"business_customer_id": acme})
		if res.Code != http.StatusOK {
			t.Fatalf("charge failed: %d: %s", res.Code, res.Body.String())
		}
		var paid struct {
			Payment struct {
				ID uuid.UUID `json:"id"`
			} `json:"payment"`
		}
		_ = json.Unmarshal(res.Body.Bytes(), &paid)
		firstPayment = paid.Payment.ID
		if res := charge(second.ID, map[string]any{"business_customer_id": acme}); res.Code != http.StatusOK {
			t.Fatalf("second charge failed: %d: %s", res.Code, res.Body.String())
		}
		var status string
		var cashMoves int
		inStore(func(tx *sqlx.Tx) error {
			if err := tx.GetContext(ctx, &status, `SELECT payment_status FROM orders WHERE store_id = $1 AND id = $2`, storeID, first.ID); err != nil {
				return err
			}
			return tx.GetContext(ctx, &cashMoves, `SELECT COUNT(*) FROM cash_movements WHERE store_id = $1 AND order_id = $2`, storeID, first.ID)
		})
		if status != "paid" || cashMoves != 0 {
			t.Fatalf("a charged order counts as paid and touches no cash: %s, %d moves", status, cashMoves)
		}
		pending := call("GET", "/invoices/uninvoiced-orders?business_customer_id="+acme.String(), owner, nil)
		if !strings.Contains(pending.Body.String(), first.ID.String()) || !strings.Contains(pending.Body.String(), second.ID.String()) {
			t.Fatalf("both charged orders wait to be billed: %s", pending.Body.String())
		}
	})

	type invoice struct {
		ID         uuid.UUID `json:"id"`
		Total      int64     `json:"total_amount"`
		Paid       int64     `json:"paid_amount"`
		Remaining  int64     `json:"remaining"`
		DueDate    string    `json:"due_date"`
		Overdue    bool      `json:"overdue"`
		IssuedAt   time.Time `json:"issued_at"`
		OrderLines []struct {
			ID uuid.UUID `json:"id"`
		} `json:"orders"`
	}
	var bill invoice
	t.Run("OwnerBillsChargedOrdersOnOneInvoice", func(t *testing.T) {
		if res := call("POST", "/invoices", owner, map[string]any{"business_customer_id": other, "order_ids": []uuid.UUID{first.ID}}); res.Code != http.StatusUnprocessableEntity {
			t.Fatalf("another customer's order must be 422, got %d", res.Code)
		}
		res := call("POST", "/invoices", owner, map[string]any{"business_customer_id": acme, "order_ids": []uuid.UUID{first.ID, second.ID}, "note": "Rapat"})
		if res.Code != http.StatusCreated {
			t.Fatalf("create invoice failed: %d: %s", res.Code, res.Body.String())
		}
		_ = json.Unmarshal(res.Body.Bytes(), &bill)
		if bill.Total != first.TotalAmount+second.TotalAmount || bill.Remaining != bill.Total || len(bill.OrderLines) != 2 || bill.Overdue {
			t.Fatalf("unexpected invoice: %+v", bill)
		}
		due, err := time.Parse(time.DateOnly, bill.DueDate)
		if days := due.Sub(bill.IssuedAt.Truncate(24*time.Hour)).Hours() / 24; err != nil || days < 29 || days > 31 {
			t.Fatalf("due date follows the 30-day terms: %s (%v days, %v)", bill.DueDate, days, err)
		}
		if res := call("POST", "/invoices", owner, map[string]any{"business_customer_id": acme, "order_ids": []uuid.UUID{first.ID}}); res.Code != http.StatusUnprocessableEntity {
			t.Fatalf("an order is billed once, got %d", res.Code)
		}
	})

	t.Run("ChargedOrderCannotBeRefunded", func(t *testing.T) {
		res := call("POST", fmt.Sprintf("/pos/orders/%s/payments/%s/refund", first.ID, firstPayment), cashier,
			map[string]any{"reason": "Salah", "pin": "482913", "restore_stock": true})
		if res.Code != http.StatusConflict {
			t.Fatalf("a charge took no money to refund: %d: %s", res.Code, res.Body.String())
		}
	})

	t.Run("InstalmentsAndPDF", func(t *testing.T) {
		path := "/invoices/" + bill.ID.String()
		if res := call("POST", path+"/payments", owner, map[string]any{"amount": bill.Total + 1, "method": "transfer"}); res.Code != http.StatusUnprocessableEntity {
			t.Fatalf("overpaying must be 422, got %d", res.Code)
		}
		if res := call("POST", path+"/payments", owner, map[string]any{"amount": 1000, "method": "cek"}); res.Code != http.StatusUnprocessableEntity {
			t.Fatalf("an unknown method must be 422, got %d", res.Code)
		}
		res := call("POST", path+"/payments", owner, map[string]any{"amount": 10000, "method": "transfer", "reference": "TRF-1"})
		var after invoice
		_ = json.Unmarshal(res.Body.Bytes(), &after)
		if res.Code != http.StatusCreated || after.Paid != 10000 || after.Remaining != bill.Total-10000 {
			t.Fatalf("instalment failed: %d: %s", res.Code, res.Body.String())
		}
		if open := call("GET", "/invoices?status=open", owner, nil); !strings.Contains(open.Body.String(), bill.ID.String()) {
			t.Fatalf("a part-paid invoice is open: %s", open.Body.String())
		}
		if res := call("POST", path+"/payments", owner, map[string]any{"amount": after.Remaining, "method": "qris"}); res.Code != http.StatusCreated {
			t.Fatalf("final payment failed: %d: %s", res.Code, res.Body.String())
		}
		if open := call("GET", "/invoices?status=open", owner, nil); strings.Contains(open.Body.String(), bill.ID.String()) {
			t.Fatal("a fully paid invoice is no longer open")
		}
		if paid := call("GET", "/invoices?status=paid", owner, nil); !strings.Contains(paid.Body.String(), bill.ID.String()) {
			t.Fatalf("a fully paid invoice is listed as paid: %s", paid.Body.String())
		}
		pdf := call("GET", path+"/pdf", owner, nil)
		if pdf.Code != http.StatusOK || pdf.Header().Get("Content-Type") != "application/pdf" || !bytes.HasPrefix(pdf.Body.Bytes(), []byte("%PDF-")) {
			t.Fatalf("pdf failed: %d %s", pdf.Code, pdf.Header().Get("Content-Type"))
		}
		var audits int
		inStore(func(tx *sqlx.Tx) error {
			return tx.GetContext(ctx, &audits, `SELECT COUNT(*) FROM audit_logs WHERE store_id = $1 AND reference_id = $2
				AND action IN ('INVOICE_ISSUED', 'INVOICE_PAYMENT_RECORDED')`, storeID, bill.ID.String())
		})
		if audits != 3 {
			t.Fatalf("expected 3 invoice audits, got %d", audits)
		}
	})
}
