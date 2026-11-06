package domain

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Promotion is an owner-defined automatic line promotion (K7, PR 11.1). Kind "percent" takes Value bps off
// the line; kind "amount" takes Value rupiah off each unit. DaysMask bit 0 is Sunday. StartMinute/EndMinute
// bound a happy hour in the store time zone (EndMinute may be below StartMinute to run past midnight).
type Promotion struct {
	ID          uuid.UUID  `json:"id" db:"id"`
	StoreID     uuid.UUID  `json:"-" db:"store_id"`
	Name        string     `json:"name" db:"name"`
	Kind        string     `json:"kind" db:"kind"`
	Value       int64      `json:"value" db:"value"`
	ProductIDs  UUIDList   `json:"product_ids" db:"product_ids"`
	CategoryIDs UUIDList   `json:"category_ids" db:"category_ids"`
	StartsAt    *time.Time `json:"starts_at,omitempty" db:"starts_at"`
	EndsAt      *time.Time `json:"ends_at,omitempty" db:"ends_at"`
	DaysMask    int        `json:"days_mask" db:"days_mask"`
	StartMinute *int       `json:"start_minute,omitempty" db:"start_minute"`
	EndMinute   *int       `json:"end_minute,omitempty" db:"end_minute"`
	Quota       *int       `json:"quota,omitempty" db:"quota"`
	UsedCount   int        `json:"used_count" db:"used_count"`
	IsActive    bool       `json:"is_active" db:"is_active"`
	CreatedAt   time.Time  `json:"created_at" db:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at" db:"updated_at"`
}

const (
	PromotionPercent = "percent"
	PromotionAmount  = "amount"
)

// UUIDList is a JSONB array of UUIDs.
type UUIDList []uuid.UUID

func (l UUIDList) Value() (driver.Value, error) {
	if l == nil {
		l = UUIDList{}
	}
	b, err := json.Marshal([]uuid.UUID(l))
	return string(b), err
}

func (l *UUIDList) Scan(src any) error {
	var raw []byte
	switch v := src.(type) {
	case []byte:
		raw = v
	case string:
		raw = []byte(v)
	case nil:
		*l = UUIDList{}
		return nil
	default:
		return fmt.Errorf("UUIDList: unsupported type %T", src)
	}
	return json.Unmarshal(raw, (*[]uuid.UUID)(l))
}

// Contains reports whether id is in the list.
func (l UUIDList) Contains(id uuid.UUID) bool {
	for _, x := range l {
		if x == id {
			return true
		}
	}
	return false
}

// BundleSlot is one part of a bundle ("Kopi", "Pastry"): Quantity units of one of ProductIDs (X4, PR 11.2).
type BundleSlot struct {
	ID              uuid.UUID `json:"id" db:"id"`
	StoreID         uuid.UUID `json:"-" db:"store_id"`
	BundleProductID uuid.UUID `json:"-" db:"bundle_product_id"`
	Name            string    `json:"name" db:"name"`
	Quantity        int       `json:"quantity" db:"quantity"`
	DisplayOrder    int       `json:"display_order" db:"display_order"`
	ProductIDs      UUIDList  `json:"product_ids" db:"product_ids"`
}
