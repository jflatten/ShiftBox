package guest

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestGuestRebuild checks that only a container with retained data can be
// replaced, readiness requires the new mounts, and a replay cannot replace it twice.
func TestGuestRebuild(t *testing.T) {
	for _, test := range []struct {
		name       string
		invalid    string
		wantUp     int
		wantStatus string
	}{
		{name: "retained data", wantUp: 1, wantStatus: "succeeded"},
		{name: "old home is missing", invalid: "old-home", wantStatus: "failed"},
		{name: "old checkout is missing", invalid: "old-project", wantStatus: "failed"},
		{name: "new checkout is missing", invalid: "new-project", wantUp: 1, wantStatus: "failed"},
		{name: "new home is missing", invalid: "new-home", wantUp: 1, wantStatus: "failed"},
		{name: "new app mount points to empty volume", invalid: "new-app", wantUp: 1, wantStatus: "failed"},
		{name: "declared app mount absent before rebuild", invalid: "old-app", wantStatus: "failed"},
		{name: "old container reused", invalid: "same-id", wantUp: 1, wantStatus: "failed"},
		{name: "ambiguous inventory", invalid: "multiple", wantStatus: "failed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			state := t.TempDir()
			project := filepath.Join(state, "project")
			home := filepath.Join(state, "retained-home", "data")
			for _, dir := range []string{project, home} {
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
			}
			for path, value := range map[string]string{
				filepath.Join(state, "retained-home", "target"): "/home/developer",
				filepath.Join(state, "retained-home", "owner"):  "1000:1000",
			} {
				if err := os.WriteFile(path, []byte(value), 0600); err != nil {
					t.Fatal(err)
				}
			}
			oldID, newID := strings.Repeat("a", 64), strings.Repeat("b", 64)
			up, later := 0, 0
			path := filepath.Join(state, "operation.json")
			d := devContainer{workspace: project, retainHome: true, run: func(_ context.Context, executable string, args ...string) ([]byte, error) {
				switch {
				case executable == "/usr/bin/docker" && args[1] == "ls":
					if !slices.Contains(args, "--all") || !slices.Contains(args, "label="+developmentLabel) {
						t.Fatalf("rebuild inventory command = %q, want all managed development containers", args)
					}
					if test.invalid == "multiple" {
						return []byte(oldID + "\n" + newID + "\n"), nil
					}
					return []byte(oldID + "\n"), nil
				case executable == "/usr/bin/docker" && args[1] == "inspect":
					if slices.Contains(args, "{{json .HostConfig.Mounts}}") {
						if args[len(args)-1] != oldID {
							t.Fatal("rebuild must compare the replacement with the old container's named volume declarations")
						}
						return []byte(`[{"Type":"volume","Source":"app-data","Target":"/app/data"},{"Type":"volume","Target":"/cache"}]`), nil
					}
					prefix := "old-"
					if args[len(args)-1] == newID {
						prefix = "new-"
					}
					mounts := []string{}
					if test.invalid != prefix+"project" {
						mounts = append(mounts, fmt.Sprintf(`{"Type":"bind","Source":%q,"Destination":"/workspaces/project","RW":true}`, project))
					}
					if test.invalid != prefix+"home" {
						mounts = append(mounts, fmt.Sprintf(`{"Type":"bind","Source":%q,"Destination":"/home/developer","RW":true}`, home))
					}
					mounts = append(mounts, fmt.Sprintf(`{"Type":"volume","Name":%q,"Destination":"/app/data","RW":true}`, "app-data"))
					if test.invalid == prefix+"app" {
						mounts[len(mounts)-1] = `{"Type":"volume","Name":"other-data","Destination":"/app/data","RW":true}`
					}
					mounts = append(mounts, fmt.Sprintf(`{"Type":"volume","Name":%q,"Destination":"/cache","RW":true}`, prefix+"anonymous"))
					return []byte("[" + strings.Join(mounts, ",") + "]"), nil
				case executable == "/usr/bin/docker" && args[1] == "exec":
					return []byte("1000 1000\n/home/developer\n"), nil
				case executable == devContainerCLI && args[0] == "up":
					up++
					if !slices.Contains(args, "--remove-existing-container") || !slices.Contains(args, "--skip-post-create") ||
						!slices.Contains(args, "type=bind,source="+home+",target=/home/developer") {
						t.Fatalf("rebuild up arguments = %q, want replacement and retained home before hooks", args)
					}
					journal, err := loadLocalJournal(path)
					if err != nil || journal.state.Operation.Phase != localRequired {
						t.Fatalf("rebuild journal = %v, %v, want durable pre-effect phase", journal, err)
					}
					id := newID
					if test.invalid == "same-id" {
						id = oldID
					}
					return []byte(`{"outcome":"success","containerId":"` + id + `","mergedConfiguration":{}}`), nil
				case executable == devContainerCLI && args[0] == "run-user-commands":
					if !slices.Contains(args, newID) {
						t.Fatalf("rebuild hook arguments = %q, want new container %s", args, newID)
					}
					later++
					return []byte(`{"outcome":"success"}`), nil
				default:
					t.Fatalf("unexpected rebuild command %q %q", executable, args)
					return nil, nil
				}
			}}
			const dispatch = `{"id":"0555292b-088e-4f5b-8ac6-8be6045ae630","version":7,"type":"rebuild",
				"command":"rebuild-development-container","arguments":{"devcontainerPath":".devcontainer/devcontainer.json"}}`
			for range 3 {
				journal, err := loadLocalJournal(path)
				if err != nil {
					t.Fatal(err)
				}
				if err := applyGuestPoll(t.Context(), journal, d, 4, "running", json.RawMessage(dispatch)); err != nil {
					t.Fatal(err)
				}
			}
			journal, err := loadLocalJournal(path)
			if err != nil {
				t.Fatal(err)
			}
			report := journal.report()
			if up != test.wantUp || report.Status != test.wantStatus {
				t.Errorf("rebuild %q = %+v after %d replacements, want %s after %d", test.name, report, up, test.wantStatus, test.wantUp)
			}
			if test.wantStatus == "succeeded" && (report.Evidence.ContainerID != newID || later != 2) {
				t.Errorf("rebuild completion = %+v and %d hook invocations, want new container %s and two stages", report, later, newID)
			}
			if test.wantStatus == "failed" && (report.Evidence.WaitForReached || later != 0) {
				t.Errorf("unsafe rebuild = %+v and %d hook invocations, want no readiness or hooks", report, later)
			}
		})
	}
}

// TestInterruptedGuestRebuild checks that an agent restart after replacement
// began cannot repeat the destructive command with the same operation ID.
func TestInterruptedGuestRebuild(t *testing.T) {
	path := filepath.Join(t.TempDir(), "operation.json")
	dispatch := ensureDispatch()
	dispatch.Command = rebuildDevelopmentCommand
	journal, err := loadLocalJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := journal.accept(dispatch); err != nil {
		t.Fatal(err)
	}
	interrupted := *journal.state.Operation
	interrupted.Phase = localRequired
	if err := journal.saveOperation(interrupted); err != nil {
		t.Fatal(err)
	}
	journal, err = loadLocalJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	runtime := devContainer{run: func(_ context.Context, executable string, args ...string) ([]byte, error) {
		t.Fatalf("interrupted rebuild unexpectedly ran %q %q", executable, args)
		return nil, nil
	}}
	report, err := journal.ensureDevelopment(t.Context(), dispatch, runtime)
	if err != nil || report.Status != "failed" || report.Problem != problemInterrupted || report.Evidence.WaitForReached {
		t.Errorf("interrupted rebuild = %+v, %v, want a durable failure without readiness", report, err)
	}
}
