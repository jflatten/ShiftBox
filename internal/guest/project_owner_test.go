package guest

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestOwnedProjectRunsHooksAfterOwnership checks the order on a newly cloned
// project, where a non-root developer cannot write to the initial checkout.
func TestOwnedProjectRunsHooksAfterOwnership(t *testing.T) {
	project := filepath.Join(t.TempDir(), "project")
	if err := os.Mkdir(project, 0700); err != nil {
		t.Fatal(err)
	}
	id := strings.Repeat("a", 64)
	var stages []string
	var ownershipErr error
	d := devContainer{workspace: project, repository: &repositoryPreparation{},
		run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			stages = append(stages, args[0])
			switch args[0] {
			case "up":
				if !strings.Contains(strings.Join(args, " "), "--skip-post-create") {
					t.Error("container startup would run remote hooks before ownership is ready")
				}
				return []byte(`{"outcome":"success","containerId":"` + id + `","mergedConfiguration":{"waitFor":"postCreateCommand","remoteUser":"developer"}}`), nil
			case "container":
				if want := []string{"container", "exec", "--user", "developer", "--", id, "/bin/sh", "-c", "printf '%s %s\\n' \"$(id -u)\" \"$(id -g)\""}; !reflect.DeepEqual(args, want) {
					t.Errorf("developer identity command = %q, want %q", args, want)
				}
				return []byte("1001 1002\n"), nil
			default:
				return []byte(`{"outcome":"success"}`), nil
			}
		},
		setProjectOwner: func(_ context.Context, path string, uid, gid int) error {
			stages = append(stages, "ownership")
			if path != project || uid != 1001 || gid != 1002 {
				t.Errorf("project ownership = %q %d:%d, want configured developer", path, uid, gid)
			}
			return ownershipErr
		},
	}
	if _, err := d.ensure(t.Context()); err != nil {
		t.Fatal(err)
	}
	if want := []string{"up", "container", "ownership", "run-user-commands"}; !reflect.DeepEqual(stages, want) {
		t.Errorf("startup stages = %v, want %v", stages, want)
	}
	if err := os.Remove(project + ".owner"); err != nil {
		t.Fatal(err)
	}
	stages = nil
	ownershipErr = errors.New("checkout filesystem refused ownership")
	if connection, err := d.ensure(t.Context()); !errors.Is(err, ownershipErr) || connection != (containerConnection{}) {
		t.Fatalf("ensure with failed ownership = %+v, %v, want ownership failure", connection, err)
	}
	if want := []string{"up", "container", "ownership"}; !reflect.DeepEqual(stages, want) {
		t.Errorf("failed startup stages = %v, want %v", stages, want)
	}
}

// TestProjectOwnerKeepsReceipt checks that stop/start does not recursively
// reclaim a checkout from its developer on every launch.
func TestProjectOwnerKeepsReceipt(t *testing.T) {
	project := filepath.Join(t.TempDir(), "project")
	if err := os.Mkdir(project, 0700); err != nil {
		t.Fatal(err)
	}
	d := devContainer{workspace: project, run: func(context.Context, string, ...string) ([]byte, error) {
		return []byte("1001 1002\n"), nil
	}}
	calls := 0
	d.setProjectOwner = func(context.Context, string, int, int) error {
		calls++
		return nil
	}
	for range 2 {
		if err := d.prepareProjectOwner(t.Context(), strings.Repeat("a", 64), "developer"); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Errorf("ownership applied %d times, want once", calls)
	}
}

// TestAssignProjectOwnerDoesNotFollowLink exercises a checkout containing a
// symlink outside the workspace and verifies directory traversal stays local.
func TestAssignProjectOwnerDoesNotFollowLink(t *testing.T) {
	parent := t.TempDir()
	project := filepath.Join(parent, "project")
	if err := os.Mkdir(project, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(parent, "missing"), filepath.Join(project, "link")); err != nil {
		t.Fatal(err)
	}
	if err := assignProjectOwner(t.Context(), project, os.Getuid(), os.Getgid()); err != nil {
		t.Fatalf("assign owner with dangling symlink = %v, want success", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := assignProjectOwner(ctx, project, os.Getuid(), os.Getgid()); !errors.Is(err, context.Canceled) {
		t.Fatalf("assign owner after cancellation = %v, want context.Canceled", err)
	}
}
