package notify

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

// SMTPConfig defines outbound email alerting configuration.
type SMTPConfig struct {
	Enabled           bool     `json:"enabled" toml:"enabled"`
	Host              string   `json:"host" toml:"host"`
	Port              int      `json:"port" toml:"port"`
	TLSMode           string   `json:"tls_mode" toml:"tls_mode"` // starttls | tls | none
	Username          string   `json:"username" toml:"username"`
	Password          string   `json:"password,omitempty" toml:"password"`
	PasswordEncrypted string   `json:"password_encrypted,omitempty" toml:"-"`
	FromEmail         string   `json:"from_email" toml:"from_email"`
	FromName          string   `json:"from_name" toml:"from_name"`
	ToEmails          []string `json:"to_emails" toml:"to_emails"`
}

// Validate verifies required SMTP fields when enabled.
func (c SMTPConfig) Validate() error {
	if !c.Enabled {
		return nil
	}
	if strings.TrimSpace(c.Host) == "" {
		return errors.New("smtp host is required")
	}
	if c.Port <= 0 || c.Port > 65535 {
		return errors.New("smtp port must be between 1 and 65535")
	}
	mode := strings.ToLower(strings.TrimSpace(c.TLSMode))
	if mode == "" {
		mode = "starttls"
	}
	if mode != "starttls" && mode != "tls" && mode != "none" {
		return fmt.Errorf("unsupported smtp tls_mode %q (must be starttls, tls, or none)", c.TLSMode)
	}
	if strings.TrimSpace(c.FromEmail) == "" {
		return errors.New("smtp from_email is required")
	}
	if len(cleanEmails(c.ToEmails)) == 0 {
		return errors.New("at least one recipient in to_emails is required")
	}
	return nil
}

// Send delivers an alert email using the configured SMTP server.
func Send(ctx context.Context, cfg SMTPConfig, subject, body string) error {
	if !cfg.Enabled {
		return errors.New("smtp is disabled")
	}
	if err := cfg.Validate(); err != nil {
		return err
	}

	recipients := cleanEmails(cfg.ToEmails)
	addr := net.JoinHostPort(strings.TrimSpace(cfg.Host), strconv.Itoa(cfg.Port))
	tlsMode := strings.ToLower(strings.TrimSpace(cfg.TLSMode))
	if tlsMode == "" {
		tlsMode = "starttls"
	}

	dialer := &net.Dialer{Timeout: 15 * time.Second}
	var conn net.Conn
	var err error

	tlsCfg := &tls.Config{
		ServerName: strings.TrimSpace(cfg.Host),
		MinVersion: tls.VersionTLS12,
	}

	if tlsMode == "tls" {
		conn, err = (&tls.Dialer{NetDialer: dialer, Config: tlsCfg}).DialContext(ctx, "tcp", addr)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("dial smtp %s: %w", addr, err)
	}

	client, err := smtp.NewClient(conn, strings.TrimSpace(cfg.Host))
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("create smtp client: %w", err)
	}
	defer client.Close()

	if tlsMode == "starttls" {
		if ok, _ := client.Extension("STARTTLS"); ok {
			if err := client.StartTLS(tlsCfg); err != nil {
				return fmt.Errorf("smtp starttls: %w", err)
			}
		} else {
			return errors.New("smtp server does not support STARTTLS")
		}
	}

	if strings.TrimSpace(cfg.Username) != "" {
		auth := smtp.PlainAuth("", strings.TrimSpace(cfg.Username), cfg.Password, strings.TrimSpace(cfg.Host))
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("smtp auth: %w", err)
		}
	}

	fromEmail := sanitizeHeader(cfg.FromEmail)
	if err := client.Mail(fromEmail); err != nil {
		return fmt.Errorf("smtp MAIL FROM: %w", err)
	}
	for _, rcpt := range recipients {
		if err := client.Rcpt(rcpt); err != nil {
			return fmt.Errorf("smtp RCPT TO %s: %w", rcpt, err)
		}
	}

	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("smtp DATA: %w", err)
	}
	msg := buildMessage(cfg.FromName, fromEmail, recipients, subject, body)
	if _, err := w.Write([]byte(msg)); err != nil {
		_ = w.Close()
		return fmt.Errorf("write smtp message: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("commit smtp message: %w", err)
	}
	_ = client.Quit()
	return nil
}

func buildMessage(fromName, fromEmail string, to []string, subject, body string) string {
	cleanFromName := sanitizeHeader(fromName)
	fromHeader := fromEmail
	if cleanFromName != "" {
		fromHeader = fmt.Sprintf("%s <%s>", encodeMIMEWord(cleanFromName), fromEmail)
	}
	headers := []string{
		"From: " + fromHeader,
		"To: " + strings.Join(to, ", "),
		"Subject: " + encodeMIMEWord(sanitizeHeader(subject)),
		"Date: " + time.Now().UTC().Format(time.RFC1123Z),
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=UTF-8",
		"Content-Transfer-Encoding: base64",
	}
	encodedBody := wrapBase64(base64.StdEncoding.EncodeToString([]byte(body)))
	return strings.Join(headers, "\r\n") + "\r\n\r\n" + encodedBody
}

func encodeMIMEWord(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] > 127 {
			return "=?UTF-8?B?" + base64.StdEncoding.EncodeToString([]byte(s)) + "?="
		}
	}
	return s
}

func wrapBase64(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i += 76 {
		end := i + 76
		if end > len(s) {
			end = len(s)
		}
		b.WriteString(s[i:end])
		b.WriteString("\r\n")
	}
	return b.String()
}

func sanitizeHeader(s string) string {
	s = strings.ReplaceAll(s, "\r", "")
	s = strings.ReplaceAll(s, "\n", "")
	return strings.TrimSpace(s)
}

func cleanEmails(list []string) []string {
	var out []string
	seen := make(map[string]bool)
	for _, raw := range list {
		e := sanitizeHeader(raw)
		if e != "" && !seen[e] {
			seen[e] = true
			out = append(out, e)
		}
	}
	return out
}
