package domain

import (
	"time"

	"github.com/google/uuid"
)

type OrderRoundStatus string

const (
	OrderRoundStatusQueued    OrderRoundStatus = "queued"
	OrderRoundStatusPreparing OrderRoundStatus = "preparing"
	OrderRoundStatusReady     OrderRoundStatus = "ready"
	OrderRoundStatusServed    OrderRoundStatus = "served"
	OrderRoundStatusCancelled OrderRoundStatus = "cancelled"
)

func ValidOrderRoundStatuses() []OrderRoundStatus {
	return []OrderRoundStatus{
		OrderRoundStatusQueued,
		OrderRoundStatusPreparing,
		OrderRoundStatusReady,
		OrderRoundStatusServed,
		OrderRoundStatusCancelled,
	}
}

type OrderRound struct {
	ID                uuid.UUID        `json:"id" db:"id"`
	StoreID           uuid.UUID        `json:"store_id" db:"store_id"`
	OrderID           uuid.UUID        `json:"order_id" db:"order_id"`
	RoundNumber       int              `json:"round_number" db:"round_number"`
	Status            OrderRoundStatus `json:"status" db:"status"`
	IdempotencyKey    string           `json:"idempotency_key" db:"idempotency_key"`
	RequestHash       string           `json:"request_hash" db:"request_hash"`
	SubmittedByUserID *uuid.UUID       `json:"submitted_by_user_id,omitempty" db:"submitted_by_user_id"`
	SubmittedAt       time.Time        `json:"submitted_at" db:"submitted_at"`
	Notes             string           `json:"notes,omitempty" db:"notes"`
	Items             []OrderItem      `json:"items,omitempty"`
	CreatedAt         time.Time        `json:"created_at" db:"created_at"`
	UpdatedAt         time.Time        `json:"updated_at" db:"updated_at"`
}
