package sms

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// WhatsAppSender sends approved template messages through the WhatsApp Cloud API (decision W9):
// POST https://graph.facebook.com/{version}/{phone_number_id}/messages. Business-initiated messages
// must use a template approved by Meta; Message.Body holds the template's body parameters, one per
// line ({{1}}, {{2}}, ...).
type WhatsAppSender struct {
	BaseURL       string // https://graph.facebook.com, overridable in tests
	APIVersion    string
	PhoneNumberID string
	AccessToken   string
	Template      string
	Language      string
	Client        *http.Client
}

func NewWhatsAppSender(apiVersion, phoneNumberID, accessToken, template, language string) (*WhatsAppSender, error) {
	w := &WhatsAppSender{
		BaseURL: "https://graph.facebook.com", APIVersion: strings.TrimSpace(apiVersion),
		PhoneNumberID: strings.TrimSpace(phoneNumberID), AccessToken: strings.TrimSpace(accessToken),
		Template: strings.TrimSpace(template), Language: strings.TrimSpace(language),
		Client: &http.Client{Timeout: 10 * time.Second},
	}
	if w.PhoneNumberID == "" || w.AccessToken == "" || w.Template == "" || w.Language == "" || w.APIVersion == "" {
		return nil, errors.New("whatsapp sender needs api version, phone number id, access token, template and language")
	}
	return w, nil
}

func (w *WhatsAppSender) Send(ctx context.Context, m Message) error {
	type param struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	params := []param{}
	for _, line := range strings.Split(m.Body, "\n") {
		params = append(params, param{Type: "text", Text: line})
	}
	payload, err := json.Marshal(map[string]any{
		"messaging_product": "whatsapp",
		"to":                strings.TrimPrefix(m.To, "+"),
		"type":              "template",
		"template": map[string]any{
			"name":       cmp.Or(m.Template, w.Template),
			"language":   map[string]string{"code": w.Language},
			"components": []map[string]any{{"type": "body", "parameters": params}},
		},
	})
	if err != nil {
		return fmt.Errorf("%w: marshal payload: %v", ErrPermanent, err)
	}
	url := fmt.Sprintf("%s/%s/%s/messages", strings.TrimRight(w.BaseURL, "/"), w.APIVersion, w.PhoneNumberID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("%w: create request: %v", ErrPermanent, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+w.AccessToken)
	resp, err := w.Client.Do(req)
	if err != nil {
		return fmt.Errorf("whatsapp post failed: %w", err)
	}
	defer resp.Body.Close()
	// Meta's error body names the problem (template not approved, recipient not opted in); no secrets.
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return nil
	case resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != http.StatusTooManyRequests:
		return fmt.Errorf("%w: whatsapp returned status %d: %s", ErrPermanent, resp.StatusCode, body)
	}
	return fmt.Errorf("whatsapp returned transient status %d", resp.StatusCode)
}
