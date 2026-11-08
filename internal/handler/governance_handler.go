package handler

import (
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/service"
)

const PlatformAdminHeader = "X-Platform-Admin-Key"

// GovernanceHandler serves Multi-Tenant SaaS Governance, Legal/UU PDP Consent, Tenant BYOK Midtrans,
// Storefront CMS & i18n, BYO-Email/WhatsApp Notifications, Self-Maintain Hardware/LAN Topology,
// Tax-Safe Account Clearance, and AzureDevID Superadmin Console endpoints (ADR-009).
type GovernanceHandler struct {
	governance       *service.GovernanceService
	platformAdminKey string
}

func NewGovernanceHandler(governance *service.GovernanceService, platformAdminKey string) *GovernanceHandler {
	return &GovernanceHandler{
		governance:       governance,
		platformAdminKey: strings.TrimSpace(platformAdminKey),
	}
}

// RequirePlatformAdmin guards /api/v1/platform/* endpoints using constant-time SHA-256 key comparison.
// Fails closed with 503 when PLATFORM_ADMIN_API_KEY is not configured.
func (h *GovernanceHandler) RequirePlatformAdmin() gin.HandlerFunc {
	return func(c *gin.Context) {
		if h == nil || h.platformAdminKey == "" {
			authError(c, http.StatusServiceUnavailable, "PLATFORM_ADMIN_UNCONFIGURED", "Konsol Superadmin AzureDevID belum dikonfigurasi pada server ini.")
			c.Abort()
			return
		}
		provided := strings.TrimSpace(c.GetHeader(PlatformAdminHeader))
		if provided == "" {
			authError(c, http.StatusUnauthorized, "UNAUTHENTICATED", "Kunci otorisasi platform wajib disertakan.")
			c.Abort()
			return
		}
		expectedDigest := sha256.Sum256([]byte(h.platformAdminKey))
		providedDigest := sha256.Sum256([]byte(provided))
		if subtle.ConstantTimeCompare(expectedDigest[:], providedDigest[:]) != 1 {
			authError(c, http.StatusUnauthorized, "INVALID_PLATFORM_KEY", "Kunci otorisasi platform tidak valid.")
			c.Abort()
			return
		}
		c.Header("Cache-Control", "no-store")
		c.Next()
	}
}

func writeGovernanceError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrGovernanceNotFound):
		authError(c, http.StatusNotFound, "NOT_FOUND", "Data tata kelola tidak ditemukan.")
	case errors.Is(err, service.ErrBYOKEncryptionUnavailable):
		authError(c, http.StatusServiceUnavailable, "BYOK_ENCRYPTION_UNAVAILABLE", "Brankas enkripsi kredensial belum diaktifkan pada server ini.")
	case errors.Is(err, service.ErrBYOKLiveAuthFailed):
		authError(c, http.StatusUnprocessableEntity, "BYOK_MIDTRANS_AUTH_FAILED", "Server Key ditolak oleh Midtrans Core API (401 Unauthorized). Periksa kembali kunci Anda.")
	case errors.Is(err, service.ErrOnboardingOTPExpired):
		authError(c, http.StatusUnprocessableEntity, "OTP_EXPIRED", err.Error())
	case errors.Is(err, service.ErrOnboardingOTPInvalid):
		authError(c, http.StatusUnprocessableEntity, "OTP_INVALID", err.Error())
	case errors.Is(err, service.ErrOnboardingOTPLocked):
		authError(c, http.StatusTooManyRequests, "OTP_ATTEMPTS_EXCEEDED", err.Error())
	case errors.Is(err, service.ErrOnboardingOTPCooldown):
		authError(c, http.StatusTooManyRequests, "OTP_RESEND_COOLDOWN", err.Error())
	case errors.Is(err, service.ErrOnboardingOTPUnverified):
		authError(c, http.StatusUnprocessableEntity, "EMAIL_OTP_UNVERIFIED", err.Error())
	default:
		writeServiceError(c, err)
	}
}

