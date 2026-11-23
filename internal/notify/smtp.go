package notify

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

// Timeout caps a single SMTP delivery attempt.
const Timeout = 20 * time.Second

// SMTPConfig defines outbound email notification configuration.
type SMTPConfig struct {
	Enabled           bool        `json:"enabled" toml:"enabled"`
	Host              string      `json:"host" toml:"host"`
	Port              int         `json:"port" toml:"port"`
	TLSMode           string      `json:"tls_mode" toml:"tls_mode"` // starttls | tls | none
	Username          string      `json:"username" toml:"username"`
	Password          string      `json:"password,omitempty" toml:"password"`
	PasswordEncrypted string      `json:"password_encrypted,omitempty" toml:"-"`
	FromEmail         string      `json:"from_email" toml:"from_email"`
	FromName          string      `json:"from_name" toml:"from_name"`
	ToEmails          []string    `json:"to_emails" toml:"to_emails"`
	Notifications     NotifyRules `json:"notifications" toml:"notifications"`
}

// Validate verifies required SMTP fields when enabled and checks notification rule values.
func (c SMTPConfig) Validate() error {
	if err := c.Notifications.Validate(); err != nil {
		return err
	}
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
	if _, err := parseSender(c.FromEmail, c.FromName); err != nil {
		return fmt.Errorf("invalid smtp from_email: %w", err)
	}
	if _, err := parseRecipients(c.ToEmails); err != nil {
		return err
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

	fromAddr, err := parseSender(cfg.FromEmail, cfg.FromName)
	if err != nil {
		return fmt.Errorf("invalid sender: %w", err)
	}
	recipients, err := parseRecipients(cfg.ToEmails)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()

	host := strings.TrimSpace(cfg.Host)
	addr := net.JoinHostPort(host, strconv.Itoa(cfg.Port))
	tlsMode := strings.ToLower(strings.TrimSpace(cfg.TLSMode))
	if tlsMode == "" {
		tlsMode = "starttls"
	}

	dialer := &net.Dialer{}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("dial smtp %s: %w", addr, err)
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}

	tlsCfg := &tls.Config{
		ServerName: host,
		MinVersion: tls.VersionTLS12,
	}
	if tlsMode == "tls" {
		conn = tls.Client(conn, tlsCfg)
	}

	client, err := smtp.NewClient(conn, host)
	if err != nil {
		return fmt.Errorf("create smtp client: %w", err)
	}
	defer client.Close()

	if tlsMode == "starttls" {
		if ok, _ := client.Extension("STARTTLS"); ok {
			if err := client.StartTLS(tlsCfg); err != nil {
				return fmt.Errorf("smtp starttls: %w", err)
			}
		} else {
			return errors.New("the server does not support STARTTLS")
		}
	}

	if username := strings.TrimSpace(cfg.Username); username != "" {
		if err := authenticateSMTP(client, username, cfg.Password, host, tlsMode != "none"); err != nil {
			return fmt.Errorf("smtp auth: %w", err)
		}
	}

	if err := client.Mail(fromAddr.Address); err != nil {
		return fmt.Errorf("smtp MAIL FROM: %w", err)
	}
	rcptAddrs := make([]string, 0, len(recipients))
	for _, rcpt := range recipients {
		if err := client.Rcpt(rcpt.Address); err != nil {
			return fmt.Errorf("smtp RCPT TO %s: %w", rcpt.Address, err)
		}
		rcptAddrs = append(rcptAddrs, rcpt.Address)
	}

	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("smtp DATA: %w", err)
	}
	msg := buildMessage(fromAddr, rcptAddrs, subject, body, time.Now())
	if _, err := w.Write(msg); err != nil {
		_ = w.Close()
		return fmt.Errorf("write smtp message: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("commit smtp message: %w", err)
	}
	if err := client.Quit(); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

func authenticateSMTP(client *smtp.Client, username, password, host string, encrypted bool) error {
	ok, mechs := client.Extension("AUTH")
	if ok {
		upper := " " + strings.ToUpper(mechs) + " "
		if !strings.Contains(upper, " PLAIN ") && strings.Contains(upper, " LOGIN ") {
			return client.Auth(&loginAuth{username: username, password: password, encrypted: encrypted})
		}
	}
	return client.Auth(smtp.PlainAuth("", username, password, host))
}

type loginAuth struct {
	username  string
	password  string
	encrypted bool
}

func (a *loginAuth) Start(server *smtp.ServerInfo) (string, []byte, error) {
	if !server.TLS && !a.encrypted {
		return "", nil, errors.New("unencrypted connection")
	}
	return "LOGIN", nil, nil
}

func (a *loginAuth) Next(fromServer []byte, more bool) ([]byte, error) {
	if !more {
		return nil, nil
	}
	prompt := strings.ToLower(strings.TrimSpace(string(fromServer)))
	switch {
	case strings.Contains(prompt, "username"):
		return []byte(a.username), nil
	case strings.Contains(prompt, "password"):
		return []byte(a.password), nil
	default:
		return nil, fmt.Errorf("unexpected LOGIN challenge: %q", string(fromServer))
	}
}

func parseSender(rawEmail, rawName string) (*mail.Address, error) {
	raw := sanitizeHeader(rawEmail)
	if raw == "" {
		return nil, errors.New("from_email is required")
	}
	parsed, err := mail.ParseAddress(raw)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(parsed.Name) == "" && strings.TrimSpace(rawName) != "" {
		parsed.Name = sanitizeHeader(rawName)
	}
	return parsed, nil
}

func parseRecipients(list []string) ([]*mail.Address, error) {
	var out []*mail.Address
	seen := make(map[string]bool)
	for _, raw := range list {
		cleaned := sanitizeHeader(raw)
		if cleaned == "" {
			continue
		}
		parsed, err := mail.ParseAddress(cleaned)
		if err != nil {
			return nil, fmt.Errorf("invalid recipient %q: %w", cleaned, err)
		}
		key := strings.ToLower(parsed.Address)
		if !seen[key] {
			seen[key] = true
			out = append(out, parsed)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("at least one recipient in to_emails is required")
	}
	return out, nil
}

func buildMessage(from *mail.Address, to []string, subject, body string, at time.Time) []byte {
	var out bytes.Buffer
	header := func(name, value string) {
		fmt.Fprintf(&out, "%s: %s\r\n", name, sanitizeHeader(value))
	}
	fromHeader := from.Address
	if from.Name != "" {
		fromHeader = fmt.Sprintf("%s <%s>", mime.QEncoding.Encode("utf-8", from.Name), from.Address)
	}
	header("From", fromHeader)
	header("To", strings.Join(to, ", "))
	header("Subject", mime.QEncoding.Encode("utf-8", subject))
	header("Date", at.Format(time.RFC1123Z))
	header("MIME-Version", "1.0")
	header("Content-Type", "text/plain; charset=utf-8")
	header("Content-Transfer-Encoding", "base64")
	out.WriteString("\r\n")
	writeBase64(&out, []byte(body))
	return out.Bytes()
}

func writeBase64(out io.Writer, data []byte) {
	encoded := base64.StdEncoding.EncodeToString(data)
	for len(encoded) > 76 {
		_, _ = io.WriteString(out, encoded[:76]+"\r\n")
		encoded = encoded[76:]
	}
	_, _ = io.WriteString(out, encoded+"\r\n")
}

func sanitizeHeader(s string) string {
	return strings.TrimSpace(strings.NewReplacer("\r", " ", "\n", " ").Replace(s))
}
