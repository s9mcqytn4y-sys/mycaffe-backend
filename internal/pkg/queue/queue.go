package queue

import (
	"fmt"
	"math"
)

// ItemCategory represents the preparation station category.
type ItemCategory string

const (
	CategoryBeverage ItemCategory = "beverage"
	CategoryKitchen  ItemCategory = "kitchen"
	CategoryPastry   ItemCategory = "pastry"
)

// OrderItemSnapshot represents an item in an order for queue estimation.
type OrderItemSnapshot struct {
	Category ItemCategory
	Quantity int
}

// Estimate represents the calculated queue wait time and position.
type Estimate struct {
	QueueNumber    int    `json:"queue_number"`
	OrdersAhead    int    `json:"orders_ahead"`
	MinWaitMinutes int    `json:"min_wait_minutes"`
	MaxWaitMinutes int    `json:"max_wait_minutes"`
	DisplayText    string `json:"display_text"`
}

// Calculator handles realistic queue positioning and time estimates for F&B operations.
type Calculator struct {
	ConcurrencyBaristas int // Parallel beverage stations (default 2)
	ConcurrencyKitchen  int // Parallel food cooks (default 2)
}

// NewCalculator creates a Calculator with realistic cafe staffing defaults.
func NewCalculator(baristas, cooks int) *Calculator {
	if baristas <= 0 {
		baristas = 2
	}
	if cooks <= 0 {
		cooks = 2
	}
	return &Calculator{
		ConcurrencyBaristas: baristas,
		ConcurrencyKitchen:  cooks,
	}
}

// CalculateEstimate computes the queue position and estimated preparation duration.
func (c *Calculator) CalculateEstimate(dailySeq int, ordersAhead int, items []OrderItemSnapshot) Estimate {
	if ordersAhead < 0 {
		ordersAhead = 0
	}

	var beverageUnits, kitchenUnits, pastryUnits int
	for _, it := range items {
		qty := it.Quantity
		if qty <= 0 {
			qty = 1
		}
		switch it.Category {
		case CategoryBeverage:
			beverageUnits += qty
		case CategoryKitchen:
			kitchenUnits += qty
		case CategoryPastry:
			pastryUnits += qty
		default:
			kitchenUnits += qty
		}
	}

	// Dynamic work duration (in minutes) for this order
	bevWork := float64(beverageUnits*2) / float64(c.ConcurrencyBaristas)
	foodWork := float64(kitchenUnits*5) / float64(c.ConcurrencyKitchen)
	pastryWork := float64(pastryUnits * 1) // Fast pastry grab/warm

	// The order is ready when the slowest station finishes
	selfDuration := math.Max(bevWork+pastryWork, foodWork)
	if selfDuration < 3.0 {
		selfDuration = 3.0 // Minimum physical prep time
	}

	// Buffer for preceding orders amortized over parallel stations
	ordersAheadLag := float64(ordersAhead) * 2.5

	minMinutes := int(math.Floor(selfDuration + ordersAheadLag))
	if minMinutes < 3 {
		minMinutes = 3
	}

	// Max range adds ~35% buffer for peak operational variance
	maxMinutes := int(math.Ceil(float64(minMinutes) * 1.35))
	if maxMinutes <= minMinutes {
		maxMinutes = minMinutes + 4
	}

	displayText := fmt.Sprintf("Pesanan Anda #%03d - Estimasi %d-%d menit", dailySeq, minMinutes, maxMinutes)

	return Estimate{
		QueueNumber:    dailySeq,
		OrdersAhead:    ordersAhead,
		MinWaitMinutes: minMinutes,
		MaxWaitMinutes: maxMinutes,
		DisplayText:    displayText,
	}
}
