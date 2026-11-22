package guest

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

const builtImageConfigFixture = "{\r\n  // exact JSONC bytes\r\n  \"image\": \"debian:stable\",\r\n}\r\n"

// TestBuiltImageFinishRetriesPending checks that the background worker backs
// off while the controller awaits rebuild success, stops on a terminal
// response, and respects the upload grant deadline.
func TestBuiltImageFinishRetriesPending(t *testing.T) {
	const scope = "0555292b-088e-4f5b-8ac6-8be6045ae630"
	for _, test := range []struct {
		name      string
		statuses  []int
		lifetime  time.Duration
		wantTimes []time.Duration
	}{
		{name: "pending backoff remains capped", statuses: []int{409, 409, 409, 409, 409, 200}, lifetime: 20 * time.Second,
			wantTimes: []time.Duration{0, time.Second, 3 * time.Second, 7 * time.Second, 12 * time.Second, 17 * time.Second}},
		{name: "grant expires during backoff", statuses: []int{409, 409}, lifetime: 2 * time.Second,
			wantTimes: []time.Duration{0, time.Second}},
		{name: "declined after pending", statuses: []int{409, 204}, lifetime: 20 * time.Second,
			wantTimes: []time.Duration{0, time.Second}},
		{name: "denied immediately", statuses: []int{403}, lifetime: 20 * time.Second, wantTimes: []time.Duration{0}},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				start := time.Now()
				var attempts []time.Duration
				client := &http.Client{Transport: proofRoundTripper(func(r *http.Request) (*http.Response, error) {
					wantPath := builtImageAPIPath + "/publications/" + scope + "/finish"
					if r.Method != http.MethodPost || r.URL.Path != wantPath || r.ContentLength != 0 {
						t.Errorf("finish request = %s %s with %d bytes, want empty POST %s", r.Method, r.URL.Path, r.ContentLength, wantPath)
					}
					index := len(attempts)
					attempts = append(attempts, time.Since(start))
					if index >= len(test.statuses) {
						return nil, errors.New("unexpected extra finish request")
					}
					status := test.statuses[index]
					var body string
					if status == http.StatusOK {
						body = `{"disposition":"promoted"}`
					}
					return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}, nil
				})}
				transport := newBuiltImageGuestTransport("", "https://controller.example.test"+pollPath, client)
				ctx, cancel := context.WithTimeout(t.Context(), test.lifetime)
				defer cancel()
				transport.finishPublication(ctx, scope)
				if !slices.Equal(attempts, test.wantTimes) {
					t.Errorf("finish attempts for statuses %v = %v, want %v", test.statuses, attempts, test.wantTimes)
				}
			})
		})
	}
}

