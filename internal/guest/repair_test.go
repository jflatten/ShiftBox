package guest

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestGuestRepair verifies entry and exit through the fixed commands, an
// offline runtime, editable project files, and durable duplicate suppression.
func TestGuestRepair(t *testing.T) {
	for _, resumed := range []bool{false, true} {
		name := "initial delivery"
		if resumed {
			name = "lost create reply"
		}
		t.Run(name, func(t *testing.T) { testGuestRepair(t, resumed) })
	}
}

func testGuestRepair(t *testing.T, resumed bool) {
	t.Helper()
	state := t.TempDir()
	project := filepath.Join(state, "project")
	if err := os.Mkdir(project, 0700); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{"work.txt", ".devcontainer.json"} {
		if err := os.WriteFile(filepath.Join(project, file), []byte("preserve"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	imageID := "sha256:" + strings.Repeat("d", 64)
	containerID := strings.Repeat("e", 64)
	imageFile := filepath.Join(state, "repair-image")
	if err := os.WriteFile(imageFile, []byte(imageID+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	created, probes, removed := 0, 0, 0
	socketDir := filepath.Join(state, "ide")
	if err := os.Mkdir(socketDir, 0700); err != nil {
		t.Fatal(err)
	}
	d := devContainer{workspace: project, repairImageFile: imageFile, repairSocketDir: socketDir,
		run: func(_ context.Context, executable string, args ...string) ([]byte, error) {
			if executable != "/usr/bin/docker" || args[0] != "container" {
				t.Fatalf("repair command = %q %q, want Docker container commands only", executable, args)
			}
			switch args[1] {
			case "ls":
				if slices.Contains(args, "label="+repairLabel) && created > removed {
					return []byte(containerID), nil
				}
				return nil, nil
			case "run":
				created++
				for _, want := range []string{"--pull=never", "--network=none", "--read-only", "--cap-drop=ALL", repairLabel, imageID,
					"type=bind,src=" + project + ",dst=/project", "type=bind,src=" + socketDir + ",dst=" + repairDirectory} {
					if !slices.Contains(args, want) {
						t.Errorf("repair command %q missing %q", args, want)
					}
				}
				return []byte(containerID + "\n"), nil
			case "inspect":
				return []byte(imageID + " true\n"), nil
			case "exec":
				probes++
				return nil, nil
			case "stop":
				return nil, nil
			case "rm":
				removed++
				if args[len(args)-1] != containerID || slices.Contains(args, "--volumes") {
					t.Fatalf("repair exit command = %q, want removal of only the repair container", args)
				}
				return nil, nil
			default:
				t.Fatalf("unexpected repair command %q", args)
				return nil, nil
			}
		},
	}
	for _, step := range []struct{ id, command, report string }{
		{"99999999-0000-4000-8000-000000000001", "enter-repair", "repair.enter"},
		{"99999999-0000-4000-8000-000000000002", "exit-repair", "repair.exit"},
	} {
		dispatch := ensureDispatch()
		dispatch.ID = step.id
		path := filepath.Join(state, "operation.json")
		raw, err := json.Marshal(map[string]any{"id": dispatch.ID, "version": dispatch.Version, "type": "repair", "command": step.command})
		if err != nil {
			t.Fatal(err)
		}
		if resumed && step.command == "enter-repair" {
			// Simulate the daemon creating the container before the agent lost its reply.
			journal, err := loadLocalJournal(path)
			if err != nil {
				t.Fatal(err)
			}
			dispatch.Command = step.command
			if err := journal.accept(dispatch); err != nil {
				t.Fatal(err)
			}
			op := *journal.state.Operation
			op.Phase = localRequired
			if err := journal.saveOperation(op); err != nil {
				t.Fatal(err)
			}
			created = 1
		}
		for range 2 {
			journal, err := loadLocalJournal(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := applyGuestPoll(t.Context(), journal, d, dispatch.Generation, "running", raw); err != nil {
				t.Fatalf("repair dispatch %q = %v", step.command, err)
			}
			report := journal.report()
			if report.Status != "succeeded" || report.Step != step.report || report.Evidence.WaitForReached ||
				(report.Evidence.ContainerID != "") != (step.command == "enter-repair") {
				t.Fatalf("repair dispatch %q = %+v, want scoped repair evidence", step.command, report)
			}
		}
	}
	if created != 1 || probes != 1 || removed != 1 {
		t.Fatalf("repair effects = %d starts, %d probes, %d removals, want one each", created, probes, removed)
	}
	for _, file := range []string{"work.txt", ".devcontainer.json"} {
		if data, err := os.ReadFile(filepath.Join(project, file)); err != nil || string(data) != "preserve" {
			t.Errorf("repair %s = %q, %v, want retained contents", file, data, err)
		}
	}
}
