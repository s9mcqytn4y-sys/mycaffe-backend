package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

// One-time codes sent by SMS (decision V4): 6 digits, 5 minutes, 5 tries, at most one per minute
// and five per hour for a phone and purpose. Only the SHA-256 of id+code is stored.

const (
	otpPurposeStaffPINReset = "staff_pin_reset"
	otpPurposeCustomerPhone = "customer_phone"

	otpTTL         = 5 * time.Minute
	otpMaxAttempts = 5
	otpCooldown    = time.Minute
	otpHourlyCap   = 5
)

var (
	ErrOTPInvalid     = errors.New("kode OTP salah atau kedaluwarsa")
	ErrOTPRateLimited = errors.New("terlalu banyak permintaan kode OTP")
)

type otpChallenge struct {
	ID         uuid.UUID     `db:"id"`
	UserID     uuid.NullUUID `db:"user_id"`
	CustomerID uuid.NullUUID `db:"customer_id"`
	CodeHash   string        `db:"code_hash"`
	Attempts   int           `db:"attempts"`
	ExpiresAt  time.Time     `db:"expires_at"`
	ConsumedAt *time.Time    `db:"consumed_at"`
}

func newOTPCode() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

func hashOTP(id uuid.UUID, code string) string {
	sum := sha256.Sum256([]byte(id.String() + ":" + code))
	return hex.EncodeToString(sum[:])
}

// issueOTPTx stores a challenge and queues its SMS; body receives the code via %s.
func issueOTPTx(ctx context.Context, tx *sqlx.Tx, messages *MessageService, storeID uuid.UUID, purpose, phone string,
	userID, customerID *uuid.UUID, template, body string, now time.Time) (uuid.UUID, time.Time, error) {
	var recent struct {
		Hour int        `db:"hour"`
		Last *time.Time `db:"last"`
	}
	if err := tx.GetContext(ctx, &recent, `SELECT COUNT(*) AS hour, MAX(created_at) AS last FROM otp_challenges
		WHERE store_id = $1 AND purpose = $2 AND phone = $3 AND created_at > $4`, storeID, purpose, phone, now.Add(-time.Hour)); err != nil {
		return uuid.Nil, time.Time{}, err
	}
	if recent.Hour >= otpHourlyCap || (recent.Last != nil && now.Sub(*recent.Last) < otpCooldown) {
		return uuid.Nil, time.Time{}, ErrOTPRateLimited
	}
	code, err := newOTPCode()
	if err != nil {
		return uuid.Nil, time.Time{}, err
	}
	id, expires := uuid.New(), now.Add(otpTTL)
	if _, err := tx.ExecContext(ctx, `INSERT INTO otp_challenges (id, store_id, purpose, phone, user_id, customer_id, code_hash, expires_at, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`, id, storeID, purpose, phone, userID, customerID, hashOTP(id, code), expires, now); err != nil {
		return uuid.Nil, time.Time{}, fmt.Errorf("failed to store otp: %w", err)
	}
	if err := messages.EnqueueSMSTx(ctx, tx, storeID, phone, template, fmt.Sprintf(body, code), id.String()); err != nil {
		return uuid.Nil, time.Time{}, err
	}
	return id, expires, nil
}

// checkOTPTx spends one attempt on a locked challenge. It returns false for a wrong, used or expired
// code; the caller must still commit so the attempt counts.
func checkOTPTx(ctx context.Context, tx *sqlx.Tx, storeID uuid.UUID, c *otpChallenge, code string, now time.Time) (bool, error) {
	if c.ConsumedAt != nil || !now.Before(c.ExpiresAt) || c.Attempts >= otpMaxAttempts {
		return false, nil
	}
	ok := subtle.ConstantTimeCompare([]byte(hashOTP(c.ID, code)), []byte(c.CodeHash)) == 1
	query := `UPDATE otp_challenges SET attempts = attempts + 1 WHERE store_id = $1 AND id = $2`
	if ok {
		query = `UPDATE otp_challenges SET attempts = attempts + 1, consumed_at = $3 WHERE store_id = $1 AND id = $2`
		_, err := tx.ExecContext(ctx, query, storeID, c.ID, now)
		return true, err
	}
	_, err := tx.ExecContext(ctx, query, storeID, c.ID)
	return false, err
}

// latestOTPTx locks the newest challenge of a purpose for a phone.
func latestOTPTx(ctx context.Context, tx *sqlx.Tx, storeID uuid.UUID, purpose, phone string) (*otpChallenge, error) {
	var c otpChallenge
	err := tx.GetContext(ctx, &c, `SELECT id, user_id, customer_id, code_hash, attempts, expires_at, consumed_at FROM otp_challenges
		WHERE store_id = $1 AND purpose = $2 AND phone = $3 ORDER BY created_at DESC LIMIT 1 FOR UPDATE`, storeID, purpose, phone)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &c, err
}
