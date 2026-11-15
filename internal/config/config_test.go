package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jflatten/ShiftBox/internal/config"
)

// TestUTMGuestDefaults keeps local guest URLs independent of Wi-Fi while
// preserving installations that explicitly configured a different endpoint.
func TestUTMGuestDefaults(t *testing.T) {
	const base = `{"dataProfile":"sqlite","sqlitePath":"/tmp/shiftbox.db","installationDir":"/tmp/shiftbox","managementOrigin":"https://shiftbox.test","runtime":`
	type endpoints struct{ host, enrollment, poll, tunnel string }
	for _, tc := range []struct {
		name, runtime string
		want          endpoints
	}{
		{"omitted guest settings", `{"browserAddress":"127.0.0.1:9443"}`,
			endpoints{"192.168.64.1", "192.168.64.1:19443", "192.168.64.1:19444", "192.168.64.1:19445"}},
		{"custom port", `{"browserAddress":"127.0.0.1:9443","pollAddress":"192.168.64.1:20444"}`,
			endpoints{"192.168.64.1", "192.168.64.1:19443", "192.168.64.1:20444", "192.168.64.1:19445"}},
		{"explicit guest addresses", `{"browserAddress":"127.0.0.1:9443","guestHost":"host.example.test","enrollmentAddress":":8443","pollAddress":":8444","tunnelAddress":":8445"}`,
			endpoints{"host.example.test", ":8443", ":8444", ":8445"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(path, []byte(base+tc.runtime+"}"), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := config.Load(path)
			if err != nil {
				t.Fatal(err)
			}
			r := cfg.Runtime
			got := endpoints{r.GuestHost, r.EnrollmentAddress, r.PollAddress, r.TunnelAddress}
			if got != tc.want {
				t.Errorf("Load(%s) guest settings = %+v, want %+v", tc.name, got, tc.want)
			}
			if r.BrowserAddress != "127.0.0.1:9443" {
				t.Errorf("Load(%s) browser address = %q, want unchanged loopback bind", tc.name, r.BrowserAddress)
			}
		})
	}
}

// TestRetentionActivitySelection keeps the default monitors enabled and
// preserves an operator's explicit decision to turn an individual signal off.
func TestRetentionActivitySelection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"dataProfile":"sqlite","sqlitePath":"/tmp/shiftbox.db","installationDir":"/tmp/shiftbox","managementOrigin":"https://shiftbox.test","retentionMonitorIDE":false}`), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	ide, cpu := cfg.RetentionActivitySignals()
	if ide || !cpu {
		t.Errorf("configured IDE/CPU signals = %t/%t, want false/true", ide, cpu)
	}
	ide, cpu = (config.Config{}).RetentionActivitySignals()
	if !ide || !cpu {
		t.Errorf("default IDE/CPU signals = %t/%t, want true/true", ide, cpu)
	}
}

// TestManagementOriginCanonicalForm prevents browser Origin serialization from disagreeing with configured mutation checks.
func TestManagementOriginCanonicalForm(t *testing.T) {
	for _, origin := range []string{"https://management.test?", "https://management.test#", "https://management.test:", "https://management.test:443", "https://Management.test", "https://management.test:65536"} {
		cfg := config.Config{InstallationDir: "/tmp/installation", DataProfile: "sqlite", SQLitePath: "/tmp/state.db", ManagementOrigin: origin, ExternalAuthIntervalSeconds: 14400}
		if err := cfg.Validate(); err == nil {
			t.Errorf("Validate(origin %q) accepted noncanonical origin", origin)
		}
	}
}

// TestLoadRequiresExplicitPath prevents accidentally selecting stale installation state.
func TestLoadRequiresExplicitPath(t *testing.T) {
	if _, err := config.Load(""); err == nil {
		t.Error("Load(empty path) succeeded, want explicit-path error")
	}
}

// TestLoadRejectsInvalidAndRedacts prevents config errors from disclosing service credentials.
func TestLoadRejectsInvalidAndRedacts(t *testing.T) {
	for _, input := range []string{
		`{"dataProfile":"postgres","postgresURI":"postgres://secret-password@host/db?sslmode=disable"}`,
		`{"dataProfile":"sqlite","sqlitePath":"relative.db"}`,
		`{"dataProfile":"other"}`,
		`{"externalAuthIntervalSeconds":-1}`,
		`{"unknown":"secret-password"}`,
		`{"postgresURI":"secret-password"`,
	} {
		// Malformed, insecure, and ambiguous configurations must all fail closed.
		t.Run(input, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(path, []byte(input), 0600); err != nil {
				t.Fatal(err)
			}
			_, err := config.Load(path)
			if err == nil {
				t.Fatal("Load(invalid config) succeeded, want error")
			}
			if strings.Contains(err.Error(), "secret-password") {
				t.Error("Load error discloses protected configuration")
			}
		})
	}
}

// TestLoadIgnoresEnvironment keeps all configuration decisions at the explicit file boundary.
func TestLoadIgnoresEnvironment(t *testing.T) {
	t.Setenv("SHIFTBOX_DATA_PROFILE", "postgres")
	path := filepath.Join(t.TempDir(), "config.json")
	input := `{"dataProfile":"sqlite","sqlitePath":"/tmp/shiftbox-fixture.db","installationDir":"/tmp/shiftbox-fixture","managementOrigin":"https://shiftbox.test","externalAuthIntervalSeconds":14400}`
	if err := os.WriteFile(path, []byte(input), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.DataProfile != "sqlite" || got.ExternalAuthIntervalSeconds != 14400 {
		t.Errorf("Load(file) = %+v, want explicit sqlite and 14400-second interval", got)
	}
}

// TestInitialAuthorizationInterval applies the approved four-hour default only when omitted.
func TestInitialAuthorizationInterval(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	input := `{"dataProfile":"sqlite","sqlitePath":"/tmp/shiftbox-fixture.db","installationDir":"/tmp/shiftbox-fixture","managementOrigin":"https://shiftbox.test"}`
	if err := os.WriteFile(path, []byte(input), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.ExternalAuthIntervalSeconds != 14400 {
		t.Errorf("default authorization interval = %d, want 14400", got.ExternalAuthIntervalSeconds)
	}
}

// TestAuditRetentionDays covers the operator default, override and invalid age.
func TestAuditRetentionDays(t *testing.T) {
	const base = `{"dataProfile":"sqlite","sqlitePath":"/tmp/shiftbox-fixture.db","installationDir":"/tmp/shiftbox-fixture","managementOrigin":"https://shiftbox.test"`
	for _, tc := range []struct {
		name, setting string
		days          int
	}{
		{"default", "", 90},
		{"override", `,"auditRetentionDays":120`, 120},
		{"zero", `,"auditRetentionDays":0`, 0},
		{"negative", `,"auditRetentionDays":-1`, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(path, []byte(base+tc.setting+"}"), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := config.Load(path)
			if tc.days == 0 {
				if err == nil {
					t.Fatal("Load accepted a nonpositive audit retention setting")
				}
				return
			}
			if err != nil || cfg.AuditHistoryDays() != tc.days {
				t.Errorf("audit history = %d days (%v), want %d", cfg.AuditHistoryDays(), err, tc.days)
			}
		})
	}
}

// TestDismissedNoticeRetentionDays checks the default, override and invalid age.
func TestDismissedNoticeRetentionDays(t *testing.T) {
	const base = `{"dataProfile":"sqlite","sqlitePath":"/tmp/shiftbox-fixture.db","installationDir":"/tmp/shiftbox-fixture","managementOrigin":"https://shiftbox.test"`
	for _, tc := range []struct {
		name, setting string
		days          int
	}{
		{"default", "", 30},
		{"override", `,"dismissedNoticeRetentionDays":60`, 60},
		{"zero", `,"dismissedNoticeRetentionDays":0`, 0},
		{"negative", `,"dismissedNoticeRetentionDays":-1`, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(path, []byte(base+tc.setting+"}"), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := config.Load(path)
			if tc.days == 0 {
				if err == nil {
					t.Fatal("Load accepted a nonpositive dismissed notice retention setting")
				}
				return
			}
			if err != nil || cfg.DismissedNoticeHistoryDays() != tc.days {
				t.Errorf("dismissed notice history = %d days (%v), want %d", cfg.DismissedNoticeHistoryDays(), err, tc.days)
			}
		})
	}
}

// TestWorkspaceRetentionConfig rejects intervals that cannot safely be applied.
func TestWorkspaceRetentionConfig(t *testing.T) {
	const base = `{"dataProfile":"sqlite","sqlitePath":"/tmp/shiftbox-fixture.db","installationDir":"/tmp/shiftbox-fixture","managementOrigin":"https://shiftbox.test"`
	for _, tc := range []struct {
		value string
		ok    bool
	}{
		{"", true},
		{`,"workspaceRetentionMaximumSeconds":3600,"retentionOutageGraceSeconds":600`, true},
		{`,"workspaceRetentionMaximumSeconds":0`, false},
		{`,"retentionOutageGraceSeconds":0`, false},
		{`,"workspaceRetentionMaximumSeconds":9223372036854775807`, false},
	} {
		path := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(path, []byte(base+tc.value+"}"), 0600); err != nil {
			t.Fatal(err)
		}
		cfg, err := config.Load(path)
		if (err == nil) != tc.ok {
			t.Errorf("config %q = %v, want valid=%t", tc.value, err, tc.ok)
		}
		if tc.value == "" && (cfg.WorkspaceRetentionMaximumSeconds != nil || cfg.RetentionOutageGrace() != 10*time.Minute) {
			t.Error("default config must disable workspace retention and use ten minutes of grace")
		}
	}
}
