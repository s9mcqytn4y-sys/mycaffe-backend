package whatsapp_test

import (
	"context"
	"errors"
	"testing"

	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/sms"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/whatsapp"
)

type mockSender struct {
	sent   []sms.Message
	retErr error
}

func (m *mockSender) Send(_ context.Context, msg sms.Message) error {
	if m.retErr != nil {
		return m.retErr
	}
	m.sent = append(m.sent, msg)
	return nil
}

func TestDispatcher_Tier1_Success(t *testing.T) {
	bridge := &mockSender{}
	cloud := &mockSender{}
	fallback := &mockSender{}

	d := whatsapp.NewDispatcher(bridge, cloud, fallback)
	notif := whatsapp.Notification{
		To:           "+6281234567890",
		TemplateName: whatsapp.TemplateOrderReceipt,
		BodyParams:   "Pelanggan\nORD-001\nCassy Cafe\nRp 50.000\nQRIS\nhttps://receipt.example.com",
	}

	err := d.Dispatch(context.Background(), notif)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if len(bridge.sent) != 1 {
		t.Errorf("expected bridge to receive 1 message, got %d", len(bridge.sent))
	}
	if len(cloud.sent) != 0 {
		t.Errorf("expected cloud not to be invoked")
	}
}

func TestDispatcher_FailoverToTier2(t *testing.T) {
	bridge := &mockSender{retErr: errors.New("bridge timeout")}
	cloud := &mockSender{}
	fallback := &mockSender{}

	d := whatsapp.NewDispatcher(bridge, cloud, fallback)
	notif := whatsapp.Notification{
		To:         "+6281234567890",
		BodyParams: "Test body",
	}

	err := d.Dispatch(context.Background(), notif)
	if err != nil {
		t.Fatalf("expected successful failover to tier 2, got: %v", err)
	}

	if len(cloud.sent) != 1 {
		t.Errorf("expected cloud to receive 1 message on failover")
	}
	if len(fallback.sent) != 0 {
		t.Errorf("expected fallback not to be invoked when tier 2 succeeds")
	}
}

func TestDispatcher_FailoverToTier3(t *testing.T) {
	bridge := &mockSender{retErr: errors.New("bridge 500")}
	cloud := &mockSender{retErr: errors.New("cloud rate limit")}
	fallback := &mockSender{}

	d := whatsapp.NewDispatcher(bridge, cloud, fallback)
	notif := whatsapp.Notification{
		To:         "+6281234567890",
		BodyParams: "Test body",
		RawMessage: "Human readable fallback message",
	}

	err := d.Dispatch(context.Background(), notif)
	if err != nil {
		t.Fatalf("expected successful failover to tier 3, got: %v", err)
	}

	if len(fallback.sent) != 1 {
		t.Fatalf("expected fallback to receive message")
	}
	if fallback.sent[0].Body != "Human readable fallback message" {
		t.Errorf("expected raw message in fallback, got: %s", fallback.sent[0].Body)
	}
}

func TestDispatcher_PermanentError_FailsFast(t *testing.T) {
	bridge := &mockSender{retErr: sms.ErrPermanent}
	cloud := &mockSender{}

	d := whatsapp.NewDispatcher(bridge, cloud, nil)
	notif := whatsapp.Notification{
		To:         "+6281234567890",
		BodyParams: "Test",
	}

	err := d.Dispatch(context.Background(), notif)
	if !errors.Is(err, sms.ErrPermanent) {
		t.Errorf("expected ErrPermanent, got: %v", err)
	}
	if len(cloud.sent) != 0 {
		t.Errorf("expected cloud not to be called on permanent error")
	}
}
