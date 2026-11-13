package mailer_test

import (
	"strings"
	"testing"
	"time"

	"github.com/s9mcqytn4y-sys/cassy-backend/internal/pkg/mailer"
)

func TestRenderAllTemplates(t *testing.T) {
	data := mailer.Data{
		StoreName: "Kopi <Senja> & Co", StoreSlug: "kopi-senja", RecipientName: "Maya", RecipientEmail: "maya@kafe.test",
		ActorName: "Budi", StaffName: "Doni", RoleLabel: "Kasir", ActionURL: "https://pos.kafe.test/staff/pin#token=abc",
		ActionLabel: "Buat PIN", ExpiresIn: "48 jam", ExpiresAt: "2 Nov 2026 10:00 WIB", EventAt: "31 Okt 2026 10:00 WIB", Year: 2026,
	}
	for _, name := range []string{
		mailer.TemplateStaffInvite,
		mailer.TemplatePINReset,
		mailer.TemplatePINChanged,
		mailer.TemplateAccountLocked,
		mailer.TemplateOwnerPINAttempts,
		mailer.TemplateReceipt,
		mailer.TemplateInvoiceReminder,
		mailer.TemplateOnboardingOTP,
		mailer.TemplateOnboardingProvisioned,
	} {
		r, err := mailer.Render(name, data)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if r.Subject == "" || strings.Contains(r.Subject, "&amp;") || strings.ContainsAny(r.Subject, "\r\n") {
			t.Errorf("%s: bad subject %q", name, r.Subject)
		}
		if strings.Contains(r.HTML, "<Senja>") || !strings.Contains(r.HTML, "Kopi &lt;Senja&gt; &amp; Co") {
			t.Errorf("%s: store name must be HTML-escaped", name)
		}
		if !strings.Contains(r.Text, "Kopi <Senja> & Co") {
			t.Errorf("%s: text body must keep the raw store name", name)
		}
	}
	invite, _ := mailer.Render(mailer.TemplateStaffInvite, data)
	if !strings.Contains(invite.HTML, data.ActionURL) || !strings.Contains(invite.Text, data.ActionURL) {
		t.Fatal("invite must contain the action link in both bodies")
	}
}

func TestBuildMIMEPreventsHeaderInjection(t *testing.T) {
	raw, err := mailer.BuildMIME("noreply@kafe.test", "Kopi Senja\r\nBcc: victim@evil.test", mailer.Message{
		To: "maya@kafe.test", ToName: "Maya", ReplyTo: "owner@kafe.test",
		Subject: "Halo\r\nBcc: attacker@evil.test", HTML: "<p>Hai</p>", Text: "Hai",
	}, time.Date(2026, 10, 31, 10, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	headers, _, _ := strings.Cut(string(raw), "\r\n\r\n")
	for _, line := range strings.Split(headers, "\r\n") {
		if strings.HasPrefix(strings.ToLower(line), "bcc:") {
			t.Fatalf("header injection produced a Bcc header: %q", line)
		}
	}
	for _, want := range []string{"multipart/alternative", "text/plain; charset=UTF-8", "text/html; charset=UTF-8", "Reply-To: <owner@kafe.test>", "Message-ID: <"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("MIME output missing %q", want)
		}
	}
}
