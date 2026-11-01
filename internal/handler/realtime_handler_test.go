package handler_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/handler"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/realtime"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/service"
)

// /ws accepts only a ticket; bearer tokens and token query parameters are ignored (ADR-003 F3).
func TestWebSocketRequiresTicket(t *testing.T) {
	gin.SetMode(gin.TestMode)
	hub := realtime.NewHub([]string{"http://localhost:3000"})
	go hub.Run()
	t.Cleanup(hub.Shutdown)
	r := gin.New()
	r.GET("/ws", handler.NewRealtimeHandler(hub, service.NewRealtimeService(nil)).Connect)

	for name, build := range map[string]func() *http.Request{
		"no ticket": func() *http.Request { return httptest.NewRequest(http.MethodGet, "/ws", nil) },
		"bearer header only": func() *http.Request {
			req := httptest.NewRequest(http.MethodGet, "/ws", nil)
			req.Header.Set("Authorization", "Bearer some.jwt.token")
			return req
		},
		"token query": func() *http.Request { return httptest.NewRequest(http.MethodGet, "/ws?token=some.jwt.token", nil) },
		"malformed ticket": func() *http.Request {
			return httptest.NewRequest(http.MethodGet, "/ws?ticket=not-a-ticket", nil)
		},
		"nil store ticket": func() *http.Request {
			return httptest.NewRequest(http.MethodGet, "/ws?ticket="+uuid.Nil.String()+"."+uuid.NewString()+".secret", nil)
		},
	} {
		t.Run(name, func(t *testing.T) {
			w := httptest.NewRecorder()
			r.ServeHTTP(w, build())
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("expected 401, got %d: %s", w.Code, w.Body.String())
			}
		})
	}
}
