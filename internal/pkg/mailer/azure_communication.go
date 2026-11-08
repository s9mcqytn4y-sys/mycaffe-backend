package mailer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// AzureCommunicationSender delivers transactional email via Azure Communication Services Email REST API.
type AzureCommunicationSender struct {
	endpoint   string
	accessKey  string
	senderFrom string
	client     *http.Client
}

// NewAzureCommunicationSender constructs an Azure Communication Services email sender.
func NewAzureCommunicationSender(endpoint, accessKey, senderFrom string) (*AzureCommunicationSender, error) {
	ep := strings.TrimRight(strings.TrimSpace(endpoint), "/")
	if ep == "" || !strings.HasPrefix(ep, "https://") {
		return nil, errors.New("azure communication sender requires a valid https endpoint")
	}
	key := strings.TrimSpace(accessKey)
	if key == "" {
		return nil, errors.New("azure communication sender requires an access key")
	}
	from := strings.TrimSpace(senderFrom)
	if from == "" {
		from = "DoNotReply@azuredev.id"
	}
	return &AzureCommunicationSender{
		endpoint:   ep,
		accessKey:  key,
		senderFrom: from,
		client:     &http.Client{Timeout: 15 * time.Second},
	}, nil
}

type acsRecipient struct {
	Address     string `json:"address"`
	DisplayName string `json:"displayName,omitempty"`
}

type acsContent struct {
	Subject   string `json:"subject"`
	PlainText string `json:"plainText,omitempty"`
	HTML      string `json:"html,omitempty"`
}

type acsEmailPayload struct {
	SenderAddress string `json:"senderAddress"`
	Content       acsContent `json:"content"`
	Recipients    struct {
		To []acsRecipient `json:"to"`
	} `json:"recipients"`
}

// Send sends the email through ACS Email API.
func (a *AzureCommunicationSender) Send(ctx context.Context, msg Message) error {
	to := strings.TrimSpace(msg.To)
	if to == "" {
		return fmt.Errorf("%w: empty recipient address", ErrPermanent)
	}

	payload := acsEmailPayload{
		SenderAddress: a.senderFrom,
		Content: acsContent{
			Subject:   msg.Subject,
			PlainText: msg.Text,
			HTML:      msg.HTML,
		},
	}
	payload.Recipients.To = []acsRecipient{{Address: to, DisplayName: msg.ToName}}

	rawJSON, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal acs payload: %w", err)
	}

	targetURL := fmt.Sprintf("%s/emails:send?api-version=2023-03-31", a.endpoint)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(rawJSON))
	if err != nil {
		return fmt.Errorf("create acs request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+a.accessKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("acs http request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusAccepted {
		return nil
	}

	bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	if resp.StatusCode >= 400 && resp.StatusCode < 500 {
		return fmt.Errorf("%w: acs email returned %d: %s", ErrPermanent, resp.StatusCode, string(bodyBytes))
	}

	return fmt.Errorf("acs email returned status %d: %s", resp.StatusCode, string(bodyBytes))
}
