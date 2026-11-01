package handler

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/metrics"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/service"
)

func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

func TestAccessLogUsesRouteTemplateAndStaffContext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	logs := captureLogs(t)
	storeID, userID := uuid.New(), uuid.New()

	r := gin.New()
	r.Use(RequestID(), AccessLog())
	r.GET("/public/qr/:token", func(c *gin.Context) {
		c.Set(staffContextKey, &service.AuthenticatedStaff{Staff: service.StaffIdentity{
			UserID: userID, Store: service.StaffStore{ID: storeID},
		}})
		c.Status(http.StatusTeapot)
	})
	before := testutil.ToFloat64(metrics.HTTPRequests.WithLabelValues("GET", "/public/qr/:token", "418"))

	req := httptest.NewRequest(http.MethodGet, "/public/qr/secret-qr-token?ticket=secret-ticket", nil)
	r.ServeHTTP(httptest.NewRecorder(), req)

	line := logs.String()
	if strings.Contains(line, "secret-qr-token") || strings.Contains(line, "secret-ticket") {
		t.Fatalf("access log leaked a path or query token: %s", line)
	}
	var entry map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(line)), &entry); err != nil {
		t.Fatalf("access log is not one JSON line: %v (%s)", err, line)
	}
	if entry["route"] != "/public/qr/:token" || entry["status"] != float64(418) ||
		entry["store_id"] != storeID.String() || entry["staff_id"] != userID.String() || entry["request_id"] == "" {
		t.Fatalf("unexpected access log fields: %v", entry)
	}
	if got := testutil.ToFloat64(metrics.HTTPRequests.WithLabelValues("GET", "/public/qr/:token", "418")); got != before+1 {
		t.Fatalf("request counter = %v, want %v", got, before+1)
	}
}

func TestRecoverReturns500AndLogsPanic(t *testing.T) {
	gin.SetMode(gin.TestMode)
	logs := captureLogs(t)

	r := gin.New()
	r.Use(RequestID(), Recover())
	r.GET("/boom", func(c *gin.Context) { panic("kaboom") })

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/boom", nil))

	if w.Code != http.StatusInternalServerError || !strings.Contains(w.Body.String(), "INTERNAL_ERROR") {
		t.Fatalf("got %d %s, want 500 INTERNAL_ERROR", w.Code, w.Body.String())
	}
	if !strings.Contains(logs.String(), `"msg":"panic"`) || !strings.Contains(logs.String(), "kaboom") {
		t.Fatalf("panic not logged: %s", logs.String())
	}
}
