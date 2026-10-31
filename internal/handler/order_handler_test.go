package handler_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/config"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/handler"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/auth"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/midtrans"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/realtime"
)

func setupTestOrderRouter(cfg *config.Config) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	hub := realtime.NewHub(cfg.AllowedOrigins)
	go hub.Run()
	midtransClient := midtrans.NewClient(cfg)
	orderHandler := handler.NewOrderHandler(cfg, midtransClient, hub)

	r.POST("/api/v1/orders", orderHandler.CreateOrder)
	r.GET("/ws", orderHandler.HandleWebSocket)
	return r
}

func TestOrderHandlerProductionAndStagingFailClosed(t *testing.T) {
	for _, env := range []string{"production", "staging"} {
		t.Run(env, func(t *testing.T) {
			cfg := &config.Config{
				AppEnv:    env,
				JWTSecret: "strong_production_secret_key_32_characters_long",
			}
			r := setupTestOrderRouter(cfg)

			// 1. Order creation in staging/production must fail-closed with 503
			reqBody := []byte(`{"store_id":"` + uuid.New().String() + `","customer_name":"Budi","order_type":"dine_in","payment_method":"qris","items":[{"product_id":"` + uuid.New().String() + `","quantity":1}]}`)
			req, _ := http.NewRequest(http.MethodPost, "/api/v1/orders", bytes.NewBuffer(reqBody))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			if w.Code != http.StatusServiceUnavailable {
				t.Errorf("[%s] expected order creation to fail with 503 Service Unavailable, got %d", env, w.Code)
			}
			if strings.Contains(w.Body.String(), "[P0 CONTAINMENT]") {
				t.Errorf("[%s] client response leaked internal diagnostic string: %s", env, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), "request_id") {
				t.Errorf("[%s] expected request_id in 503 response, got: %s", env, w.Body.String())
			}

			// 2. WebSocket in staging/production must fail-closed with 503
			wsReq, _ := http.NewRequest(http.MethodGet, "/ws?store_id="+uuid.New().String(), nil)
			wsW := httptest.NewRecorder()
			r.ServeHTTP(wsW, wsReq)

			if wsW.Code != http.StatusServiceUnavailable {
				t.Errorf("[%s] expected WebSocket to fail-closed with 503, got %d", env, wsW.Code)
			}
			if strings.Contains(wsW.Body.String(), "[P0 CONTAINMENT]") {
				t.Errorf("[%s] WebSocket response leaked internal diagnostic string: %s", env, wsW.Body.String())
			}
		})
	}
}

func TestWebSocketAuthentication(t *testing.T) {
	secret := "secret_key_for_testing_purposes_only_32_chars"
	cfg := &config.Config{
		AppEnv:         "development",
		JWTSecret:      secret,
		AllowedOrigins: []string{"http://localhost:3000"},
	}
	r := setupTestOrderRouter(cfg)

	// 1. Missing token -> 401 Unauthorized
	req, _ := http.NewRequest(http.MethodGet, "/ws", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 Unauthorized for missing token, got %d", w.Code)
	}

	// 2. Token with tenant mismatch -> 403 Forbidden
	storeA := uuid.New().String()
	storeB := uuid.New().String()
	token, _ := auth.GenerateTestToken(storeA, uuid.New().String(), "cashier", secret, 1*time.Hour)

	reqMismatch, _ := http.NewRequest(http.MethodGet, "/ws?store_id="+storeB, nil)
	reqMismatch.Header.Set("Authorization", "Bearer "+token)
	wMismatch := httptest.NewRecorder()
	r.ServeHTTP(wMismatch, reqMismatch)
	if wMismatch.Code != http.StatusForbidden {
		t.Errorf("expected 403 Forbidden for store_id mismatch, got %d", wMismatch.Code)
	}

	// 3. Invalid / malformed token -> 401 Unauthorized with generic error message
	reqInvalid, _ := http.NewRequest(http.MethodGet, "/ws", nil)
	reqInvalid.Header.Set("Authorization", "Bearer malformed.invalid.token")
	wInvalid := httptest.NewRecorder()
	r.ServeHTTP(wInvalid, reqInvalid)

	if wInvalid.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 Unauthorized for invalid token, got %d", wInvalid.Code)
	}
	if !strings.Contains(wInvalid.Body.String(), "Unauthorized: invalid or expired token") {
		t.Errorf("expected generic unauthorized message, got %s", wInvalid.Body.String())
	}
}

// Access tokens in the query string are rejected (ADR-003 F3): they would land in proxy logs.
func TestWebSocketRejectsQueryToken(t *testing.T) {
	secret := "secret_key_for_testing_purposes_only_32_chars"
	cfg := &config.Config{AppEnv: "development", JWTSecret: secret, AllowedOrigins: []string{"http://localhost:3000"}}
	r := setupTestOrderRouter(cfg)
	token, _ := auth.GenerateTestToken(uuid.New().String(), uuid.New().String(), "cashier", secret, time.Hour)

	req, _ := http.NewRequest(http.MethodGet, "/ws?token="+token, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("query-string token must be ignored (401), got %d", w.Code)
	}
}
