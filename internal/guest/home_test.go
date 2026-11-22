package guest

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"syscall"
	"testing"
)

// TestDevContainerRetainsDeveloperHome verifies that the discovered home is
// seeded with the developer's ownership and repaired on restart before hooks.
func TestDevContainerRetainsDeveloperHome(t *testing.T) {
	state := t.TempDir()
	project := filepath.Join(state, "project")
	if err := os.Mkdir(project, 0700); err != nil {
		t.Fatal(err)
	}
	initial := strings.Repeat("a", 64)
	replacement := strings.Repeat("b", 64)
	uid, gid := os.Getuid(), os.Getgid()
	wrongUID, wrongGID := uid, gid
	if uid == 0 {
		uid, gid = 1001, 1002
	} else {
		groups, err := os.Getgroups()
		if err != nil {
			t.Fatal(err)
		}
		for _, group := range groups {
			if group != gid {
				wrongGID = group
				break
			}
		}
	}
	outside := filepath.Join(state, "outside")
	if err := os.WriteFile(outside, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(outside, wrongUID, wrongGID); err != nil {
		t.Fatal(err)
	}
	checkOwner := func(path string, wantUID, wantGID int) {
		t.Helper()
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		stat := info.Sys().(*syscall.Stat_t)
		if int(stat.Uid) != wantUID || int(stat.Gid) != wantGID {
			t.Errorf("owner of %q = %d:%d, want %d:%d", path, stat.Uid, stat.Gid, wantUID, wantGID)
		}
	}
	home := filepath.Join(state, "retained-home", "data")
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
					mounted = slices.Contains(args, "type=bind,source="+home+",target=/srv/developer")
					checkOwner(filepath.Join(home, ".profile"), uid, gid)
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
						return []byte(fmt.Sprintf("%d %d\n/srv/developer\n", uid+1, gid+1)), nil
					}
					return []byte(fmt.Sprintf("%d %d\n/srv/developer\n", uid, gid)), nil
				}
				return []byte(fmt.Sprintf("%d %d\n", uid, gid)), nil
			case "cp":
				if executable != "/usr/bin/docker" || args[2] != initial+":/srv/developer/." {
					t.Fatalf("home copy = %q %q, want the discovered home from the first container", executable, args)
				}
				// Simulate files and directories installed by Dockerfile steps running as root.
				config := filepath.Join(args[3], ".config")
				if err := os.Mkdir(config, 0700); err != nil {
					return nil, err
				}
				profile := filepath.Join(args[3], ".profile")
				if err := os.WriteFile(profile, []byte("original home contents"), 0600); err != nil {
					return nil, err
				}
				settings := filepath.Join(config, "settings")
				if err := os.WriteFile(settings, nil, 0600); err != nil {
					return nil, err
				}
				link := filepath.Join(args[3], "link")
				if err := os.Symlink(outside, link); err != nil {
					return nil, err
				}
				for _, path := range []string{profile, settings, config, link} {
					if err := os.Lchown(path, wrongUID, wrongGID); err != nil {
						return nil, err
					}
				}
				return nil, nil
			case "run-user-commands":
				checkOwner(filepath.Join(home, ".profile"), uid, gid)
				checkOwner(filepath.Join(home, ".config", "settings"), uid, gid)
				checkOwner(filepath.Join(home, ".config"), uid, gid)
				checkOwner(filepath.Join(home, "link"), uid, gid)
				checkOwner(outside, wrongUID, wrongGID)
				return []byte(`{"outcome":"success"}`), nil
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
	if err := os.Chown(filepath.Join(home, ".profile"), wrongUID, wrongGID); err != nil {
		t.Fatal(err)
	}
	stages = nil
	if _, err := d.ensure(t.Context()); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(stages, "cp") {
		t.Errorf("restart copied an already retained home: %v", stages)
	}
	if err := os.Chown(filepath.Join(home, ".profile"), wrongUID, wrongGID); err != nil {
		t.Fatal(err)
	}
	changedOwner = true
	if _, err := d.ensure(t.Context()); err == nil {
		t.Error("ensure accepted a retained home owned by a different developer identity")
	}
	checkOwner(filepath.Join(home, ".profile"), wrongUID, wrongGID)
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