// TestBuiltImageGuestRetainsHit verifies both fresh container creations use
// the same verified local image and unchanged config, while the required
// hooks run once on the recreated container with the retained developer home.
func TestBuiltImageGuestRetainsHit(t *testing.T) {
	const configPath = ".devcontainer/team/devcontainer.json"
	project, digest := builtImageWorkspaceFixture(t, configPath)
	initial, recreated := strings.Repeat("a", 64), strings.Repeat("b", 64)
	image := "sha256:" + strings.Repeat("c", 64)
	retainedHome := filepath.Join(filepath.Dir(project), "retained-home", "data")
	const developerHome = "/srv/developer"
	var steps []string
	workspace := devContainer{
		workspace: project, config: configPath, repository: &repositoryPreparation{}, retainHome: true,
		setProjectOwner: func(context.Context, string, int, int) error { return nil },
		builtImage: &builtImageGuest{
			lookup: func(_ context.Context, configSHA256 string) (string, error) {
				steps = append(steps, "lookup")
				if configSHA256 != digest {
					t.Errorf("lookup digest = %q, want %q for raw JSONC", configSHA256, digest)
				}
				return image, nil
			},
			publish: func(context.Context, devContainerBuiltImage) error {
				t.Error("a reused image was queued as a new build")
				return nil
			},
		},
	}
	workspace.run = func(ctx context.Context, executable string, args ...string) ([]byte, error) {
		if executable == devContainerCLI {
			switch args[0] {
			case "up":
				gotImage, gotDigest := builtImageSelectionFromContext(ctx)
				if gotImage != image || gotDigest != digest {
					t.Errorf("up image selection = (%s, %s), want (%s, %s)", gotImage, gotDigest, image, digest)
				}
				config := filepath.Join(project, configPath)
				if i := slices.Index(args, "--config"); i < 0 || i+1 >= len(args) || args[i+1] != config || slices.Contains(args, "--override-config") || !slices.Contains(args, "--skip-post-create") {
					t.Errorf("up arguments = %q, want original config and deferred post-create hooks", args)
				}
				if raw, err := os.ReadFile(config); err != nil || string(raw) != builtImageConfigFixture {
					t.Errorf("CLI config bytes = %q, %v, want the unchanged JSONC fixture", raw, err)
				}
				if slices.Contains(args, "--remove-existing-container") {
					steps = append(steps, "recreate")
					if !slices.Contains(args, "type=bind,source="+retainedHome+",target="+developerHome) {
						t.Error("recreation lost the retained developer home mount")
					}
					return builtImageUpFixture(recreated, digest, "", true), nil
				}
				steps = append(steps, "up")
				return builtImageUpFixture(initial, digest, "", true), nil
			case "run-user-commands":
				steps = append(steps, "hooks")
				if !slices.Contains(args, recreated) || !slices.Contains(args, "--skip-non-blocking-commands") {
					t.Errorf("required hooks = %q, want waitFor hooks on the recreated container", args)
				}
				return []byte(`{"outcome":"success"}`), nil
			}
		}
		if executable == "/usr/bin/docker" {
			switch {
			case args[0] == "cp":
				steps = append(steps, "seed home")
				if args[2] != initial+":"+developerHome+"/." {
					t.Errorf("copy source = %q, want discovered home on the first container", args[2])
				}
				return nil, os.WriteFile(filepath.Join(args[3], ".profile"), []byte("retained"), 0600)
			case args[0] == "container" && args[1] == "inspect":
				if args[len(args)-1] == recreated {
					return json.Marshal([]dockerMount{{Type: "bind", Source: retainedHome, Destination: developerHome, RW: true}})
				}
				return []byte(`[]`), nil
			case args[0] == "container" && args[1] == "exec":
				identity := fmt.Sprintf("%d %d\n", os.Getuid(), os.Getgid())
				if strings.Contains(args[len(args)-1], "getent") {
					identity += developerHome + "\n"
				}
				return []byte(identity), nil
			}
		}
		return nil, fmt.Errorf("unexpected fixture command %s %v", executable, args)
	}
	got, err := workspace.ensure(t.Context())
	want := containerConnection{ID: recreated, WaitFor: "postCreateCommand"}
	if err != nil || got != want {
		t.Fatalf("ensure(digest hit) = %+v, %v, want %+v", got, err, want)
	}
	if wantSteps := []string{"lookup", "up", "seed home", "recreate", "hooks"}; !slices.Equal(steps, wantSteps) {
		t.Errorf("digest hit steps = %q, want %q", steps, wantSteps)
	}
	if data, err := os.ReadFile(filepath.Join(retainedHome, ".profile")); err != nil || string(data) != "retained" {
		t.Errorf("retained home profile = %q, %v, want retained", data, err)
	}
}

// TestBuiltImageGuestFallsBackBeforeHooks checks that when lookup or local
// validation fails, the same normal CLI startup reaches its required hooks.
func TestBuiltImageGuestFallsBackBeforeHooks(t *testing.T) {
	validImage := "sha256:" + strings.Repeat("a", 64)
	for _, test := range []struct {
		name, lookupImage, cliImage string
		lookupErr                   error
	}{
		{name: "denied", lookupImage: validImage, lookupErr: errors.New("cache grant denied")},
		{name: "tag instead of a verified digest", lookupImage: "registry.example.test/image:latest"},
		{name: "image lost before CLI inspection", lookupImage: validImage, cliImage: validImage},
	} {
		t.Run(test.name, func(t *testing.T) {
			const configPath = ".devcontainer/devcontainer.json"
			project, digest := builtImageWorkspaceFixture(t, configPath)
			id := strings.Repeat("b", 64)
			if err := os.WriteFile(filepath.Join(project, ".devcontainer.json"), []byte("{}"), 0600); err != nil {
				t.Fatal(err)
			}
			var steps []string
			workspace := devContainer{workspace: project, repository: &repositoryPreparation{},
				setProjectOwner: func(context.Context, string, int, int) error { return nil },
				builtImage: &builtImageGuest{lookup: func(_ context.Context, configSHA256 string) (string, error) {
					steps = append(steps, "lookup")
					if configSHA256 != digest {
						t.Errorf("default config digest = %q, want %q before the root fallback file", configSHA256, digest)
					}
					return test.lookupImage, test.lookupErr
				}},
				run: func(ctx context.Context, executable string, args ...string) ([]byte, error) {
					if executable == "/usr/bin/docker" && args[0] == "container" && args[1] == "exec" {
						return []byte("1000 1000\n"), nil
					}
					switch args[0] {
					case "up":
						steps = append(steps, "normal setup")
						if image, _ := builtImageSelectionFromContext(ctx); image != test.cliImage || !slices.Contains(args, "--skip-post-create") {
							t.Errorf("up selection/arguments = %q %q, want image %q with hooks deferred", image, args, test.cliImage)
						}
						return builtImageUpFixture(id, digest, "sha256:"+strings.Repeat("b", 64), false), nil
					case "run-user-commands":
						steps = append(steps, "required hooks")
						return []byte(`{"outcome":"success"}`), nil
					}
					return nil, fmt.Errorf("unexpected fixture command %s %v", executable, args)
				},
			}
			got, err := workspace.ensure(t.Context())
			want := containerConnection{ID: id, WaitFor: "postCreateCommand"}
			if err != nil || got != want {
				t.Fatalf("ensure(fallback) = %+v, %v, want %+v", got, err, want)
			}
			if expected := []string{"lookup", "normal setup", "required hooks"}; !slices.Equal(steps, expected) {
				t.Errorf("fallback steps = %q, want %q", steps, expected)
			}
		})
	}
}