// ============================================================================
// 1. PUBLIC ENDPOINTS
// ============================================================================

// ListPublicLegalDocuments handles GET /api/v1/public/legal?locale=id-ID
func (h *GovernanceHandler) ListPublicLegalDocuments(c *gin.Context) {
	docs, err := h.governance.ListActiveLegalDocuments(c.Request.Context(), c.Query("locale"))
	if err != nil {
		writeGovernanceError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"documents": docs})
}

// ListPublicSubscriptionPlans handles GET /api/v1/public/plans
func (h *GovernanceHandler) ListPublicSubscriptionPlans(c *gin.Context) {
	plans, err := h.governance.ListSubscriptionPlans(c.Request.Context())
	if err != nil {
		writeGovernanceError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"plans": plans})
}

// SubmitOnboardingApplication handles POST /api/v1/public/onboarding/apply
func (h *GovernanceHandler) SubmitOnboardingApplication(c *gin.Context) {
	var in service.OnboardingApplicationInput
	if !bindJSON(c, &in) {
		return
	}
	app, replay, devOTP, err := h.governance.SubmitOnboardingApplication(c.Request.Context(), in)
	if err != nil {
		writeGovernanceError(c, err)
		return
	}
	status := http.StatusCreated
	if replay {
		status = http.StatusOK
	}
	c.Header("Cache-Control", "no-store")
	resp := gin.H{"application": app, "is_idempotent_replay": replay}
	if devOTP != "" {
		resp["dev_otp_preview"] = devOTP
	}
	c.JSON(status, resp)
}

// VerifyOnboardingOTP handles POST /api/v1/public/onboarding/:id/verify-otp
func (h *GovernanceHandler) VerifyOnboardingOTP(c *gin.Context) {
	id, ok := pathID(c, "id")
	if !ok {
		return
	}
	var body struct {
		OTPCode string `json:"otp_code"`
	}
	if !bindJSON(c, &body) {
		return
	}
	app, err := h.governance.VerifyOnboardingEmailOTP(c.Request.Context(), id, body.OTPCode)
	if err != nil {
		writeGovernanceError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"application": app})
}

// ResendOnboardingOTP handles POST /api/v1/public/onboarding/:id/resend-otp
func (h *GovernanceHandler) ResendOnboardingOTP(c *gin.Context) {
	id, ok := pathID(c, "id")
	if !ok {
		return
	}
	expAt, devOTP, err := h.governance.ResendOnboardingEmailOTP(c.Request.Context(), id)
	if err != nil {
		writeGovernanceError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	resp := gin.H{"otp_expires_at": expAt}
	if devOTP != "" {
		resp["dev_otp_preview"] = devOTP
	}
	c.JSON(http.StatusOK, resp)
}

// GetPublicStoreCMS handles GET /api/v1/public/stores/:slug/cms
func (h *GovernanceHandler) GetPublicStoreCMS(c *gin.Context) {
	cms, err := h.governance.GetPublicStoreCMS(c.Request.Context(), c.Param("slug"))
	if err != nil {
		writeGovernanceError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"cms": cms})
}

// ============================================================================
// 2. TENANT OWNER ENDPOINTS (rbac.StoreSettingsManage)
// ============================================================================

// ListTenantLegalAcceptances handles GET /api/v1/store/legal/acceptances
func (h *GovernanceHandler) ListTenantLegalAcceptances(c *gin.Context) {
	rows, err := h.governance.ListTenantLegalAcceptances(c.Request.Context(), currentStaff(c))
	if err != nil {
		writeGovernanceError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"acceptances": rows})
}

