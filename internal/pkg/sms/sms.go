// Package sms delivers text messages (decision V5): one Sender interface with a log driver for
// development and test; provider drivers plug in behind the same interface.
package sms

import (
	"context"
	"errors"
	"log"
	"regexp"
	"strings"
)

// ErrPermanent marks a failure that retrying cannot fix (invalid number, rejected sender).
var ErrPermanent = errors.New("permanent sms failure")

// ErrInvalidPhone means the input is not a usable phone number.
var ErrInvalidPhone = errors.New("invalid phone number")

type Message struct {
	To   string // E.164, e.g. +6281234567890
	Body string
}

type Sender interface {
	Send(ctx context.Context, m Message) error
}

// LogSender prints messages instead of sending them. Config allows it only in development and
// test, because OTP bodies are secrets.
type LogSender struct{}

func (LogSender) Send(_ context.Context, m Message) error {
	log.Printf("[SMS LOG DRIVER] to=%s body=%q", m.To, m.Body)
	return nil
}

var e164 = regexp.MustCompile(`^\+[1-9][0-9]{7,14}$`)

// NormalizePhone turns Indonesian inputs (0812…, 62812…, +62 812-…) into E.164 and accepts other
// E.164 numbers as they are.
func NormalizePhone(raw string) (string, error) {
	s := strings.NewReplacer(" ", "", "-", "", "(", "", ")", "", ".", "").Replace(strings.TrimSpace(raw))
	switch {
	case strings.HasPrefix(s, "+"):
	case strings.HasPrefix(s, "62"):
		s = "+" + s
	case strings.HasPrefix(s, "0"):
		s = "+62" + s[1:]
	default:
		return "", ErrInvalidPhone
	}
	if !e164.MatchString(s) || (strings.HasPrefix(s, "+62") && !strings.HasPrefix(s, "+628")) {
		return "", ErrInvalidPhone // Indonesian mobile numbers start with 8 after the country code
	}
	return s, nil
}
