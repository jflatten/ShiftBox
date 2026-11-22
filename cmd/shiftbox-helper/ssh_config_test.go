package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestSSHConfigPreservesUserHostsAndRename verifies literal host discovery,
// case-insensitive collision handling, and a stable alias after removal and rename.
func TestSSHConfigPreservesUserHostsAndRename(t *testing.T) {
	paths := sshConfigFixture(t)
	const original = "ServerAliveInterval 27\nHost ShIfTbOx-project\n    HostName custom.internal\n# Preserve this exact text"
	if err := os.WriteFile(paths.ConfigFile, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(paths.ConfigFile, 0640); err != nil {
		t.Fatal(err)
	}
	reconcile := func(rows []sshAliasWorkspace) string {
		t.Helper()
		if err := reconcileSSHConfig(paths, "https://controller.test", "account-a", rows); err != nil {
			t.Fatal(err)
		}
		return readSSHConfigFixture(t, paths.ConfigFile)
	}
	row := sshAliasWorkspace{ID: "workspace-one", Name: "Project"}
	if got := reconcile([]sshAliasWorkspace{row}); !strings.Contains(got, "Host shiftbox-project-2\n") ||
		!strings.HasSuffix(got, original) {
		t.Fatalf("reconcile collision produced %q, want distinct ShiftBox alias and exact user config", got)
	}
	if got := reconcile(nil); got != original {
		t.Fatalf("reconcile after removal produced %q, want original user config", got)
	}
	row.Name = "Renamed Project"
	if got := reconcile([]sshAliasWorkspace{row}); !strings.Contains(got, "Host shiftbox-project-2\n") {
		t.Errorf("reconcile after rename produced %q, want original alias", got)
	}
	info, err := os.Stat(paths.ConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0640 {
		t.Errorf("config permissions = %o, want 640", mode)
	}
}

// TestSSHConfigScopesAliases verifies switching accounts or installations
// preserves each scope's saved names without leaking another scope's entries.
func TestSSHConfigScopesAliases(t *testing.T) {
	paths := sshConfigFixture(t)
	for _, tc := range []struct{ origin, account, name, want string }{
		{"https://controller.test", "first", "Project A", "shiftbox-project-a"},
		{"https://controller.test", "second", "Project A", "shiftbox-project-a-2"},
		{"https://other.test", "first", "Project A", "shiftbox-project-a-3"},
		{"https://CONTROLLER.test:443/", "first", "New label", "shiftbox-project-a"},
	} {
		if err := reconcileSSHConfig(paths, tc.origin, tc.account, []sshAliasWorkspace{{ID: "workspace-one", Name: tc.name}}); err != nil {
			t.Fatal(err)
		}
		got := readSSHConfigFixture(t, paths.ConfigFile)
		if !strings.Contains(got, "Host "+tc.want+"\n") || strings.Count(got, "Host shiftbox-") != 1 {
			t.Errorf("SSH config for %s/%s = %q, want one eligible alias %s", tc.origin, tc.account, got, tc.want)
		}
	}
}

// TestSSHConfigUsesStrictOpenSSHOptions verifies a real parser applies isolated
// trust to managed aliases and leaves top-level user defaults in effect.
func TestSSHConfigUsesStrictOpenSSHOptions(t *testing.T) {
	ssh, err := exec.LookPath("ssh")
	if err != nil {
		t.Skip("OpenSSH is unavailable")
	}
	paths := sshConfigFixture(t)
	if err := os.WriteFile(paths.ConfigFile, []byte("ServerAliveInterval 27\nHost *\n    StrictHostKeyChecking no\n"+
		"    ProxyUseFdpass yes\n    ControlMaster auto\n    ControlPath /tmp/example-control\n"), 0600); err != nil {
		t.Fatal(err)
	}
	const origin = "https://controller.test"
	if err := reconcileSSHConfig(paths, origin, "account-a", []sshAliasWorkspace{{ID: "workspace-one", Name: "Project"}}); err != nil {
		t.Fatal(err)
	}
	alias, pin, err := sshHostPin(origin, "workspace-one", paths.PinDir)
	if err != nil {
		t.Fatal(err)
	}
	get := func(host string) string {
		t.Helper()
		out, err := exec.CommandContext(t.Context(), ssh, "-F", paths.ConfigFile, "-G", host).CombinedOutput()
		if err != nil {
			t.Fatalf("ssh -G %s: %v, %s", host, err, out)
		}
		return string(out)
	}
	managed := get("shiftbox-project")
	for _, option := range []string{
		"hostkeyalias " + alias,
		"userknownhostsfile " + pin,
		"stricthostkeychecking true",
		"globalknownhostsfile none",
		"hostkeyalgorithms ssh-ed25519",
		"proxyusefdpass no",
		"controlmaster false",
		"serveraliveinterval 27",
		"proxy --origin 'https://controller.test' --account 'account-a' --workspace 'workspace-one'",
	} {
		if !strings.Contains(managed, option) {
			t.Errorf("ssh -G managed missing %q", option)
		}
	}
	if strings.Contains(managed, "controlpath /tmp/example-control") {
		t.Error("managed SSH alias inherited a multiplex control path")
	}
	if other := get("unrelated"); !strings.Contains(other, "serveraliveinterval 27") ||
		!strings.Contains(other, "stricthostkeychecking false") ||
		!strings.Contains(other, "controlpath /tmp/example-control") {
		t.Errorf("ssh -G unrelated missing preserved user defaults: %q", other)
	}
}

// TestSSHConfigRefusesBrokenManagedBlock verifies an incomplete marker never
// causes the helper to overwrite the user's existing file.
func TestSSHConfigRefusesBrokenManagedBlock(t *testing.T) {
	paths := sshConfigFixture(t)
	const original = "# BEGIN SHIFTBOX WORKSPACE SSH\nHost user-project\n"
	if err := os.WriteFile(paths.ConfigFile, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	if err := reconcileSSHConfig(paths, "https://controller.test", "account", nil); err == nil {
		t.Fatal("reconcile accepted incomplete ownership markers")
	}
	if got := readSSHConfigFixture(t, paths.ConfigFile); got != original {
		t.Errorf("reconcile overwrote incomplete config with %q", got)
	}
}

// TestSSHConfigPreservesIncludedName prevents a helper stanza from taking an
// alias that a user declares in a quoted, relative Include.
func TestSSHConfigPreservesIncludedName(t *testing.T) {
	paths := sshConfigFixture(t)
	const original = "Include \"team SSH.conf\"\n"
	if err := os.WriteFile(paths.ConfigFile, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(paths.ConfigFile), "team SSH.conf"),
		[]byte("Host shiftbox-project\n    HostName team.internal\n"), 0600); err != nil {
		t.Fatal(err)
	}
	err := reconcileSSHConfig(paths, "https://controller.test", "account", []sshAliasWorkspace{{ID: "workspace-one", Name: "Project"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := readSSHConfigFixture(t, paths.ConfigFile); !strings.Contains(got, "Host shiftbox-project-2\n") ||
		!strings.HasSuffix(got, original) {
		t.Errorf("reconciled config = %q, want distinct alias and original Include", got)
	}
}

func sshConfigFixture(t *testing.T) sshConfigPaths {
	t.Helper()
	dir := t.TempDir()
	return sshConfigPaths{
		ConfigFile:       filepath.Join(dir, "config"),
		AliasStateFile:   filepath.Join(dir, "helper", "aliases.json"),
		HelperExecutable: filepath.Join(dir, "ShiftBox Local Helper.app", "Contents", "MacOS", "helper"),
		PinDir:           filepath.Join(dir, "helper", "host pins"),
	}
}

func readSSHConfigFixture(t *testing.T, file string) string {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
