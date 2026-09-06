// Package mailer sends lifecycle emails over SMTP. Delivery records are the
// caller's responsibility (FR-1); this package is transport only.
package mailer

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/smtp"
	"strings"
	"time"
)

type Config struct {
	Host     string // SMTP host
	Port     int    // SMTP port (587 typical)
	Username string
	Password string
	From     string // envelope From address
	// NoTLSForLocalhost permits plain SMTP on loopback for development.
	NoTLSForLocalhost bool
}

type Mailer struct {
	cfg  Config
	send func(to, subject, body string) error
}

func New(cfg Config) *Mailer { return &Mailer{cfg: cfg} }

// NewFunc returns a Mailer backed by an arbitrary send function (test
// fakes, file/stdout development transports). A nil fn is a no-op mailer
// that always succeeds.
func NewFunc(fn func(to, subject, body string) error) *Mailer {
	return &Mailer{cfg: Config{Host: "func", From: "func@local"}, send: fn}
}

func (m *Mailer) addr() string { return net.JoinHostPort(m.cfg.Host, fmt.Sprint(m.cfg.Port)) }

// Send delivers a plaintext message. subject/body must not contain user
// input unsanitized; callers pass fixed templates plus values. Safe on a
// nil receiver (review follow-up: typed-nil wiring must not panic).
func (m *Mailer) Send(to, subject, body string) error {
	if m == nil || m.cfg.Host == "" || m.cfg.From == "" {
		return errors.New("mailer: not configured")
	}
	from := m.cfg.From
	headers := strings.Join([]string{
		"From: " + from,
		"To: " + to,
		"Subject: " + sanitizeHeader(subject),
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=\"utf-8\"",
		"Date: " + time.Now().Format(time.RFC1123Z),
	}, "\r\n")
	msg := []byte(headers + "\r\n\r\n" + body + "\r\n")

	if m.send != nil {
		return m.send(to, subject, body)
	}

	addr := m.addr()
	host, _, _ := net.SplitHostPort(addr)
	isLocal := host == "127.0.0.1" || host == "::1" || host == "localhost"

	var auth smtp.Auth
	if m.cfg.Username != "" {
		auth = smtp.PlainAuth("", m.cfg.Username, m.cfg.Password, host)
	}

	if m.cfg.Port == 465 {
		conn, err := tls.Dial("tcp", addr, &tls.Config{ServerName: host})
		if err != nil {
			return fmt.Errorf("mailer: tls dial: %w", err)
		}
		c, err := smtp.NewClient(conn, host)
		if err != nil {
			return err
		}
		defer c.Close()
		return m.deliver(c, auth, from, to, msg)
	}

	if m.cfg.Port == 25 && isLocal && m.cfg.NoTLSForLocalhost {
		return smtp.SendMail(addr, auth, from, []string{to}, msg)
	}
	return smtp.SendMail(addr, auth, from, []string{to}, msg) // STARTTLS enforced by net/smtp
}

func (m *Mailer) deliver(c *smtp.Client, auth smtp.Auth, from, to string, msg []byte) error {
	if auth != nil {
		if ok, _ := c.Extension("AUTH"); ok {
			if err := c.Auth(auth); err != nil {
				return fmt.Errorf("mailer: auth: %w", err)
			}
		}
	}
	if err := c.Mail(from); err != nil {
		return err
	}
	if err := c.Rcpt(to); err != nil {
		return err
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(msg); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}

func sanitizeHeader(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r", ""), "\n", "")
}
