package service

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/auth"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/database"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/sms"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/repository/postgres"
)

// Staff PIN reset by SMS (decision V4), next to the e-mail link of ADR-003.

var ErrInvalidPhone = errors.New("nomor HP tidak valid")

// WithAuthSMS enables SMS one-time codes for staff PIN reset.
func WithAuthSMS(messages *MessageService) AuthServiceOption {
	return func(s *AuthService) { s.messages = messages }
}

const staffResetSMS = "Kode reset PIN MyCaffe: %s. Berlaku 5 menit. Jangan berikan kode ini kepada siapa pun, termasuk staf MyCaffe."

// RequestPINResetSMS is the public "lupa PIN via SMS" flow. Like RequestPINReset it never reveals
// whether the store, phone or staff member exists, and a rate-limited request is silently dropped.
func (s *AuthService) RequestPINResetSMS(ctx context.Context, storeSlug, rawPhone string) error {
	if !s.messages.Enabled() {
		return ErrSMSUnavailable
	}
	phone, err := sms.NormalizePhone(rawPhone)
	if err != nil {
		return nil
	}
	storeID, active, err := s.repo.ResolveStoreForLogin(ctx, s.db, strings.ToLower(strings.TrimSpace(storeSlug)))
	if errors.Is(err, postgres.ErrStoreNotFound) || (err == nil && !active) {
		return nil
	}
	if err != nil {
		return err
	}
	now := s.nowFunc().UTC()
	err = database.WithTenantTx(ctx, s.db, storeID, func(tx *sqlx.Tx) error {
		var userID uuid.UUID
		err := tx.GetContext(ctx, &userID, `SELECT id FROM users WHERE store_id = $1 AND phone = $2 AND is_active AND pin_set_at IS NOT NULL`, storeID, phone)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		_, _, err = issueOTPTx(ctx, tx, s.messages, storeID, otpPurposeStaffPINReset, phone, &userID, nil, "staff_pin_reset", staffResetSMS, now)
		return err
	})
	if errors.Is(err, ErrOTPRateLimited) {
		return nil
	}
	return err
}

// ResetPINWithSMS sets a new PIN after a valid code. Every failure answers the same ErrOTPInvalid.
func (s *AuthService) ResetPINWithSMS(ctx context.Context, storeSlug, rawPhone, code, pin string) (*PINSetupResult, error) {
	if err := auth.ValidatePINPolicy(pin); err != nil {
		return nil, err
	}
	phone, err := sms.NormalizePhone(rawPhone)
	if err != nil {
		return nil, ErrOTPInvalid
	}
	storeID, active, err := s.repo.ResolveStoreForLogin(ctx, s.db, strings.ToLower(strings.TrimSpace(storeSlug)))
	if errors.Is(err, postgres.ErrStoreNotFound) || (err == nil && !active) {
		return nil, ErrOTPInvalid
	}
	if err != nil {
		return nil, err
	}
	pinHash, err := s.hashPIN(pin)
	if err != nil {
		return nil, err
	}
	now := s.nowFunc().UTC()
	var result *PINSetupResult
	err = database.WithTenantTx(ctx, s.db, storeID, func(tx *sqlx.Tx) error {
		c, err := latestOTPTx(ctx, tx, storeID, otpPurposeStaffPINReset, phone)
		if err != nil || c == nil {
			return err
		}
		ok, err := checkOTPTx(ctx, tx, storeID, c, strings.TrimSpace(code), now)
		if err != nil || !ok {
			return err // a wrong code commits the spent attempt
		}
		user, err := s.repo.GetUserByIDForUpdate(ctx, tx, storeID, c.UserID.UUID)
		if err != nil {
			return err
		}
		if !user.IsActive {
			return nil
		}
		if err := s.repo.SetUserPIN(ctx, tx, storeID, user.ID, pinHash, false, now); err != nil {
			return err
		}
		if err := s.emailRepo.ConsumeOpenTokens(ctx, tx, storeID, user.ID, now); err != nil {
			return err
		}
		if _, err := s.repo.RevokeAllUserSessions(ctx, tx, storeID, user.ID, "pin_reset", now); err != nil {
			return err
		}
		if err := s.enqueuePINChangedTx(ctx, tx, user, now); err != nil {
			return err
		}
		self := &AuthenticatedStaff{Staff: StaffIdentity{UserID: user.ID, FullName: user.FullName, Store: StaffStore{ID: storeID}}}
		if err := s.auditTx(ctx, tx, self, "STAFF_PIN_SET", "PIN diatur ulang dengan kode OTP SMS", user.ID, map[string]any{"purpose": "sms_reset"}); err != nil {
			return err
		}
		slug, _, err := s.repo.GetStoreProfile(ctx, tx, storeID)
		if err != nil {
			return err
		}
		result = &PINSetupResult{StoreSlug: slug, Email: user.Email}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, ErrOTPInvalid
	}
	return result, nil
}

// SetStaffPhone records the number the staff member receives reset codes on (owner only).
func (s *AuthService) SetStaffPhone(ctx context.Context, actor *AuthenticatedStaff, userID uuid.UUID, rawPhone string) (string, error) {
	if err := requireOwner(actor); err != nil {
		return "", err
	}
	phone, err := sms.NormalizePhone(rawPhone)
	if err != nil {
		return "", ErrInvalidPhone
	}
	storeID := actor.Staff.Store.ID
	err = database.WithTenantTx(ctx, s.db, storeID, func(tx *sqlx.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE users SET phone = $3 WHERE store_id = $1 AND id = $2`, storeID, userID, phone)
		if _, unique := postgres.UniqueViolation(err); unique {
			return &ConflictError{Field: "phone", Message: "nomor HP sudah dipakai staf lain di toko ini"}
		}
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrStaffNotFound
		}
		return s.auditTx(ctx, tx, actor, "STAFF_PHONE_SET", "Nomor HP staf untuk kode OTP diatur owner", userID, nil)
	})
	return phone, err
}
