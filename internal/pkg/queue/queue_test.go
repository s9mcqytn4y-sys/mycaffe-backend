package queue_test

import (
	"strings"
	"testing"

	"github.com/s9mcqytn4y-sys/cassy-backend/internal/pkg/queue"
)

func TestCalculator_ZeroOrdersAhead(t *testing.T) {
	calc := queue.NewCalculator(2, 2)
	items := []queue.OrderItemSnapshot{
		{Category: queue.CategoryBeverage, Quantity: 2},
		{Category: queue.CategoryPastry, Quantity: 1},
	}

	est := calc.CalculateEstimate(14, 0, items)
	if est.QueueNumber != 14 {
		t.Errorf("expected queue number 14, got %d", est.QueueNumber)
	}
	if est.MinWaitMinutes < 3 {
		t.Errorf("expected min wait >= 3 min, got %d", est.MinWaitMinutes)
	}
	if est.MaxWaitMinutes <= est.MinWaitMinutes {
		t.Errorf("expected max wait > min wait")
	}
	if !strings.Contains(est.DisplayText, "Pesanan Anda #014") {
		t.Errorf("expected formatted string containing '#014', got: %s", est.DisplayText)
	}
}

func TestCalculator_BusyQueueScaling(t *testing.T) {
	calc := queue.NewCalculator(2, 2)
	items := []queue.OrderItemSnapshot{
		{Category: queue.CategoryKitchen, Quantity: 3},
	}

	estAhead0 := calc.CalculateEstimate(5, 0, items)
	estAhead10 := calc.CalculateEstimate(15, 10, items)

	if estAhead10.MinWaitMinutes <= estAhead0.MinWaitMinutes {
		t.Errorf("expected 10 orders ahead to result in larger wait time than 0 ahead")
	}
}
