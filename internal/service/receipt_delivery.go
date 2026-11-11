package service

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/mail"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/domain"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/pkg/database"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/pkg/mailer"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/pkg/rbac"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/pkg/sms"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/repository/postgres"
)

// Digital receipts (v0.9 PR 9.5, decision W9): the cashier or the guest sends the receipt link of a
// paid order by e-mail, WhatsApp (Cloud API template) or SMS (verified phones only, V4). The message
// carries only the 90-day receipt link. Limits: 3 sends per order, 10 per recipient per day.

var (
	ErrReceiptNotPaid   = errors.New("order is not paid yet")
	ErrReceiptSendLimit = errors.New("receipt send limit reached")
	ErrPhoneNotVerified = errors.New("phone number is not verified")
)

const (
	receiptSendsPerOrder     = 3
	receiptSendsPerRecipient = 10
)

type ReceiptSendInput struct {
	Channel string `json:"channel"`
	To      string `json:"to"`
}

type ReceiptSendResult struct {
	Channel   string    `json:"channel"`
	Recipient string    `json:"recipient"` // masked
	ExpiresAt time.Time `json:"expires_at"`
}

type ReceiptDeliveryService struct {
	db       *sqlx.DB
	receipts *ReceiptService
	email    *EmailService
	messages *MessageService
	nowFunc  func() time.Time
}

func NewReceiptDeliveryService(db *sqlx.DB, receipts *ReceiptService, email *EmailService, messages *MessageService) *ReceiptDeliveryService {
	return &ReceiptDeliveryService{db: db, receipts: receipts, email: email, messages: messages, nowFunc: time.Now}
}

// SendByStaff sends the receipt of any paid order of the store.
func (s *ReceiptDeliveryService) SendByStaff(ctx context.Context, actor *AuthenticatedStaff, orderID uuid.UUID, in ReceiptSendInput) (*ReceiptSendResult, error) {
	if actor == nil {
		return nil, ErrInvalidSession
	}
	if !actor.Can(rbac.OrderPOSCreate) {
		return nil, ErrForbidden
	}
	userID := actor.Staff.UserID
	return s.send(ctx, actor.Staff.Store.ID, orderID, in, &userID, nil, actor.Staff.FullName)
}

// SendByGuest sends the receipt of one of the guest's own paid QR orders.
func (s *ReceiptDeliveryService) SendByGuest(ctx context.Context, g *GuestIdentity, orderID uuid.UUID, in ReceiptSendInput) (*ReceiptSendResult, error) {
	if g == nil {
		return nil, ErrGuestSessionInvalid
	}
	return s.send(ctx, g.StoreID, orderID, in, nil, &g.SessionID, "Tamu (QR)")
}

// normalizeRecipient validates the address for the channel and returns it with its masked form.
func normalizeRecipient(channel, to string) (string, string, error) {
	switch channel {
	case "email":
		addr, err := mail.ParseAddress(strings.TrimSpace(to))
		if err != nil || addr.Name != "" || len(addr.Address) > 254 {
			return "", "", &ValidationError{Fields: map[string]string{"to": "alamat email tidak valid"}}
		}
		email := strings.ToLower(addr.Address)
		at := strings.LastIndex(email, "@")
		return email, email[:1] + "***" + email[at:], nil
	case "whatsapp", "sms":
		phone, err := sms.NormalizePhone(to)
		if err != nil {
			return "", "", &ValidationError{Fields: map[string]string{"to": "nomor HP tidak valid"}}
		}
		return phone, phone[:5] + "****" + phone[len(phone)-3:], nil
	}
	return "", "", &ValidationError{Fields: map[string]string{"channel": "harus email, whatsapp, atau sms"}}
}

