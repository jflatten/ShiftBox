package guest

import (
	"os"
	"path/filepath"
	"testing"
)

// TestWorkspaceCommandRefresh keeps the file-mounted launcher inode stable
// while signed agent releases replace the binary visible through the directory mount.
func TestWorkspaceCommandRefresh(t *testing.T) {
	directory := t.TempDir()
	executable := filepath.Join(t.TempDir(), "selected-agent")
	if err := os.WriteFile(executable, []byte("selected release one"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := prepareWorkspaceCommand(executable, directory); err != nil {
		t.Fatal(err)
	}
	launcher := filepath.Join(directory, "shiftbox-launcher")
	first, err := os.Stat(launcher)
	if err != nil {
		t.Fatal(err)
	}
	if first.Mode().Perm() != 0755 {
		t.Errorf("workspace launcher mode = %o, want 755 for the container remote user", first.Mode().Perm())
	}
	content, err := os.ReadFile(launcher)
	if err != nil || string(content) != workspaceLauncher {
		t.Fatalf("workspace launcher = %q, %v; want stable shell launcher", content, err)
	}
	if err := os.WriteFile(executable, []byte("selected release two"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := prepareWorkspaceCommand(executable, directory); err != nil {
		t.Fatal(err)
	}
	second, err := os.Stat(launcher)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(first, second) {
		t.Error("workspace launcher inode changed, which would leave existing file bind mounts stale")
	}
	content, err = os.ReadFile(filepath.Join(directory, "shiftbox"))
	if err != nil || string(content) != "selected release two" {
		t.Errorf("published workspace binary = %q, %v; want selected release two", content, err)
	}
}
