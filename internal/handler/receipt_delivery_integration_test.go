package handler_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/config"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/handler"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/pkg/database"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/pkg/mailer"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/pkg/rbac"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/pkg/sms"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/repository/postgres"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/service"
)

type captureSender struct {
	mu   sync.Mutex
	sent []sms.Message
}

func (c *captureSender) Send(_ context.Context, m sms.Message) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sent = append(c.sent, m)
	return nil
}

func (c *captureSender) to(phone string) []sms.Message {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []sms.Message
	for _, m := range c.sent {
		if m.To == phone {
			out = append(out, m)
		}
	}
	return out
}

// TestReceiptDeliveryIntegration covers v0.9 PR 9.5 (decision W9): receipts by e-mail, WhatsApp and
// SMS (verified phones only), limits, guest ownership, privacy of the recipient and channel routing.
func TestReceiptDeliveryIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_APP_DATABASE_URL"))
	if dsn == "" {
		t.Skip("TEST_APP_DATABASE_URL not set; skipping receipt delivery integration test")
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
	smsOut, waOut := &captureSender{}, &captureSender{}
	messages := service.NewMessageService(db, smsOut)
	receipts := service.NewReceiptService(db, "https://demo.kafe.test")
	deliveries := service.NewReceiptDeliveryService(db, receipts, service.NewEmailService(db, mailer.LogSender{}), messages)
	rh := handler.NewReceiptDeliveryHandler(deliveries)

	r := gin.New()
	r.POST("/login", ah.Login)
	r.POST("/pos/orders/:id/receipt/send", ah.RequirePermission(rbac.OrderPOSCreate), rh.SendByStaff)

	call := func(path, auth string, body any) *httptest.ResponseRecorder {
		var buf bytes.Buffer
		_ = json.NewEncoder(&buf).Encode(body)
		req := httptest.NewRequest("POST", path, &buf)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", auth)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	inStore := func(fn func(tx *sqlx.Tx) error) {
		t.Helper()
		if err := database.WithTenantTx(ctx, db, storeID, fn); err != nil {
			t.Fatalf("tenant tx failed: %v", err)
		}
	}
	res := call("/login", "", map[string]string{"store_slug": "central-cafe", "email": "cashier.central@mycaffe.local", "pin": "482913"})
	var tokens struct {
		AccessToken string `json:"access_token"`
	}
	_ = json.Unmarshal(res.Body.Bytes(), &tokens)
	cashier := "Bearer " + tokens.AccessToken

	order := func(paymentStatus string, guest *uuid.UUID) uuid.UUID {
		t.Helper()
		id := uuid.New()
		inStore(func(tx *sqlx.Tx) error {
			_, err := tx.ExecContext(ctx, `
				INSERT INTO orders (id, store_id, idempotency_key, request_hash, order_number, queue_number, customer_name, order_type,
					status, payment_status, guest_session_id, subtotal_amount, total_amount)
				VALUES ($1, $2, $3, $4, $5, 'RC', 'Tamu', 'takeaway', 'completed', $6, $7, 46200, 46200)`,
				id, storeID, "rcpt-"+id.String(), strings.Repeat("e", 64), "RCPT-"+id.String()[:8], paymentStatus, guest)
			return err
		})
		return id
	}
	send := func(orderID uuid.UUID, channel, to string) *httptest.ResponseRecorder {
		return call(fmt.Sprintf("/pos/orders/%s/receipt/send", orderID), cashier, map[string]string{"channel": channel, "to": to})
	}
	expect := func(res *httptest.ResponseRecorder, code int, errCode string) {
		t.Helper()
		if res.Code != code || (errCode != "" && !strings.Contains(res.Body.String(), errCode)) {
			t.Fatalf("expected %d %s, got %d: %s", code, errCode, res.Code, res.Body.String())
		}
	}
	suffix := uuid.NewString()[:8]
	email := "Tamu." + suffix + "@Kafe.Test"
	paid := order("paid", nil)

	t.Run("EmailCarriesOnlyTheLinkAndMasksTheRecipient", func(t *testing.T) {
		expect(send(order("unpaid", nil), "email", email), http.StatusConflict, "RECEIPT_NOT_READY")
		expect(send(paid, "fax", email), http.StatusUnprocessableEntity, "")
		expect(send(paid, "email", "bukan-email"), http.StatusUnprocessableEntity, "")
		res := send(paid, "email", email)
		expect(res, http.StatusAccepted, "")
		if !strings.Contains(res.Body.String(), `"recipient":"t***@kafe.test"`) {
			t.Fatalf("response must mask the address: %s", res.Body.String())
		}
		var mail struct {
			Subject string `db:"subject"`
			Text    string `db:"text_body"`
		}
		inStore(func(tx *sqlx.Tx) error {
			return tx.GetContext(ctx, &mail, `SELECT subject, text_body FROM email_outbox WHERE store_id = $1 AND to_email = $2 AND template = 'receipt'`,
				storeID, strings.ToLower(email))
		})
		if !strings.Contains(mail.Subject, "RCPT-") || !strings.Contains(mail.Text, "https://demo.kafe.test/receipts/") || !strings.Contains(mail.Text, "Rp46.200") {
			t.Fatalf("receipt email must carry order number, total and link: %+v", mail)
		}
		var stored string
		inStore(func(tx *sqlx.Tx) error {
			return tx.GetContext(ctx, &stored, `SELECT recipient_masked || ' ' || recipient_hash FROM receipt_deliveries WHERE store_id = $1 AND order_id = $2`, storeID, paid)
		})
		if strings.Contains(stored, suffix) {
			t.Fatalf("deliveries must not store the plain address: %s", stored)
		}
	})

	phone := "+62813" + fmt.Sprintf("%08d", time.Now().UnixNano()%100000000)
	t.Run("WhatsAppNeedsTheDriver", func(t *testing.T) {
		expect(send(paid, "whatsapp", phone), http.StatusServiceUnavailable, "WHATSAPP_UNAVAILABLE")
		messages.SetWhatsApp(waOut)
		expect(send(paid, "whatsapp", phone), http.StatusAccepted, "")
	})

	t.Run("SMSOnlyToVerifiedPhonesAndPerOrderLimit", func(t *testing.T) {
		expect(send(paid, "sms", phone), http.StatusUnprocessableEntity, "PHONE_NOT_VERIFIED")
		inStore(func(tx *sqlx.Tx) error {
			_, err := tx.ExecContext(ctx, `INSERT INTO customers (store_id, phone, phone_verified_at) VALUES ($1, $2, NOW())
				ON CONFLICT (store_id, phone) DO UPDATE SET phone_verified_at = NOW()`, storeID, phone)
			return err
		})
		expect(send(paid, "sms", phone), http.StatusAccepted, "")
		expect(send(paid, "email", email), http.StatusTooManyRequests, "RECEIPT_SEND_LIMIT") // fourth send of this order
	})

	t.Run("RecipientDailyLimit", func(t *testing.T) {
		other := "limit." + suffix + "@kafe.test"
		sum := sha256.Sum256([]byte(other))
		inStore(func(tx *sqlx.Tx) error {
			for i := 0; i < 10; i++ {
				if _, err := tx.ExecContext(ctx, `INSERT INTO receipt_deliveries (store_id, order_id, channel, recipient_hash, recipient_masked)
					VALUES ($1, $2, 'email', $3, 'l***@kafe.test')`, storeID, paid, hex.EncodeToString(sum[:])); err != nil {
					return err
				}
			}
			return nil
		})
		expect(send(order("paid", nil), "email", other), http.StatusTooManyRequests, "RECEIPT_SEND_LIMIT")
	})

	t.Run("GuestOnlyOwnOrders", func(t *testing.T) {
		var tableSession, guestA, guestB uuid.UUID
		inStore(func(tx *sqlx.Tx) error {
			var tableID uuid.UUID
			if err := tx.GetContext(ctx, &tableID, `INSERT INTO tables (store_id, table_number, qr_token) VALUES ($1, $2, $3) RETURNING id`,
				storeID, "RC-"+suffix, strings.Repeat("q", 22)+suffix); err != nil {
				return err
			}
			if err := tx.GetContext(ctx, &tableSession, `INSERT INTO table_sessions (store_id, table_id, opened_via) VALUES ($1, $2, 'qr') RETURNING id`, storeID, tableID); err != nil {
				return err
			}
			for _, g := range []*uuid.UUID{&guestA, &guestB} {
				*g = uuid.New()
				hash := sha256.Sum256([]byte(g.String()))
				if _, err := tx.ExecContext(ctx, `INSERT INTO guest_sessions (id, store_id, table_session_id, token_hash, expires_at)
					VALUES ($1, $2, $3, $4, NOW() + INTERVAL '1 hour')`, *g, storeID, tableSession, hash[:]); err != nil {
					return err
				}
			}
			return nil
		})
		mine := order("paid", &guestA)
		guestEmail := "guest." + suffix + "@kafe.test"
		if _, err := deliveries.SendByGuest(ctx, &service.GuestIdentity{StoreID: storeID, SessionID: guestB}, mine, service.ReceiptSendInput{Channel: "email", To: guestEmail}); !errors.Is(err, service.ErrOrderNotFound) {
			t.Fatalf("another guest must not send this receipt, got %v", err)
		}
		out, err := deliveries.SendByGuest(ctx, &service.GuestIdentity{StoreID: storeID, SessionID: guestA}, mine, service.ReceiptSendInput{Channel: "email", To: guestEmail})
		if err != nil || out.Channel != "email" {
			t.Fatalf("guest must send the receipt of their own order: %v", err)
		}
	})

	t.Run("EachChannelUsesItsOwnSender", func(t *testing.T) {
		if _, err := messages.DeliverDue(ctx, 1000); err != nil {
			t.Fatal(err)
		}
		wa, text := waOut.to(phone), smsOut.to(phone)
		if len(wa) != 1 || len(strings.Split(wa[0].Body, "\n")) != 3 || !strings.HasPrefix(strings.Split(wa[0].Body, "\n")[2], "https://demo.kafe.test/receipts/") {
			t.Fatalf("whatsapp must get one template message with three parameters, got %+v", wa)
		}
		if len(text) != 1 || !strings.Contains(text[0].Body, "Struk RCPT-") {
			t.Fatalf("sms must get one plain message, got %+v", text)
		}
		var audits int
		inStore(func(tx *sqlx.Tx) error {
			return tx.GetContext(ctx, &audits, `SELECT COUNT(*) FROM audit_logs WHERE store_id = $1 AND action = 'RECEIPT_SENT' AND reference_id = $2`, storeID, paid.String())
		})
		if audits != 3 {
			t.Fatalf("expected 3 RECEIPT_SENT audits for the order, got %d", audits)
		}
	})
}