// AcceptLegalDocument handles POST /api/v1/store/legal/:id/accept
func (h *GovernanceHandler) AcceptLegalDocument(c *gin.Context) {
	docID, ok := pathID(c, "id")
	if !ok {
		return
	}
	out, err := h.governance.AcceptLegalDocument(c.Request.Context(), currentStaff(c), docID, c.ClientIP(), c.Request.UserAgent())
	if err != nil {
		writeGovernanceError(c, err)
		return
	}
	c.JSON(http.StatusCreated, out)
}

// GetStoreKYB handles GET /api/v1/store/kyb
func (h *GovernanceHandler) GetStoreKYB(c *gin.Context) {
	out, err := h.governance.GetStoreKYBProfile(c.Request.Context(), currentStaff(c))
	if err != nil {
		writeGovernanceError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, out)
}

// UpsertStoreKYB handles PUT /api/v1/store/kyb
func (h *GovernanceHandler) UpsertStoreKYB(c *gin.Context) {
	var in service.StoreKYBUpdateInput
	if !bindJSON(c, &in) {
		return
	}
	out, err := h.governance.UpsertStoreKYBProfile(c.Request.Context(), currentStaff(c), in)
	if err != nil {
		writeGovernanceError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, out)
}

// GetStorePaymentCredentials handles GET /api/v1/store/payment-credentials
func (h *GovernanceHandler) GetStorePaymentCredentials(c *gin.Context) {
	out, err := h.governance.GetStorePaymentCredential(c.Request.Context(), currentStaff(c))
	if err != nil {
		writeGovernanceError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, out)
}

// UpsertStorePaymentCredentials handles PUT /api/v1/store/payment-credentials
func (h *GovernanceHandler) UpsertStorePaymentCredentials(c *gin.Context) {
	var in service.PaymentCredentialInput
	if !bindJSON(c, &in) {
		return
	}
	out, err := h.governance.UpsertStorePaymentCredential(c.Request.Context(), currentStaff(c), in)
	if err != nil {
		writeGovernanceError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, out)
}

