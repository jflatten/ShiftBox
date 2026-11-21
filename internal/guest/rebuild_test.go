package guest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
				filepath.Join(state, "retained-home", "owner"):  homeOwner(os.Getuid(), os.Getgid()),
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
					return []byte(fmt.Sprintf("%d %d\n/home/developer\n", os.Getuid(), os.Getgid())), nil
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

// TestGuestRebuildPublishesWithoutLookup exercises the explicit poll dispatch
// with a dirty checkout. The worker offers the CLI's actual immutable result,
// and a controller decline leaves the replacement and retained data usable.
func TestGuestRebuildPublishesWithoutLookup(t *testing.T) {
	const configPath = ".devcontainer/devcontainer.json"
	const repo = "https://github.com/example/project.git"
	const ref = "main"
	const opID = "0555292b-088e-4f5b-8ac6-8be6045ae630"
	const dirty = "uncommitted project changes\n"
	const retained = "existing developer settings\n"
	const homeTarget = "/home/developer"
	project, digest := builtImageWorkspaceFixture(t, configPath)
	state := filepath.Dir(project)
	home := filepath.Join(state, "retained-home", "data")
	receiptPath := repositoryReceipt(project)
	for _, path := range []string{home, filepath.Dir(receiptPath)} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	receipt, err := json.Marshal(repositoryEvidence{repositorySource: repositorySource{URL: repo, Ref: ref, Config: configPath},
		Path: project, Commit: strings.Repeat("c", 40)})
	if err != nil {
		t.Fatal(err)
	}
	for path, value := range map[string]string{
		receiptPath: string(receipt), filepath.Join(project, "unfinished.txt"): dirty, filepath.Join(home, ".profile"): retained,
		filepath.Join(state, "retained-home", "target"): homeTarget,
		filepath.Join(state, "retained-home", "owner"):  homeOwner(os.Getuid(), os.Getgid()),
	} {
		if err := os.WriteFile(path, []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	oldID, newID := strings.Repeat("a", 64), strings.Repeat("b", 64)
	build := "sha256:" + strings.Repeat("d", 64)
	wantRequest := builtImageNegotiation{OperationID: opID, DispatchVersion: 7, ConfigSHA256: digest}
	var requests []string
	client := &http.Client{Transport: proofRoundTripper(func(r *http.Request) (*http.Response, error) {
		requests = append(requests, r.URL.Path)
		if r.Method != http.MethodPost || r.URL.Path != builtImageAPIPath+"/publications" {
			t.Errorf("cache request = %s %s, want only a publication request", r.Method, r.URL.Path)
		} else {
			var got builtImageNegotiation
			if err := decodeAgentJSON(r.Body, &got); err != nil || got != wantRequest {
				t.Errorf("publication request = %+v, %v, want %+v", got, err, wantRequest)
			}
		}
		return &http.Response{StatusCode: http.StatusNoContent, Body: io.NopCloser(strings.NewReader(""))}, nil
	})}
	transport := newBuiltImageGuestTransport(state, "https://controller.example.test"+pollPath, client)
	workspace := devContainer{workspace: project, retainHome: true, builtTransport: transport,
		repository: &repositoryPreparation{project: project, run: func(context.Context, string, string, ...string) ([]byte, error) {
			t.Error("rebuild attempted Git commands in a retained worktree")
			return nil, fmt.Errorf("unexpected Git command")
		}}, setProjectOwner: func(context.Context, string, int, int) error { return nil },
	}
	var up, hooks int
	configAfterInit := builtImageConfigFixture + "\r\n// initializer changed the working copy after CLI parsing\r\n"
	workspace.run = func(ctx context.Context, program string, args ...string) ([]byte, error) {
		switch {
		case program == "/usr/bin/docker" && args[1] == "ls":
			return []byte(oldID + "\n"), nil
		case program == "/usr/bin/docker" && args[1] == "inspect":
			if slices.Contains(args, "{{json .HostConfig.Mounts}}") {
				return json.Marshal([]namedVolume{{Type: "volume", Source: "app-data", Target: "/app/data"}})
			}
			return json.Marshal([]dockerMount{
				{Type: "bind", Source: project, Destination: "/workspaces/project", RW: true},
				{Type: "bind", Source: home, Destination: homeTarget, RW: true},
				{Type: "volume", Name: "app-data", Destination: "/app/data", RW: true},
			})
		case program == "/usr/bin/docker" && args[1] == "exec":
			identity := fmt.Sprintf("%d %d\n", os.Getuid(), os.Getgid())
			if strings.Contains(args[len(args)-1], "getent") {
				identity += homeTarget + "\n"
			}
			return []byte(identity), nil
		case program == devContainerCLI && args[0] == "up":
			up++
			if image, _ := builtImageSelectionFromContext(ctx); image != "" || !slices.Contains(args, "--remove-existing-container") ||
				!slices.Contains(args, "type=bind,source="+home+",target="+homeTarget) {
				t.Errorf("rebuild up = image %q, arguments %q, want ordinary replacement with retained home", image, args)
			}
			if err := os.WriteFile(filepath.Join(project, configPath), []byte(configAfterInit), 0600); err != nil {
				return nil, err
			}
			return builtImageUpFixture(newID, digest, build, false), nil
		case program == devContainerCLI && args[0] == "run-user-commands" && slices.Contains(args, newID):
			hooks++
			return []byte(`{"outcome":"success"}`), nil
		default:
			return nil, fmt.Errorf("unexpected rebuild command %q %q", program, args)
		}
	}
	dispatch := fmt.Appendf(nil, `{"id":%q,"version":7,"type":"rebuild","command":"rebuild-development-container",`+
		`"arguments":{"repositoryUrl":%q,"repositoryRef":%q,"devcontainerPath":%q}}`, opID, repo, ref, configPath)
	journal, err := loadLocalJournal(filepath.Join(state, "operation.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := applyGuestPoll(t.Context(), journal, workspace, 4, "running", dispatch); err != nil {
		t.Fatal(err)
	}
	if report := journal.report(); !report.Evidence.WaitForReached || report.Evidence.ContainerID != newID {
		t.Fatalf("local rebuild = %+v, want ready replacement %s before publication", report, newID)
	}
	select {
	case candidate := <-transport.queue:
		if candidate.imageID != build || candidate.request != wantRequest {
			t.Errorf("queued rebuild image = %+v, want %s and %+v from the CLI receipt", candidate, build, wantRequest)
		}
		transport.publish(t.Context(), candidate)
	default:
		t.Fatal("explicit rebuild produced a valid image without queuing its publication")
	}
	for range 2 {
		if err := applyGuestPoll(t.Context(), journal, workspace, 4, "running", dispatch); err != nil {
			t.Fatal(err)
		}
	}
	if got := journal.report(); got.Status != "succeeded" || up != 1 || hooks != 2 || len(transport.queue) != 0 {
		t.Errorf("rebuild after publication decline = %+v, up=%d, hooks=%d, queued=%d, want one successful replacement with both hook stages", got, up, hooks, len(transport.queue))
	}
	if want := []string{builtImageAPIPath + "/publications"}; !slices.Equal(requests, want) {
		t.Errorf("built image requests = %q, want %q", requests, want)
	}
	for path, expected := range map[string]string{filepath.Join(project, "unfinished.txt"): dirty,
		filepath.Join(home, ".profile"): retained, filepath.Join(project, configPath): configAfterInit} {
		if content, err := os.ReadFile(path); err != nil || string(content) != expected {
			t.Errorf("retained file %s = %q, %v, want %q", filepath.Base(path), content, err, expected)
		}
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
