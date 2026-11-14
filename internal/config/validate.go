package config

import (
	"errors"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Validate rejects ambiguous paths, insecure service URLs, and invalid intervals.
func (c Config) Validate() error {
	if !filepath.IsAbs(c.InstallationDir) {
		return errors.New("installation directory must be absolute")
	}
	if c.ExternalAuthIntervalSeconds <= 0 {
		return errors.New("external authorization interval must be positive")
	}
	const maxSeconds = int64((1<<63 - 1) / time.Second)
	for _, value := range []*int64{c.WorkspaceRetentionMaximumSeconds, c.RetentionOutageGraceSeconds} {
		if value != nil && (*value <= 0 || *value > maxSeconds) {
			return errors.New("workspace retention intervals must be positive supported numbers of seconds")
		}
	}
	if days := c.AuditHistoryDays(); days <= 0 || days > int(time.Duration(1<<63-1)/(24*time.Hour)) {
		return errors.New("audit retention must be a positive supported number of days")
	}
	if days := c.DismissedNoticeHistoryDays(); days <= 0 || days > int(time.Duration(1<<63-1)/(24*time.Hour)) {
		return errors.New("dismissed notice retention must be a positive supported number of days")
	}
	if err := validateManagementOrigin(c.ManagementOrigin); err != nil {
		return err
	}

	switch c.DataProfile {
	case "sqlite":
		if !filepath.IsAbs(c.SQLitePath) || c.PostgresURI != "" {
			return errors.New("sqlite requires an absolute database path and no postgres connection")
		}
	case "postgres":
		u, err := url.Parse(c.PostgresURI)
		if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Hostname() == "" || u.Query().Get("sslmode") != "verify-full" || c.SQLitePath != "" {
			return errors.New("postgres requires a verified TLS connection and no sqlite path")
		}
	default:
		return errors.New("data profile must be sqlite or postgres")
	}
	return nil
}

// validateManagementOrigin keeps configured origins equal to browser serialization.
func validateManagementOrigin(value string) error {
	origin, err := url.Parse(value)
	if err != nil || origin.Scheme != "https" || origin.Hostname() == "" || origin.User != nil || origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" || origin.ForceQuery || origin.String() != value || origin.Host != strings.ToLower(origin.Host) || strings.HasSuffix(origin.Host, ":") {
		return errors.New("management origin must be a canonical HTTPS origin")
	}
	// Browsers omit the default HTTPS port when serializing their Origin header.
	if port := origin.Port(); port != "" {
		number, err := strconv.Atoi(port)
		if err != nil || number < 1 || number > 65535 || number == 443 || strconv.Itoa(number) != port {
			return errors.New("management origin must use a canonical HTTPS port")
		}
	}
	return nil
}
