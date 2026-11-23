package notify

import (
	"fmt"
	"strings"
)

// NotifyMode controls when an operation triggers an outbound email notification.
type NotifyMode string

const (
	NotifyOff     NotifyMode = "off"
	NotifyFailure NotifyMode = "failure"
	NotifyAlways  NotifyMode = "always"
)

// NotifyRules configures which system events trigger SMTP email notifications.
type NotifyRules struct {
	// Alert events (boolean)
	CircuitBreakerTripped bool `json:"circuit_breaker_tripped" toml:"circuit_breaker_tripped"`
	OversizeRejected      bool `json:"oversize_rejected" toml:"oversize_rejected"`

	// Core backup operations (tri-state: off | failure | always)
	BackupUpload NotifyMode `json:"backup_upload" toml:"backup_upload"`
	RemoteBackup NotifyMode `json:"remote_backup" toml:"remote_backup"`

	// Scheduled maintenance & migration
	MigrationBackup     NotifyMode `json:"migration_backup" toml:"migration_backup"`
	MigrationExport     bool       `json:"migration_export" toml:"migration_export"`
	MigrationImport     bool       `json:"migration_import" toml:"migration_import"`
	UpstreamHealthCheck bool       `json:"upstream_health_check" toml:"upstream_health_check"`

	// Security & destructive admin actions
	ClientDelete    bool `json:"client_delete" toml:"client_delete"`
	ClientKeyRotate bool `json:"client_key_rotate" toml:"client_key_rotate"`
	ResourceDelete  bool `json:"resource_delete" toml:"resource_delete"`
	SecretsReveal   bool `json:"secrets_reveal" toml:"secrets_reveal"`
	AdminSecurity   bool `json:"admin_security" toml:"admin_security"`
}

// DefaultNotifyRules returns backward-compatible notification defaults.
func DefaultNotifyRules() NotifyRules {
	return NotifyRules{
		CircuitBreakerTripped: true,
		OversizeRejected:      true,
		BackupUpload:          NotifyFailure,
		RemoteBackup:          NotifyFailure,
		MigrationBackup:       NotifyFailure,
		MigrationExport:       false,
		MigrationImport:       false,
		UpstreamHealthCheck:   true,
		ClientDelete:          false,
		ClientKeyRotate:       false,
		ResourceDelete:        false,
		SecretsReveal:         false,
		AdminSecurity:         false,
	}
}

// DefaultSMTPConfig returns an SMTPConfig initialized with default transport and notification rules.
func DefaultSMTPConfig() SMTPConfig {
	return SMTPConfig{
		Port:          587,
		TLSMode:       "starttls",
		FromName:      "SafeKeep",
		Notifications: DefaultNotifyRules(),
	}
}

// Normalize fills empty modes with their defaults and trims/lowercases values.
func (r NotifyRules) Normalize() NotifyRules {
	r.BackupUpload = normalizeMode(r.BackupUpload, NotifyFailure)
	r.RemoteBackup = normalizeMode(r.RemoteBackup, NotifyFailure)
	r.MigrationBackup = normalizeMode(r.MigrationBackup, NotifyFailure)
	return r
}

// Validate checks that all tri-state modes have supported values when provided.
func (r NotifyRules) Validate() error {
	for field, m := range map[string]NotifyMode{
		"backup_upload":    r.BackupUpload,
		"remote_backup":    r.RemoteBackup,
		"migration_backup": r.MigrationBackup,
	} {
		if m == "" {
			continue
		}
		v := NotifyMode(strings.ToLower(strings.TrimSpace(string(m))))
		if v != NotifyOff && v != NotifyFailure && v != NotifyAlways {
			return fmt.Errorf("invalid notification mode %q for %s (must be off, failure, or always)", m, field)
		}
	}
	return nil
}

// ShouldNotify evaluates whether an outcome should send a notification under this mode.
func (m NotifyMode) ShouldNotify(succeeded bool) bool {
	switch NotifyMode(strings.ToLower(strings.TrimSpace(string(m)))) {
	case NotifyAlways:
		return true
	case NotifyFailure:
		return !succeeded
	default:
		return false
	}
}

func normalizeMode(m, fallback NotifyMode) NotifyMode {
	v := NotifyMode(strings.ToLower(strings.TrimSpace(string(m))))
	switch v {
	case NotifyOff, NotifyFailure, NotifyAlways:
		return v
	default:
		return fallback
	}
}
