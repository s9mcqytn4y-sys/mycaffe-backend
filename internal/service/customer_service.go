package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/domain"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/pkg/database"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/pkg/rbac"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/pkg/sms"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/repository/postgres"
)

// Customer phone verification at the POS (decision V4): a verified number is required before it is
// used for WhatsApp/SMS receipts, points, deposits or invoices.

var ErrVerificationNotFound = errors.New("verification not found")

const customerVerifySMS = "Kode verifikasi nomor HP Anda di MyCaffe: %s. Berlaku 5 menit. Tunjukkan kode ini hanya kepada kasir."

type Customer struct {
	ID              uuid.UUID  `json:"id" db:"id"`
	Phone           string     `json:"phone" db:"phone"`
	Name            string     `json:"name" db:"name"`
	PhoneVerifiedAt *time.Time `json:"phone_verified_at,omitempty" db:"phone_verified_at"`
}

type CustomerVerification struct {
	ChallengeID uuid.UUID `json:"challenge_id"`
	CustomerID  uuid.UUID `json:"customer_id"`
	Phone       string    `json:"phone"`
	ExpiresAt   time.Time `json:"expires_at"`
}

type CustomerService struct {
	db       *sqlx.DB
	messages *MessageService
	nowFunc  func() time.Time
}

func NewCustomerService(db *sqlx.DB, messages *MessageService) *CustomerService {
	return &CustomerService{db: db, messages: messages, nowFunc: time.Now}
}

// StartVerification upserts the customer by phone and texts a code.
func (s *CustomerService) StartVerification(ctx context.Context, actor *AuthenticatedStaff, rawPhone, name string) (*CustomerVerification, error) {
	if !actor.Can(rbac.OrderPOSCreate) {
		return nil, ErrForbidden
	}
	phone, err := sms.NormalizePhone(rawPhone)
	name = strings.TrimSpace(name)
	fields := map[string]string{}
	if err != nil {
		fields["phone"] = "nomor HP tidak valid (contoh 081234567890)"
	}
	if len(name) > 100 {
		fields["name"] = "maksimal 100 karakter"
	}
	if len(fields) > 0 {
		return nil, &ValidationError{Fields: fields}
	}
	if !s.messages.Enabled() {
		return nil, ErrSMSUnavailable
	}
	storeID, now := actor.Staff.Store.ID, s.nowFunc().UTC()
	out := &CustomerVerification{Phone: phone}
	err = database.WithTenantTx(ctx, s.db, storeID, func(tx *sqlx.Tx) error {
		if err := tx.GetContext(ctx, &out.CustomerID, `INSERT INTO customers (store_id, phone, name) VALUES ($1, $2, NULLIF($3, ''))
			ON CONFLICT (store_id, phone) DO UPDATE SET name = COALESCE(NULLIF(EXCLUDED.name, ''), customers.name), updated_at = NOW()
			RETURNING id`, storeID, phone, name); err != nil {
			return err
		}
		var err error
		out.ChallengeID, out.ExpiresAt, err = issueOTPTx(ctx, tx, s.messages, storeID, otpPurposeCustomerPhone, phone, nil, &out.CustomerID,
			"customer_phone_verify", customerVerifySMS, now)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ConfirmVerification checks the code the customer reads out and marks the phone verified.
func (s *CustomerService) ConfirmVerification(ctx context.Context, actor *AuthenticatedStaff, challengeID uuid.UUID, code string) (*Customer, error) {
	if !actor.Can(rbac.OrderPOSCreate) {
		return nil, ErrForbidden
	}
	storeID, now := actor.Staff.Store.ID, s.nowFunc().UTC()
	var out *Customer
	err := database.WithTenantTx(ctx, s.db, storeID, func(tx *sqlx.Tx) error {
		var c otpChallenge
		err := tx.GetContext(ctx, &c, `SELECT id, user_id, customer_id, code_hash, attempts, expires_at, consumed_at FROM otp_challenges
			WHERE store_id = $1 AND id = $2 AND purpose = 'customer_phone' FOR UPDATE`, storeID, challengeID)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrVerificationNotFound
		}
		if err != nil {
			return err
		}
		ok, err := checkOTPTx(ctx, tx, storeID, &c, strings.TrimSpace(code), now)
		if err != nil || !ok {
			return err
		}
		var cust Customer
		if err := tx.GetContext(ctx, &cust, `UPDATE customers SET phone_verified_at = $3, updated_at = $3 WHERE store_id = $1 AND id = $2
			RETURNING id, phone, COALESCE(name, '') AS name, phone_verified_at`, storeID, c.CustomerID.UUID, now); err != nil {
			return err
		}
		actorID := actor.Staff.UserID
		meta, _ := json.Marshal(map[string]any{"customer_id": cust.ID, "challenge_id": c.ID})
		if err := postgres.InsertAuditLog(ctx, tx, &domain.AuditLog{StoreID: storeID, ActorID: &actorID, ActorName: actor.Staff.FullName,
			Action: "CUSTOMER_PHONE_VERIFIED", Reason: "Nomor HP pelanggan diverifikasi dengan kode OTP", ReferenceID: cust.ID.String(),
			Metadata: string(meta), CreatedAt: now}); err != nil {
			return err
		}
		out = &cust
		return nil
	})
	if err != nil {
		return nil, err
	}
	if out == nil {
		return nil, ErrOTPInvalid
	}
	return out, nil
}
