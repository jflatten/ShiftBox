package config_test

import (
	"github.com/jflatten/ShiftBox/internal/config"
	"testing"
)

// TestServiceRouting round-trips workspace identities and denies malformed or unrelated hostnames.
func TestServiceRouting(t *testing.T) {
	const workspace = "00000000-0000-4000-8000-000000000001"
	hosts, err := config.NewServiceHosts("https://management.test", "{workspace}.ide.test", "{service}--{workspace}.preview.test")
	if err != nil {
		t.Fatal(err)
	}
	for _, service := range []string{"ide", "preview-8080", "my--service"} {
		hostname, err := hosts.Hostname(workspace, service)
		if err != nil {
			t.Fatal(err)
		}
		gotWorkspace, gotService, ok := hosts.Resolve(hostname)
		if !ok || gotWorkspace != workspace || gotService != service {
			t.Errorf("Resolve(%q) = %q, %q, %t, want %q, %q, true", hostname, gotWorkspace, gotService, ok, workspace, service)
		}
	}
	for _, hostname := range []string{"management.test", "other.ide.test", workspace + ".ide.test.attacker.test", "preview-8080--" + workspace + ".preview.test:443", "--" + workspace + ".preview.test", "IDE--" + workspace + ".preview.test", "ide--" + workspace + ".preview.test", workspace + ".ide.test."} {
		if _, _, ok := hosts.Resolve(hostname); ok {
			t.Errorf("Resolve(%q) accepted invalid service host", hostname)
		}
	}
	for _, service := range []string{"", "-bad", "bad-", "a.b", "arbitrary:8080", "abcdefghijklmnopqrstuvwxyz"} {
		if _, err := hosts.Hostname(workspace, service); err == nil {
			t.Errorf("Hostname(%q, %q) accepted invalid service key", workspace, service)
		}
	}
	if _, err := hosts.Hostname("not-a-uuid", "ide"); err == nil {
		t.Error("Hostname(invalid UUID) accepted invalid workspace")
	}
}

// TestManagementServiceCollision prevents a service origin from also carrying management credentials.
func TestManagementServiceCollision(t *testing.T) {
	for _, origin := range []string{"https://00000000-0000-4000-8000-000000000001.ide.test", "https://web--00000000-0000-4000-8000-000000000001.preview.test", "https://management.test:443", "https://00000000-0000-4000-8000-000000000001.ide.test:8443"} {
		if _, err := config.NewServiceHosts(origin, "{workspace}.ide.test", "{service}--{workspace}.preview.test"); err == nil {
			t.Errorf("NewServiceHosts(%q) accepted conflicting/noncanonical origin", origin)
		}
	}
}
