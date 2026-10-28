package realtime_test

import (
	"testing"
	"time"

	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/realtime"
)

func TestHubTenantIsolation(t *testing.T) {
	hub := realtime.NewHub([]string{"http://localhost:3000"})
	go hub.Run()

	// 1. Broadcast with empty StoreID must return an error (prevent cross-tenant leaks)
	err := hub.BroadcastEvent("test.event", "", map[string]string{"msg": "leak"})
	if err == nil {
		t.Errorf("expected BroadcastEvent with empty storeID to return error, got nil")
	}

	// 2. Valid broadcast with storeID must succeed
	err = hub.BroadcastEvent("test.event", "store-123", map[string]string{"msg": "secure"})
	if err != nil {
		t.Errorf("expected BroadcastEvent with valid storeID to succeed, got %v", err)
	}

	// Let hub process event
	time.Sleep(50 * time.Millisecond)
}