func (s *ReceiptDeliveryService) send(ctx context.Context, storeID, orderID uuid.UUID, in ReceiptSendInput, userID, guestSessionID *uuid.UUID, actorName string) (*ReceiptSendResult, error) {
	channel := strings.ToLower(strings.TrimSpace(in.Channel))
	to, masked, err := normalizeRecipient(channel, in.To)
	if err != nil {
		return nil, err
	}
	switch {
	case channel == "whatsapp" && !s.messages.WhatsAppEnabled():
		return nil, ErrWhatsAppUnavailable
	case channel == "sms" && !s.messages.Enabled():
		return nil, ErrSMSUnavailable
	}
	sum := sha256.Sum256([]byte(to))
	recipientHash := hex.EncodeToString(sum[:])
	now := s.nowFunc().UTC()
	out := &ReceiptSendResult{Channel: channel, Recipient: masked}
	err = database.WithTenantTx(ctx, s.db, storeID, func(tx *sqlx.Tx) error {
		// The row lock serializes sends of one order, so the per-order limit holds under double taps.
		var order struct {
			OrderNumber    string     `db:"order_number"`
			PaymentStatus  string     `db:"payment_status"`
			GuestSessionID *uuid.UUID `db:"guest_session_id"`
			StoreName      string     `db:"store_name"`
		}
		err := tx.GetContext(ctx, &order, `SELECT o.order_number, o.payment_status, o.guest_session_id, s.name AS store_name
			FROM orders o JOIN stores s ON s.id = o.store_id WHERE o.store_id = $1 AND o.id = $2 FOR UPDATE OF o`, storeID, orderID)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && guestSessionID != nil &&
			(order.GuestSessionID == nil || *order.GuestSessionID != *guestSessionID)) {
			return ErrOrderNotFound
		}
		if err != nil {
			return err
		}
		if order.PaymentStatus != "paid" && order.PaymentStatus != "partially_refunded" && order.PaymentStatus != "refunded" {
			return ErrReceiptNotPaid
		}
		if channel == "sms" {
			var verified bool
			if err := tx.GetContext(ctx, &verified, `SELECT EXISTS (SELECT 1 FROM customers
				WHERE store_id = $1 AND phone = $2 AND phone_verified_at IS NOT NULL)`, storeID, to); err != nil {
				return err
			}
			if !verified {
				return ErrPhoneNotVerified
			}
		}
		var perOrder, perRecipient int
		if err := tx.GetContext(ctx, &perOrder, `SELECT COUNT(*) FROM receipt_deliveries WHERE store_id = $1 AND order_id = $2`, storeID, orderID); err != nil {
			return err
		}
		if err := tx.GetContext(ctx, &perRecipient, `SELECT COUNT(*) FROM receipt_deliveries
			WHERE store_id = $1 AND recipient_hash = $2 AND created_at > $3::timestamptz - INTERVAL '1 day'`, storeID, recipientHash, now); err != nil {
			return err
		}
		if perOrder >= receiptSendsPerOrder || perRecipient >= receiptSendsPerRecipient {
			return ErrReceiptSendLimit
		}
		link, err := s.receipts.issueLinkTx(ctx, tx, storeID, orderID, userID)
		if err != nil {
			return err
		}
		out.ExpiresAt = link.ExpiresAt
		ref := orderID.String()
		switch channel {
		case "email":
			r, berr := s.receipts.buildTx(ctx, tx, storeID, orderID, ReceiptKindSale, nil)
			if berr != nil {
				return berr
			}
			err = s.email.EnqueueTx(ctx, tx, storeID, mailer.TemplateReceipt, to, "", ref, receiptEmailData(r, link))
		case "whatsapp":
			// Template parameters, one per line: {{1}} store, {{2}} order number, {{3}} link.
			err = s.messages.EnqueueWhatsAppTx(ctx, tx, storeID, to, "receipt", order.StoreName+"\n"+order.OrderNumber+"\n"+link.URL, ref)
		case "sms":
			err = s.messages.EnqueueSMSTx(ctx, tx, storeID, to, "receipt",
				"Terima kasih sudah mampir ke "+order.StoreName+". Struk "+order.OrderNumber+": "+link.URL, ref)
		}
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO receipt_deliveries (store_id, order_id, channel, recipient_hash, recipient_masked,
			requested_by_user_id, guest_session_id, created_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
			storeID, orderID, channel, recipientHash, masked, userID, guestSessionID, now); err != nil {
			return err
		}
		meta, _ := json.Marshal(map[string]any{"channel": channel, "recipient": masked})
		return postgres.InsertAuditLog(ctx, tx, &domain.AuditLog{
			StoreID: storeID, ActorID: userID, ActorName: actorName, Action: "RECEIPT_SENT",
			Reason: "Struk digital dikirim lewat " + channel, ReferenceID: ref, Metadata: string(meta), CreatedAt: now,
		})
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// receiptEmailData fills the receipt email from the stored receipt, so lines and totals match the paper
// receipt. The link opens the web receipt page, which also shows the order's current status (PRD v3 N8).
func receiptEmailData(r *Receipt, link *ReceiptLink) mailer.Data {
	d := mailer.Data{
		ActionURL: link.URL, ActionLabel: "Lihat struk dan status pesanan",
		OrderNumber: r.OrderNumber, TotalLabel: rupiah(r.Total), ExpiresAt: link.ExpiresAt.Format("02/01/2006"),
		IssuedAt: formatLocalTime(r.IssuedAt), SubtotalItemsLabel: rupiah(r.Subtotal),
	}
	// r.Payments holds only payments that took money; split bills may repeat a method.
	methods := []string{}
	for _, p := range r.Payments {
		if label := methodLabel(p.Method); !slices.Contains(methods, label) {
			methods = append(methods, label)
		}
	}
	d.PaymentMethod = strings.Join(methods, ", ")
	if r.Discount > 0 {
		d.DiscountLabel = rupiah(r.Discount)
	}
	if r.Service > 0 {
		d.ServiceFeeLabel = rupiah(r.Service)
	}
	if r.Tax > 0 {
		d.TaxLabel = rupiah(r.Tax)
	}
	for _, l := range r.Lines {
		details := l.Options
		if l.Variant != "" {
			details = append([]string{l.Variant}, details...)
		}
		d.Items = append(d.Items, mailer.ReceiptItem{
			Name: l.Name, Details: strings.Join(details, ", "), Quantity: l.Quantity, SubtotalLabel: rupiah(l.Amount),
		})
	}
	return d
}