// GetStoreCMS handles GET /api/v1/store/cms
func (h *GovernanceHandler) GetStoreCMS(c *gin.Context) {
	out, err := h.governance.GetStoreCMSConfig(c.Request.Context(), currentStaff(c))
	if err != nil {
		writeGovernanceError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

// UpsertStoreCMS handles PUT /api/v1/store/cms
func (h *GovernanceHandler) UpsertStoreCMS(c *gin.Context) {
	var in service.StoreCMSConfigInput
	if !bindJSON(c, &in) {
		return
	}
	out, err := h.governance.UpsertStoreCMSConfig(c.Request.Context(), currentStaff(c), in)
	if err != nil {
		writeGovernanceError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

// GetStoreNotifications handles GET /api/v1/store/notifications
func (h *GovernanceHandler) GetStoreNotifications(c *gin.Context) {
	out, err := h.governance.GetStoreNotificationConfig(c.Request.Context(), currentStaff(c))
	if err != nil {
		writeGovernanceError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, out)
}

// UpsertStoreNotifications handles PUT /api/v1/store/notifications
func (h *GovernanceHandler) UpsertStoreNotifications(c *gin.Context) {
	var in service.StoreNotificationConfigInput
	if !bindJSON(c, &in) {
		return
	}
	out, err := h.governance.UpsertStoreNotificationConfig(c.Request.Context(), currentStaff(c), in)
	if err != nil {
		writeGovernanceError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, out)
}

// ListNetworkNodes handles GET /api/v1/store/network/nodes
func (h *GovernanceHandler) ListNetworkNodes(c *gin.Context) {
	nodes, err := h.governance.ListNetworkNodes(c.Request.Context(), currentStaff(c))
	if err != nil {
		writeGovernanceError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"nodes": nodes})
}

// UpsertNetworkNode handles PUT /api/v1/store/network/nodes
func (h *GovernanceHandler) UpsertNetworkNode(c *gin.Context) {
	var in service.NetworkNodeInput
	if !bindJSON(c, &in) {
		return
	}
	node, err := h.governance.UpsertNetworkNode(c.Request.Context(), currentStaff(c), in)
	if err != nil {
		writeGovernanceError(c, err)
		return
	}
	c.JSON(http.StatusOK, node)
}

// RunNetworkSimulation handles POST /api/v1/store/network/simulate
func (h *GovernanceHandler) RunNetworkSimulation(c *gin.Context) {
	var body struct {
		Scenario string `json:"scenario"`
	}
	if c.Request.ContentLength > 0 && !bindJSON(c, &body) {
		return
	}
	report, err := h.governance.RunNetworkSimulation(c.Request.Context(), currentStaff(c), body.Scenario)
	if err != nil {
		writeGovernanceError(c, err)
		return
	}
	c.JSON(http.StatusOK, report)
}

// GetStoreMikrotikProfile handles GET /api/v1/store/network/mikrotik
func (h *GovernanceHandler) GetStoreMikrotikProfile(c *gin.Context) {
	out, err := h.governance.GetStoreMikrotikProfile(c.Request.Context(), currentStaff(c))
	if err != nil {
		writeGovernanceError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, out)
}

// UpsertStoreMikrotikProfile handles PUT /api/v1/store/network/mikrotik
func (h *GovernanceHandler) UpsertStoreMikrotikProfile(c *gin.Context) {
	var in service.MikrotikProfileInput
	if !bindJSON(c, &in) {
		return
	}
	out, err := h.governance.UpsertStoreMikrotikProfile(c.Request.Context(), currentStaff(c), in)
	if err != nil {
		writeGovernanceError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, out)
}

// GenerateStoreMikrotikRsc handles POST /api/v1/store/network/mikrotik/generate-rsc
func (h *GovernanceHandler) GenerateStoreMikrotikRsc(c *gin.Context) {
	bundle, err := h.governance.GenerateStoreMikrotikRsc(c.Request.Context(), currentStaff(c))
	if err != nil {
		writeGovernanceError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, bundle)
}

// ListStoreSubscriptionInvoices handles GET /api/v1/store/subscription/invoices
func (h *GovernanceHandler) ListStoreSubscriptionInvoices(c *gin.Context) {
	rows, err := h.governance.ListStorePlatformInvoices(c.Request.Context(), currentStaff(c))
	if err != nil {
		writeGovernanceError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"invoices": rows})
}

// CreateStoreSubscriptionInvoice handles POST /api/v1/store/subscription/invoices
func (h *GovernanceHandler) CreateStoreSubscriptionInvoice(c *gin.Context) {
	var in service.CreatePlatformInvoiceInput
	if !bindJSON(c, &in) {
		return
	}
	inv, err := h.governance.CreatePlatformSubscriptionInvoice(c.Request.Context(), currentStaff(c), in)
	if err != nil {
		writeGovernanceError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusCreated, inv)
}

// RequestAccountClearance handles POST /api/v1/store/clearance
func (h *GovernanceHandler) RequestAccountClearance(c *gin.Context) {
	var body struct {
		Reason string `json:"reason"`
	}
	if !bindJSON(c, &body) {
		return
	}
	out, err := h.governance.RequestAccountClearance(c.Request.Context(), currentStaff(c), body.Reason)
	if err != nil {
		writeGovernanceError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusCreated, out)
}

// CancelAccountClearance handles POST /api/v1/store/clearance/:id/cancel
func (h *GovernanceHandler) CancelAccountClearance(c *gin.Context) {
	id, ok := pathID(c, "id")
	if !ok {
		return
	}
	out, err := h.governance.CancelAccountClearance(c.Request.Context(), currentStaff(c), id)
	if err != nil {
		writeGovernanceError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

// ============================================================================
// 3. PLATFORM SUPERADMIN ENDPOINTS (AzureDevID Owner Console)
// ============================================================================

// PlatformOverview handles GET /api/v1/platform/overview
func (h *GovernanceHandler) PlatformOverview(c *gin.Context) {
	out, err := h.governance.GetPlatformOverview(c.Request.Context())
	if err != nil {
		writeGovernanceError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

// ListOnboardingApplications handles GET /api/v1/platform/onboarding
func (h *GovernanceHandler) ListOnboardingApplications(c *gin.Context) {
	rows, err := h.governance.ListOnboardingApplications(c.Request.Context(), c.Query("status"))
	if err != nil {
		writeGovernanceError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"applications": rows})
}

// ReviewOnboardingApplication handles POST /api/v1/platform/onboarding/:id/review
func (h *GovernanceHandler) ReviewOnboardingApplication(c *gin.Context) {
	id, ok := pathID(c, "id")
	if !ok {
		return
	}
	var body struct {
		Status      string `json:"status"`
		ReviewNotes string `json:"review_notes"`
	}
	if !bindJSON(c, &body) {
		return
	}
	out, err := h.governance.ReviewOnboardingApplication(c.Request.Context(), id, body.Status, body.ReviewNotes)
	if err != nil {
		writeGovernanceError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

// ProvisionOnboardingApplication handles POST /api/v1/platform/onboarding/:id/provision
func (h *GovernanceHandler) ProvisionOnboardingApplication(c *gin.Context) {
	id, ok := pathID(c, "id")
	if !ok {
		return
	}
	var in service.ProvisionOnboardingInput
	if !bindJSON(c, &in) {
		return
	}
	out, err := h.governance.ProvisionStoreFromOnboarding(c.Request.Context(), id, in)
	if err != nil {
		writeGovernanceError(c, err)
		return
	}
	status := http.StatusCreated
	if out.AlreadyExisted {
		status = http.StatusOK
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(status, out)
}

// ReviewStoreKYB handles POST /api/v1/platform/tenants/:store_id/kyb-review
func (h *GovernanceHandler) ReviewStoreKYB(c *gin.Context) {
	storeID, ok := pathID(c, "store_id")
	if !ok {
		return
	}
	var body struct {
		KYBStatus          string `json:"kyb_status"`
		SubscriptionStatus string `json:"subscription_status"`
		ReviewNotes        string `json:"review_notes"`
	}
	if !bindJSON(c, &body) {
		return
	}
	out, err := h.governance.ReviewStoreKYB(c.Request.Context(), storeID, body.KYBStatus, body.SubscriptionStatus, body.ReviewNotes)
	if err != nil {
		writeGovernanceError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

// ExecuteTenantClearance handles POST /api/v1/platform/tenants/:store_id/clearance/:id/execute
func (h *GovernanceHandler) ExecuteTenantClearance(c *gin.Context) {
	storeID, ok := pathID(c, "store_id")
	if !ok {
		return
	}
	clearanceID, ok := pathID(c, "id")
	if !ok {
		return
	}
	out, err := h.governance.ExecuteTenantClearance(c.Request.Context(), storeID, clearanceID)
	if err != nil {
		writeGovernanceError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

// MidtransBusinessReviewReadiness handles GET /api/v1/platform/midtrans-readiness
func (h *GovernanceHandler) MidtransBusinessReviewReadiness(c *gin.Context) {
	out, err := h.governance.EvaluateMidtransBusinessReviewReadiness(c.Request.Context())
	if err != nil {
		writeGovernanceError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

// MobileBootstrap handles GET /api/v1/mobile/bootstrap
// Consolidates tablet POS branding, layout orientation, hardware LAN/drawer configuration,
// payment gateway status, and authenticated staff profile.
func (h *GovernanceHandler) MobileBootstrap(c *gin.Context) {
	staff := currentStaff(c)
	if staff == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "UNAUTHENTICATED", "message": "Autentikasi staf diperlukan"})
		return
	}
	out, err := h.governance.GetMobileBootstrap(c.Request.Context(), staff)
	if err != nil {
		writeGovernanceError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store, private")
	c.JSON(http.StatusOK, out)
}
