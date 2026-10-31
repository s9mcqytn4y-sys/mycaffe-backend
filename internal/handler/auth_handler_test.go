package handler_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/handler"
)

// These paths are rejected before the service is touched, so a nil service is enough.
func TestAuthHandlerRejectsBeforeService(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := handler.NewAuthHandler(nil)
	r := gin.New()
	r.POST("/api/v1/auth/staff/login", h.Login)
	r.POST("/api/v1/auth/staff/refresh", h.Refresh)
	r.POST("/api/v1/auth/staff/logout", h.Logout)
	r.GET("/api/v1/auth/staff/me", h.Me)

	cases := []struct {
		name, method, path, body string
		headers                  map[string]string
		want                     int
		code                     string
	}{
		{"login_malformed_json", http.MethodPost, "/api/v1/auth/staff/login", `{"store_slug":`, nil, http.StatusBadRequest, "INVALID_REQUEST"},
		{"login_missing_pin", http.MethodPost, "/api/v1/auth/staff/login", `{"store_slug":"central-cafe","email":"a@b.c"}`, nil, http.StatusBadRequest, "INVALID_REQUEST"},
		{"refresh_without_csrf_header", http.MethodPost, "/api/v1/auth/staff/refresh", "", map[string]string{"Cookie": handler.RefreshCookieName + "=x"}, http.StatusForbidden, "CSRF_HEADER_REQUIRED"},
		{"refresh_without_cookie", http.MethodPost, "/api/v1/auth/staff/refresh", "", map[string]string{handler.CSRFHeader: "1"}, http.StatusUnauthorized, "SESSION_INVALID"},
		{"logout_without_csrf_header", http.MethodPost, "/api/v1/auth/staff/logout", "", nil, http.StatusForbidden, "CSRF_HEADER_REQUIRED"},
		{"me_without_bearer", http.MethodGet, "/api/v1/auth/staff/me", "", nil, http.StatusUnauthorized, "UNAUTHENTICATED"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			for k, v := range tc.headers {
				req.Header.Set(k, v)
			}
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != tc.want || !strings.Contains(w.Body.String(), tc.code) {
				t.Fatalf("got %d %s, want %d %s", w.Code, w.Body.String(), tc.want, tc.code)
			}
		})
	}

	// Logout without a cookie (but with the CSRF header) clears the cookie and returns 204.
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/staff/logout", nil)
	req.Header.Set(handler.CSRFHeader, "1")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	cookie := w.Header().Get("Set-Cookie")
	if w.Code != http.StatusNoContent || !strings.Contains(cookie, "Max-Age=0") || !strings.Contains(cookie, "HttpOnly") ||
		!strings.Contains(cookie, "Secure") || !strings.Contains(cookie, "SameSite=Strict") || !strings.Contains(cookie, "Path=/api/v1/auth/staff") {
		t.Fatalf("logout: %d Set-Cookie=%q", w.Code, cookie)
	}
}
