package mailer

import (
	"bytes"
	"embed"
	"fmt"
	stdhtml "html"
	htmltemplate "html/template"
	"strings"
	texttemplate "text/template"
)

//go:embed templates/*.html templates/*.txt
var templateFS embed.FS

// Template names. Each has templates/<name>.html (subject, preheader, html_body) and <name>.txt.
const (
	TemplateStaffInvite           = "staff_invite"
	TemplatePINReset              = "pin_reset"
	TemplatePINChanged            = "pin_changed"
	TemplateAccountLocked         = "account_locked"
	TemplateOwnerPINAttempts      = "owner_pin_attempts"
	TemplateReceipt               = "receipt"
	TemplateOnboardingOTP         = "onboarding_otp"
	TemplateOnboardingProvisioned = "onboarding_provisioned"
)

// ReceiptItem represents an itemized line in a digital receipt.
type ReceiptItem struct {
	Name          string
	Details       string
	Quantity      int
	SubtotalLabel string
}

// Data is the template input. Fields a template does not use are ignored.
type Data struct {
	StoreName      string
	StoreSlug      string
	ReplyTo        string
	RecipientName  string
	RecipientEmail string
	ActorName      string
	StaffName      string
	RoleLabel      string
	ActionURL      string
	ActionLabel    string
	OTPCode        string
	PlanLabel      string
	ExpiresIn      string
	ExpiresAt      string
	EventAt        string
	OrderNumber    string
	TotalLabel     string
	Year           int

	// Rich Receipt fields
	IssuedAt           string
	PaymentMethod      string
	ReferenceID        string
	CashierName        string
	Items              []ReceiptItem
	SubtotalItemsLabel string
	DiscountLabel      string
	ServiceFeeLabel    string
	TaxLabel           string
}

type Rendered struct {
	Subject string
	HTML    string
	Text    string
}

// Render produces subject, HTML (auto-escaped by html/template) and plain-text bodies.
func Render(name string, data Data) (Rendered, error) {
	htmlTmpl, err := htmltemplate.ParseFS(templateFS, "templates/layout.html", "templates/"+name+".html")
	if err != nil {
		return Rendered{}, fmt.Errorf("parse %s html template: %w", name, err)
	}
	var subject, html bytes.Buffer
	if err := htmlTmpl.ExecuteTemplate(&subject, "subject", data); err != nil {
		return Rendered{}, fmt.Errorf("render %s subject: %w", name, err)
	}
	if err := htmlTmpl.ExecuteTemplate(&html, "layout", data); err != nil {
		return Rendered{}, fmt.Errorf("render %s html: %w", name, err)
	}

	textTmpl, err := texttemplate.ParseFS(templateFS, "templates/"+name+".txt")
	if err != nil {
		return Rendered{}, fmt.Errorf("parse %s text template: %w", name, err)
	}
	var text bytes.Buffer
	if err := textTmpl.Execute(&text, data); err != nil {
		return Rendered{}, fmt.Errorf("render %s text: %w", name, err)
	}
	// html/template escapes the subject for HTML; the Subject header needs the plain string.
	return Rendered{
		Subject: stdhtml.UnescapeString(strings.TrimSpace(subject.String())),
		HTML:    html.String(),
		Text:    text.String(),
	}, nil
}