// TestBuiltImageGuestPublicationFailureKeepsIDE checks a failed enqueue
// cannot undo the connection evidence after an eligible image build.
func TestBuiltImageGuestPublicationFailureKeepsIDE(t *testing.T) {
	const configPath = ".devcontainer/devcontainer.json"
	project, digest := builtImageWorkspaceFixture(t, configPath)
	source := testCodeServerPackage(t)
	id, build := strings.Repeat("a", 64), "sha256:"+strings.Repeat("b", 64)
	var ideStarted bool
	var candidate devContainerBuiltImage
	workspace := devContainer{workspace: project, config: configPath, repository: &repositoryPreparation{}, ide: true, codeServerSource: source,
		setProjectOwner: func(context.Context, string, int, int) error { return nil },
		builtImage: &builtImageGuest{
			lookup: func(context.Context, string) (string, error) { return "", nil },
			publish: func(_ context.Context, built devContainerBuiltImage) error {
				candidate = built
				return errors.New("publication queue is full")
			},
		},
		run: func(_ context.Context, executable string, args ...string) ([]byte, error) {
			if executable == "/usr/bin/docker" && args[0] == "container" && args[1] == "inspect" {
				return json.Marshal([]dockerMount{{Type: "bind", Source: source, Destination: codeServerDir}})
			}
			if executable == "/usr/bin/docker" && args[0] == "container" && args[1] == "exec" {
				return []byte("1000 1000\n"), nil
			}
			switch args[0] {
			case "up":
				return builtImageUpFixture(id, digest, build, false), nil
			case "run-user-commands":
				return []byte(`{"outcome":"success"}`), nil
			case "exec":
				ideStarted = slices.Contains(args, startCodeServer)
				return nil, nil
			}
			return nil, fmt.Errorf("unexpected fixture command %s %v", executable, args)
		},
	}
	got, err := workspace.ensure(t.Context())
	want := containerConnection{ID: id, WaitFor: "postCreateCommand"}
	if err != nil || got != want || !ideStarted {
		t.Fatalf("ensure(publication queue full) = %+v, %v, IDE=%t, want %+v and a running IDE", got, err, ideStarted, want)
	}
	if candidate.ID != build || candidate.ConfigSHA256 != digest || candidate.Hit {
		t.Errorf("queued build = %+v, want immutable ID %q and exact config digest %q", candidate, build, digest)
	}
}

func builtImageWorkspaceFixture(t *testing.T, configPath string) (string, string) {
	t.Helper()
	workspace := filepath.Join(t.TempDir(), "project")
	config := filepath.Join(workspace, configPath)
	if err := os.MkdirAll(filepath.Dir(config), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config, []byte(builtImageConfigFixture), 0600); err != nil {
		t.Fatal(err)
	}
	return workspace, fmt.Sprintf("%x", sha256.Sum256([]byte(builtImageConfigFixture)))
}

func builtImageUpFixture(id, digest, build string, hit bool) []byte {
	return fmt.Appendf(nil, `{"outcome":"success","containerId":%q,"mergedConfiguration":{"waitFor":"postCreateCommand","remoteUser":"developer"},`+
		`"shiftboxImage":{"configSHA256":%q,"builtImageID":%q,"imageCacheHit":%t}}`, id, digest, build, hit)
}
