package guest

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// TestDevContainerRetainsDeveloperHome verifies that the discovered home is
// seeded from the first container and mounted before remote hooks run.
func TestDevContainerRetainsDeveloperHome(t *testing.T) {
	state := t.TempDir()
	project := filepath.Join(state, "project")
	if err := os.Mkdir(project, 0700); err != nil {
		t.Fatal(err)
	}
	initial := strings.Repeat("a", 64)
	replacement := strings.Repeat("b", 64)
	var stages []string
	var mounted bool
	var changedOwner bool
	d := devContainer{workspace: project, repository: &repositoryPreparation{}, retainHome: true,
		setProjectOwner: func(context.Context, string, int, int) error { return nil },
		run: func(_ context.Context, executable string, args ...string) ([]byte, error) {
			stages = append(stages, args[0])
			switch args[0] {
			case "up":
				id := initial
				if slices.Contains(args, "--remove-existing-container") {
					mounted = slices.Contains(args, "type=bind,source="+filepath.Join(state, "retained-home", "data")+",target=/srv/developer")
				}
				if mounted {
					id = replacement
				}
				return []byte(`{"outcome":"success","containerId":"` + id + `","mergedConfiguration":{"remoteUser":"developer"}}`), nil
			case "container":
				if args[1] == "inspect" {
					if !mounted {
						return []byte("[]"), nil
					}
					return []byte(`[{"Type":"bind","Source":"` + filepath.Join(state, "retained-home", "data") + `","Destination":"/srv/developer","RW":true}]`), nil
				}
				if strings.Contains(args[len(args)-1], "getent") {
					if changedOwner {
						return []byte("1001 1002\n/srv/developer\n"), nil
					}
					return []byte(fmt.Sprintf("%d %d\n/srv/developer\n", os.Getuid(), os.Getgid())), nil
				}
				return []byte(fmt.Sprintf("%d %d\n", os.Getuid(), os.Getgid())), nil
			case "cp":
				if executable != "/usr/bin/docker" || args[2] != initial+":/srv/developer/." {
					t.Fatalf("home copy = %q %q, want the discovered home from the first container", executable, args)
				}
				return nil, os.WriteFile(filepath.Join(args[3], ".profile"), []byte("original home contents"), 0600)
			default:
				return []byte(`{"outcome":"success"}`), nil
			}
		},
	}
	connection, err := d.ensure(t.Context())
	if err != nil || connection.ID != replacement || !mounted {
		t.Fatalf("ensure = %+v, %v, want the replacement with retained home", connection, err)
	}
	if want := []string{"up", "container", "container", "cp", "container", "up", "container", "container", "container", "run-user-commands"}; !reflect.DeepEqual(stages, want) {
		t.Errorf("startup stages = %v, want %v", stages, want)
	}
	data, err := os.ReadFile(filepath.Join(state, "retained-home", "data", ".profile"))
	if err != nil || string(data) != "original home contents" {
		t.Fatalf("retained home content = %q, %v, want original home contents", data, err)
	}
	stages = nil
	if _, err := d.ensure(t.Context()); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(stages, "cp") {
		t.Errorf("restart copied an already retained home: %v", stages)
	}
	changedOwner = true
	if _, err := d.ensure(t.Context()); err == nil {
		t.Error("ensure accepted a retained home owned by a different developer identity")
	}
}

// TestRetainedHomeRejectsUnexpectedMount verifies that a different mount at
// the developer home cannot be mistaken for ShiftBox's retained data.
func TestRetainedHomeRejectsUnexpectedMount(t *testing.T) {
	d := devContainer{workspace: filepath.Join(t.TempDir(), "project")}
	run := func(context.Context, string, ...string) ([]byte, error) {
		return []byte(`[{"Type":"volume","Source":"/var/lib/docker/volumes/unrelated/_data","Destination":"/srv/developer","RW":true}]`), nil
	}
	if mounted, err := d.hasRetainedHomeMount(t.Context(), strings.Repeat("a", 64), "/srv/developer", run); mounted || err == nil {
		t.Fatalf("home mount = %v, %v, want rejection of the unrelated volume", mounted, err)
	}
}
