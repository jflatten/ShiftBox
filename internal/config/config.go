package config

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"time"
)

// Config holds explicit installation inputs; only composition chooses its path.
type Config struct {
	InstallationDir                  string             `json:"installationDir"`
	DataProfile                      string             `json:"dataProfile"`
	SQLitePath                       string             `json:"sqlitePath"`
	PostgresURI                      string             `json:"postgresURI"`
	ManagementOrigin                 string             `json:"managementOrigin"`
	ExternalAuthIntervalSeconds      int64              `json:"externalAuthIntervalSeconds"`
	AuditRetentionDays               *int               `json:"auditRetentionDays,omitempty"`
	DismissedNoticeRetentionDays     *int               `json:"dismissedNoticeRetentionDays,omitempty"`
	WorkspaceRetentionMaximumSeconds *int64             `json:"workspaceRetentionMaximumSeconds,omitempty"`
	RetentionOutageGraceSeconds      *int64             `json:"retentionOutageGraceSeconds,omitempty"`
	RetentionMonitorIDE              *bool              `json:"retentionMonitorIDE,omitempty"`
	RetentionMonitorCPU              *bool              `json:"retentionMonitorCPU,omitempty"`
	Runtime                          *ControllerRuntime `json:"runtime,omitempty"`
}

// ControllerRuntime describes the native listeners and UTM resources for serve.
type ControllerRuntime struct {
	BrowserAddress    string      `json:"browserAddress"`
	GuestHost         string      `json:"guestHost"`
	EnrollmentAddress string      `json:"enrollmentAddress"`
	PollAddress       string      `json:"pollAddress"`
	TunnelAddress     string      `json:"tunnelAddress"`
	UTMStorage        string      `json:"utmStorage"`
	UTMMinimum        VMResources `json:"utmMinimum"`
	UTMMaximum        VMResources `json:"utmMaximum"`
	DexBinary         string      `json:"dexBinary,omitempty"`
	GitHubAppFile     string      `json:"githubAppFile,omitempty"`
}

// VMResources records the validated allocation bounds for the local UTM host.
type VMResources struct {
	CPU       int   `json:"cpu"`
	MemoryMiB int64 `json:"memoryMiB"`
	DiskMiB   int64 `json:"diskMiB"`
}

// RetentionOutageGrace returns the configured grace period or ten minutes.
func (c Config) RetentionOutageGrace() time.Duration {
	if c.RetentionOutageGraceSeconds == nil {
		return 10 * time.Minute
	}
	return time.Duration(*c.RetentionOutageGraceSeconds) * time.Second
}

// RetentionActivitySignals selects the IDE and CPU signals enabled by the operator.
func (c Config) RetentionActivitySignals() (ide, cpu bool) {
	return c.RetentionMonitorIDE == nil || *c.RetentionMonitorIDE, c.RetentionMonitorCPU == nil || *c.RetentionMonitorCPU
}

// AuditHistoryDays returns the configured age limit or the 90-day default.
func (c Config) AuditHistoryDays() int {
	if c.AuditRetentionDays == nil {
		return 90
	}
	return *c.AuditRetentionDays
}

// DismissedNoticeHistoryDays returns the configured age limit or the 30-day default.
func (c Config) DismissedNoticeHistoryDays() int {
	if c.DismissedNoticeRetentionDays == nil {
		return 30
	}
	return *c.DismissedNoticeRetentionDays
}

// Load reads one explicit file without consulting process environment variables.
// Diagnostics deliberately omit file content and database credentials.
func Load(path string) (Config, error) {
	if path == "" {
		return Config{}, errors.New("configuration path is required")
	}
	file, err := os.Open(path)
	if err != nil {
		return Config{}, errors.New("cannot read configuration file")
	}
	defer file.Close()
	cfg := Config{ExternalAuthIntervalSeconds: 14400}
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, errors.New("invalid configuration document")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return Config{}, errors.New("configuration must contain one document")
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}
