package guest

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestGuestPollPreparesRepository verifies the production poll path runs
// repository preparation before any Dev Container automation.
func TestGuestPollPreparesRepository(t *testing.T) {
	state := t.TempDir()
	project := filepath.Join(state, "project")
	journal, err := loadLocalJournal(filepath.Join(state, "operation.json"))
	if err != nil {
		t.Fatal(err)
	}
	var gitCalls int
	runtime := devContainer{
		workspace: project,
		repository: &repositoryPreparation{
			project: project,
			token:   func(context.Context) (string, error) { return "fixture-owner-token", nil },
			run: func(_ context.Context, dir, _ string, args ...string) ([]byte, error) {
				gitCalls++
				switch args[0] {
				case "clone":
					return nil, os.Mkdir(filepath.Join(dir, ".git"), 0700)
				case "reset":
					return nil, nil
				case "rev-parse":
					return []byte(strings.Repeat("b", 40)), nil
				default:
					t.Fatalf("unexpected Git command %q", args)
					return nil, nil
				}
			},
		},
		run: func(_ context.Context, executable string, _ ...string) ([]byte, error) {
			if _, err := os.Stat(repositoryReceipt(project)); err != nil {
				t.Fatalf("Dev Container setup started before repository receipt was committed: %v", err)
			}
			if executable == "/usr/bin/docker" {
				return []byte(strconv.Itoa(os.Getuid()) + " " + strconv.Itoa(os.Getgid())), nil
			}
			return []byte(`{"outcome":"success","containerId":"` + strings.Repeat("1", 64) + `","mergedConfiguration":{}}`), nil
		},
	}
	const dispatch = `{"id":"0555292b-088e-4f5b-8ac6-8be6045ae630","version":7,"type":"provision",
		"command":"ensure-development-container","arguments":{"repositoryUrl":"https://github.com/example/repo.git",
		"devcontainerPath":"devcontainer.json","retainedMounts":[]}}`
	if err := applyGuestPoll(t.Context(), journal, runtime, 4, "running", json.RawMessage(dispatch)); err != nil {
		t.Fatal(err)
	}
	if report := journal.report(); gitCalls != 3 || !report.Evidence.WaitForReached {
		t.Errorf("repository Git calls = %d, report = %+v, want 3 Git calls followed by waitFor", gitCalls, report)
	}
}

// TestGuestPollStopsAtRepositoryFailure preserves a bounded report and never
// executes project automation when Git authorization or checkout fails.
func TestGuestPollStopsAtRepositoryFailure(t *testing.T) {
	state := t.TempDir()
	journal, err := loadLocalJournal(filepath.Join(state, "operation.json"))
	if err != nil {
		t.Fatal(err)
	}
	dispatch := ensureDispatch()
	dispatch.Repository = "https://github.com/example/repo.git"
	dispatch.Config = "devcontainer.json"
	runtime := devContainer{
		repository: &repositoryPreparation{
			project: filepath.Join(state, "project"),
			token: func(context.Context) (string, error) {
				return "", errors.New("credential provider printed fixture-secret-value")
			},
		},
		run: func(context.Context, string, ...string) ([]byte, error) {
			t.Fatal("failed repository preparation started project automation")
			return nil, nil
		},
	}
	report, err := journal.ensureDevelopment(t.Context(), dispatch, runtime)
	if err != nil || report.Status != "failed" || report.Problem != "guest.repository_preparation_failed" ||
		report.Evidence.WaitForReached || !validConfigurationReport(report) {
		t.Errorf("ensureDevelopment(repository failure) = (%+v, %v), want a bounded repository failure", report, err)
	}
}

// TestGuestGitAccessToken reads an owner token over the controller HTTPS
// client and rejects responses containing additional credentials.
func TestGuestGitAccessToken(t *testing.T) {
	path := "/internal/v1/guest/git/github:access-token"
	responses := make(chan string, 2)
	responses <- `{"accessToken":"fixture-owner-token"}`
	responses <- `{"accessToken":"fixture-owner-token","refreshToken":"fixture-refresh-token"}`
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != path {
			t.Error("unexpected Git access token request")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(<-responses))
	}))
	defer server.Close()
	endpoint := server.URL + pollPath
	if got, err := guestGitAccessToken(t.Context(), server.Client(), endpoint); err != nil || got != "fixture-owner-token" {
		t.Errorf("guestGitAccessToken(valid response) = (%q, %v), want the current owner token", got, err)
	}
	if got, err := guestGitAccessToken(t.Context(), server.Client(), endpoint); err == nil || got != "" {
		t.Errorf("guestGitAccessToken(refresh token present) = (%q, %v), want denial with no token", got, err)
	}
}

