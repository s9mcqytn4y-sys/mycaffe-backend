package whatsapp

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/sms"
)

// ErrNoSenderConfigured indicates no valid sender driver is available.
var ErrNoSenderConfigured = errors.New("no whatsapp sender driver configured")

// Notification holds destination and parameter content for an outbound message.
type Notification struct {
	To           string       // E.164 phone number, e.g. +6281234567890
	TemplateName TemplateName // Name of the approved template
	BodyParams   string       // Newline-delimited parameters for template
	RawMessage   string       // Optional human-readable fallback message
}

// Dispatcher manages resilient multi-tiered delivery:
// 1. Primary: Local WA Bridge (whatsmeow / cost-free)
// 2. Secondary: Meta WhatsApp Cloud API (Graph API)
// 3. Tertiary: SMS / Email bridge fallback
type Dispatcher struct {
	bridgeSender sms.Sender
	cloudSender  sms.Sender
	smsFallback  sms.Sender
}

// NewDispatcher constructs a Dispatcher with configurable sender drivers.
func NewDispatcher(bridge, cloud, fallback sms.Sender) *Dispatcher {
	return &Dispatcher{
		bridgeSender: bridge,
		cloudSender:  cloud,
		smsFallback:  fallback,
	}
}

// Dispatch attempts delivery across the priority tiers with graceful failover.
func (d *Dispatcher) Dispatch(ctx context.Context, n Notification) error {
	phone := strings.TrimSpace(n.To)
	if phone == "" {
		return fmt.Errorf("%w: empty recipient phone number", sms.ErrPermanent)
	}

	body := n.BodyParams
	if body == "" {
		body = n.RawMessage
	}

	msg := sms.Message{
		To:   phone,
		Body: body,
	}

	var tierErrors []string

	// Tier 1: Local WA Bridge (whatsmeow)
	if d.bridgeSender != nil {
		err := d.bridgeSender.Send(ctx, msg)
		if err == nil {
			return nil
		}
		if errors.Is(err, sms.ErrPermanent) {
			return err
		}
		log.Printf("[WA DISPATCHER] Tier 1 (WA Bridge) failed: %v. Escalating to Tier 2 (Cloud API)...", err)
		tierErrors = append(tierErrors, fmt.Sprintf("bridge: %v", err))
	}

	// Tier 2: Meta WhatsApp Cloud API
	if d.cloudSender != nil {
		err := d.cloudSender.Send(ctx, msg)
		if err == nil {
			return nil
		}
		if errors.Is(err, sms.ErrPermanent) {
			return err
		}
		log.Printf("[WA DISPATCHER] Tier 2 (Cloud API) failed: %v. Escalating to Tier 3 (SMS/Email)...", err)
		tierErrors = append(tierErrors, fmt.Sprintf("cloud: %v", err))
	}

	// Tier 3: SMS / Email Fallback
	if d.smsFallback != nil {
		fallbackMsg := msg
		if n.RawMessage != "" {
			fallbackMsg.Body = n.RawMessage
		}
		err := d.smsFallback.Send(ctx, fallbackMsg)
		if err == nil {
			return nil
		}
		tierErrors = append(tierErrors, fmt.Sprintf("fallback_sms: %v", err))
		return fmt.Errorf("all notification tiers exhausted: %s", strings.Join(tierErrors, "; "))
	}

	if len(tierErrors) == 0 {
		return ErrNoSenderConfigured
	}

	return fmt.Errorf("all configured whatsapp tiers failed: %s", strings.Join(tierErrors, "; "))
}
