package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/service"
)

// Each payment attempt opens a gateway charge, so one guest session gets at most 20 per window (F1).
func TestInitiatePaymentLimitedPerGuestSession(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewGuestHandler(nil)
	session := uuid.New()
	r := gin.New()
	r.POST("/orders/:id/payments", func(c *gin.Context) {
		c.Set(guestContextKey, &service.GuestIdentity{StoreID: uuid.New(), SessionID: session})
	}, h.InitiatePayment)

	post := func() int {
		w := httptest.NewRecorder()
		// An empty body stops at validation, so the service (nil here) is never reached.
		req := httptest.NewRequest(http.MethodPost, "/orders/"+uuid.NewString()+"/payments", strings.NewReader(""))
		r.ServeHTTP(w, req)
		return w.Code
	}
	for i := 0; i < 20; i++ {
		if code := post(); code == http.StatusTooManyRequests {
			t.Fatalf("attempt %d blocked too early", i+1)
		}
	}
	if code := post(); code != http.StatusTooManyRequests {
		t.Fatalf("attempt 21 must be limited, got %d", code)
	}
	other := &service.GuestIdentity{SessionID: uuid.New()}
	if h.paymentsPerGuest.Blocked(other.SessionID.String()) {
		t.Fatal("another session must not share the limit")
	}
}