// TestPrepareRepository checks out real Git branches, tags, and commits and
// verifies that redelivery preserves subsequent developer changes.
func TestPrepareRepository(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	source := t.TempDir()
	gitAt := func(t *testing.T, dir string, args ...string) string {
		t.Helper()
		result, err := exec.CommandContext(t.Context(), git, append([]string{"-C", dir}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v = %q, %v, want success", args, result, err)
		}
		return strings.TrimSpace(string(result))
	}
	gitAt(t, source, "init", "--initial-branch=main")
	gitAt(t, source, "config", "user.email", "fixture@example.test")
	gitAt(t, source, "config", "user.name", "ShiftBox fixture")
	writeFile := func(t *testing.T, path, content string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, filepath.Join(source, "devcontainer.json"), "{}")
	writeFile(t, filepath.Join(source, "project.txt"), "initial")
	gitAt(t, source, "add", ".")
	gitAt(t, source, "-c", "commit.gpgsign=false", "commit", "-m", "initial")
	initial := gitAt(t, source, "rev-parse", "HEAD")
	gitAt(t, source, "-c", "tag.gpgSign=false", "tag", "v1")
	gitAt(t, source, "switch", "-c", "feature/nested")
	writeFile(t, filepath.Join(source, "project.txt"), "feature")
	gitAt(t, source, "-c", "commit.gpgsign=false", "commit", "-am", "feature")
	feature := gitAt(t, source, "rev-parse", "HEAD")
	gitAt(t, source, "switch", "main")

	for _, test := range []struct{ name, ref, commit, branch string }{
		{"default branch", "", initial, "main"},
		{"named branch", "feature/nested", feature, "feature/nested"},
		{"explicit branch", "refs/heads/feature/nested", feature, "feature/nested"},
		{"tag", "v1", initial, "HEAD"},
		{"explicit tag", "refs/tags/v1", initial, "HEAD"},
		{"commit", initial, initial, "HEAD"},
	} {
		t.Run(test.name, func(t *testing.T) {
			project := filepath.Join(t.TempDir(), "project")
			var tokenCalls int
			repo := repositoryPreparation{
				project: project,
				token: func(context.Context) (string, error) {
					tokenCalls++
					return "fixture-access-token", nil
				},
				run: func(ctx context.Context, dir, token string, args ...string) ([]byte, error) {
					if len(args) > 0 && args[0] == "clone" {
						if token != "fixture-access-token" || args[len(args)-2] != "https://github.com/example/repo.git" {
							t.Fatal("clone did not use the approved repository and current owner token")
						}
						args[len(args)-2] = source
					}
					return exec.CommandContext(ctx, git, append([]string{"-C", dir}, args...)...).Output()
				},
			}
			input := repositorySource{URL: "https://github.com/example/repo.git", Ref: test.ref, Config: "devcontainer.json"}
			evidence, err := repo.prepare(t.Context(), input)
			if err != nil || evidence.Commit != test.commit || evidence.Path != project {
				t.Fatalf("prepare(%q) = (%+v, %v), want commit %s in %s", test.ref, evidence, err, test.commit, project)
			}
			if branch := gitAt(t, project, "rev-parse", "--abbrev-ref", "HEAD"); branch != test.branch {
				t.Errorf("checkout(%q) on %q, want %q", test.ref, branch, test.branch)
			}
			writeFile(t, filepath.Join(project, "project.txt"), "uncommitted work")
			replayed, err := repo.prepare(t.Context(), input)
			if err != nil || replayed != evidence || tokenCalls != 1 {
				t.Errorf("prepare(replay) = (%+v, %v), token calls %d, want (%+v, nil) and 1 call", replayed, err, tokenCalls, evidence)
			}
			content, err := os.ReadFile(filepath.Join(project, "project.txt"))
			if err != nil || string(content) != "uncommitted work" {
				t.Errorf("project.txt after replay = %q, %v, want uncommitted work", content, err)
			}
		})
	}
}

// TestPrepareRepositoryRejectsUnknownTree verifies a retained project cannot
// be silently replaced and Git failures do not publish a partial worktree.
func TestPrepareRepositoryRejectsUnknownTree(t *testing.T) {
	parent := t.TempDir()
	project := filepath.Join(parent, "project")
	repo := repositoryPreparation{
		project: project,
		token:   func(context.Context) (string, error) { return "fixture-secret-value", nil },
		run: func(context.Context, string, string, ...string) ([]byte, error) {
			return nil, errors.New("git emitted fixture-secret-value")
		},
	}
	input := repositorySource{URL: "https://github.com/example/repo.git", Ref: "main", Config: "devcontainer.json"}
	if _, err := repo.prepare(t.Context(), input); err == nil || strings.Contains(err.Error(), "fixture-secret-value") {
		t.Errorf("prepare(failed clone) = %v, want a redacted error", err)
	}
	if entries, err := os.ReadDir(parent); err != nil || len(entries) != 0 {
		t.Fatalf("failed clone left %v, %v, want empty staging directory", entries, err)
	}
	if err := os.Mkdir(project, 0700); err != nil {
		t.Fatal(err)
	}
	repo.token = func(context.Context) (string, error) {
		t.Fatal("prepare attempted authentication before reconciling the retained tree")
		return "", nil
	}
	if _, err := repo.prepare(t.Context(), input); err == nil {
		t.Error("prepare(unknown retained tree) = nil, want refusal")
	}
}
