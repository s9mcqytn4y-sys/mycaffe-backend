package domain

import (
	"time"

	"github.com/google/uuid"
)

type Store struct {
	ID                uuid.UUID `json:"id" db:"id"`
	Slug              string    `json:"slug" db:"slug"`
	Name              string    `json:"name" db:"name"`
	Address           string    `json:"address" db:"address"`
	Phone             string    `json:"phone" db:"phone"`
	IsActive          bool      `json:"is_active" db:"is_active"`
	TaxPercentage     int64     `json:"tax_percentage" db:"tax_percentage"`           // e.g. 10 for 10%
	ServicePercentage int64     `json:"service_percentage" db:"service_percentage"`   // e.g. 5 for 5%
	QRISMDRPercentage int64     `json:"qris_mdr_percentage" db:"qris_mdr_percentage"` // basis points (e.g. 70 for 0.7%)
	Currency          string    `json:"currency" db:"currency"`                       // IDR
	CreatedAt         time.Time `json:"created_at" db:"created_at"`
	UpdatedAt         time.Time `json:"updated_at" db:"updated_at"`
}
