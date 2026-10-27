package handler_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
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

func TestOrderHandlerProductionFailClosed(t *testing.T) {
	cfg := &config.Config{
		AppEnv:    "production",
		JWTSecret: "strong_production_secret_key_32_characters_long",
	}
	r := setupTestOrderRouter(cfg)

	// 1. Order creation in production must fail-closed with 503
	reqBody := []byte(`{"store_id":"` + uuid.New().String() + `","customer_name":"Budi","order_type":"dine_in","payment_method":"qris","items":[{"product_id":"` + uuid.New().String() + `","quantity":1}]}`)
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/orders", bytes.NewBuffer(reqBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected production order creation to fail with 503 Service Unavailable, got %d", w.Code)
	}

	// 2. WebSocket in production must fail-closed with 503
	wsReq, _ := http.NewRequest(http.MethodGet, "/ws?store_id="+uuid.New().String(), nil)
	wsW := httptest.NewRecorder()
	r.ServeHTTP(wsW, wsReq)

	if wsW.Code != http.StatusServiceUnavailable {
		t.Errorf("expected production WebSocket to fail-closed with 503, got %d", wsW.Code)
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

	reqMismatch, _ := http.NewRequest(http.MethodGet, "/ws?token="+token+"&store_id="+storeB, nil)
	wMismatch := httptest.NewRecorder()
	r.ServeHTTP(wMismatch, reqMismatch)

	if wMismatch.Code != http.StatusForbidden {
		t.Errorf("expected 403 Forbidden for tenant store_id mismatch, got %d", wMismatch.Code)
	}
}
