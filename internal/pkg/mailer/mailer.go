// Package mailer delivers rendered emails over SMTP (owner decision E5): Gmail with an app
// password for dev/demo, the Hostinger mailbox (smtp.hostinger.com:465) for production.
// It uses only the standard library.
package mailer

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"strconv"
	"strings"
	"time"
)

type Message struct {
	To      string
	ToName  string
	ReplyTo string
	Subject string
	HTML    string
	Text    string
}

type Sender interface {
	Send(ctx context.Context, msg Message) error
}

// ErrPermanent marks failures that retrying cannot fix (bad recipient, rejected message).
var ErrPermanent = errors.New("permanent email delivery failure")

type SMTPConfig struct {
	Host     string
	Port     int
	Username string
	Password string
	TLSMode  string // "starttls" or "implicit"
	From     string
	FromName string
	Timeout  time.Duration
}

type SMTPSender struct {
	cfg SMTPConfig
}

func NewSMTPSender(cfg SMTPConfig) *SMTPSender {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 30 * time.Second
	}
	return &SMTPSender{cfg: cfg}
}

func (s *SMTPSender) Send(ctx context.Context, msg Message) error {
	if _, err := mail.ParseAddress(msg.To); err != nil {
		return fmt.Errorf("%w: invalid recipient", ErrPermanent)
	}
	raw, err := BuildMIME(s.cfg.From, s.cfg.FromName, msg, time.Now())
	if err != nil {
		return fmt.Errorf("%w: %v", ErrPermanent, err)
	}

	addr := net.JoinHostPort(s.cfg.Host, strconv.Itoa(s.cfg.Port))
	deadline := time.Now().Add(s.cfg.Timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	dialer := &net.Dialer{Deadline: deadline}
	tlsConfig := &tls.Config{ServerName: s.cfg.Host, MinVersion: tls.VersionTLS12}

	var conn net.Conn
	if s.cfg.TLSMode == "implicit" {
		conn, err = tls.DialWithDialer(dialer, "tcp", addr, tlsConfig)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("smtp dial %s: %w", addr, err)
	}
	_ = conn.SetDeadline(deadline)

	client, err := smtp.NewClient(conn, s.cfg.Host)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("smtp handshake: %w", err)
	}
	defer func() { _ = client.Close() }()

	if s.cfg.TLSMode != "implicit" {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			return errors.New("smtp server does not offer STARTTLS; refusing to send credentials in clear text")
		}
		if err := client.StartTLS(tlsConfig); err != nil {
			return fmt.Errorf("smtp starttls: %w", err)
		}
	}
	if err := client.Auth(smtp.PlainAuth("", s.cfg.Username, s.cfg.Password, s.cfg.Host)); err != nil {
		return fmt.Errorf("smtp auth: %w", err)
	}
	if err := client.Mail(s.cfg.From); err != nil {
		return classify("smtp MAIL FROM", err)
	}
	if err := client.Rcpt(msg.To); err != nil {
		return classify("smtp RCPT TO", err)
	}
	w, err := client.Data()
	if err != nil {
		return classify("smtp DATA", err)
	}
	if _, err := w.Write(raw); err != nil {
		return fmt.Errorf("smtp write: %w", err)
	}
	if err := w.Close(); err != nil {
		return classify("smtp end of data", err)
	}
	return client.Quit()
}

// classify turns SMTP 5xx replies into ErrPermanent so the outbox stops retrying them.
func classify(step string, err error) error {
	var tpErr *textproto.Error
	if errors.As(err, &tpErr) && tpErr.Code >= 500 {
		return fmt.Errorf("%w: %s: %v", ErrPermanent, step, err)
	}
	return fmt.Errorf("%s: %w", step, err)
}

// LogSender is the development/test driver: it logs the plain-text email instead of sending it,
// so activation links can be copied from the console. Never allowed in production (config).
type LogSender struct{}

func (LogSender) Send(_ context.Context, msg Message) error {
	log.Printf("[EMAIL:log-driver] to=%s subject=%q\n%s", msg.To, msg.Subject, msg.Text)
	return nil
}

// BuildMIME renders an RFC 5322 multipart/alternative message (text + HTML, quoted-printable).
// Header values are stripped of CR/LF to prevent header injection.
func BuildMIME(fromAddr, fromName string, msg Message, now time.Time) ([]byte, error) {
	from := (&mail.Address{Name: clean(fromName), Address: clean(fromAddr)}).String()
	to := (&mail.Address{Name: clean(msg.ToName), Address: clean(msg.To)}).String()
	domain := "mycaffe.local"
	if at := strings.LastIndex(fromAddr, "@"); at >= 0 {
		domain = clean(fromAddr[at+1:])
	}
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		return nil, err
	}

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for _, part := range []struct{ contentType, content string }{
		{"text/plain; charset=UTF-8", msg.Text},
		{"text/html; charset=UTF-8", msg.HTML},
	} {
		pw, err := mw.CreatePart(textproto.MIMEHeader{
			"Content-Type":              {part.contentType},
			"Content-Transfer-Encoding": {"quoted-printable"},
		})
		if err != nil {
			return nil, err
		}
		qp := quotedprintable.NewWriter(pw)
		if _, err := qp.Write([]byte(part.content)); err != nil {
			return nil, err
		}
		if err := qp.Close(); err != nil {
			return nil, err
		}
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}

	var out bytes.Buffer
	header := func(k, v string) { fmt.Fprintf(&out, "%s: %s\r\n", k, v) }
	header("From", from)
	header("To", to)
	if msg.ReplyTo != "" {
		header("Reply-To", (&mail.Address{Address: clean(msg.ReplyTo)}).String())
	}
	header("Subject", mime.QEncoding.Encode("UTF-8", clean(msg.Subject)))
	header("Date", now.Format(time.RFC1123Z))
	header("Message-ID", fmt.Sprintf("<%s@%s>", hex.EncodeToString(id), domain))
	header("MIME-Version", "1.0")
	header("Auto-Submitted", "auto-generated")
	header("Content-Type", "multipart/alternative; boundary="+mw.Boundary())
	out.WriteString("\r\n")
	out.Write(body.Bytes())
	return out.Bytes(), nil
}

func clean(s string) string {
	return strings.NewReplacer("\r", "", "\n", "").Replace(strings.TrimSpace(s))
}
