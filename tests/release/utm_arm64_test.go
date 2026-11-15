//go:build utm_release

package release_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/operation"
	"github.com/jflatten/ShiftBox/internal/provider"
	"github.com/jflatten/ShiftBox/internal/proxy"
	"github.com/jflatten/ShiftBox/internal/workspace"
)

const nativeARM64Repository = "https://github.com/example/platform-fixture"

// TestUTMARM64Workbenches boots and enrolls the ARM64 export, launches a
// native devcontainer, and verifies the offline workbench after a failed rebuild.
func TestUTMARM64Workbenches(t *testing.T) {
	if os.Getenv("SHIFTBOX_TEST_UTM_ARM64_WORKBENCHES") != "1" {
		t.Skip("set SHIFTBOX_TEST_UTM_ARM64_WORKBENCHES=1 for native ARM64 workbench validation")
	}
	runUTMGuestFixture(t, nil, nativeARM64Workbenches)
}

type arm64Workbenches struct {
	nativeSeedProvider
	prepared, native, breakConfig, broken, offline chan struct{}
	timings                                        chan string
}

// start seeds an isolated local checkout with the same receipt a successful
// checkout writes. The agent then runs its normal development and repair workflows.
func startARM64Workbenches(t *testing.T, host string, machines provider.Provider) *arm64Workbenches {
	t.Helper()
	listener, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
	if err != nil {
		t.Fatal(err)
	}
	id := uuid.NewString()
	path := "/" + id + "/"
	fixture := &arm64Workbenches{
		nativeSeedProvider: nativeSeedProvider{Provider: machines, seed: "http://" + listener.Addr().String() + path},
		prepared:           make(chan struct{}, 1), native: make(chan struct{}, 1), breakConfig: make(chan struct{}),
		broken: make(chan struct{}, 1), offline: make(chan struct{}, 1), timings: make(chan string, 1),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+path+"meta-data", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, "instance-id: %s\n", id)
	})
	mux.HandleFunc("GET "+path+"user-data", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, "#!/bin/sh\nset -eu\nbase='%s'\nrepository='%s'\n%s", fixture.seed, nativeARM64Repository, arm64WorkbenchesScript)
	})
	for endpoint, report := range map[string]chan struct{}{
		"prepared": fixture.prepared, "native": fixture.native, "broken": fixture.broken, "offline": fixture.offline,
	} {
		mux.HandleFunc("POST "+path+endpoint, func(w http.ResponseWriter, _ *http.Request) {
			select {
			case report <- struct{}{}:
			default:
			}
			w.WriteHeader(http.StatusNoContent)
		})
	}
	mux.HandleFunc("POST "+path+"timings", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		select {
		case fixture.timings <- string(body):
		default:
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET "+path+"break", func(w http.ResponseWriter, _ *http.Request) {
		select {
		case <-fixture.breakConfig:
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 3 * time.Second}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	t.Cleanup(func() {
		_ = server.Close()
		if err := <-done; !errors.Is(err, http.ErrServerClosed) {
			t.Errorf("ARM64 checkout fixture server: %v", err)
		}
	})
	return fixture
}

func (f *arm64Workbenches) Start(ctx context.Context, req provider.StartRequest) (provider.Inventory, error) {
	got, err := f.nativeSeedProvider.Start(ctx, req)
	if err != nil {
		return got, err
	}
	select {
	case <-f.prepared:
		return got, nil
	case <-ctx.Done():
		return got, fmt.Errorf("ARM64 guest did not prepare its test checkout: %w", ctx.Err())
	}
}

func (f *arm64Workbenches) verify(t *testing.T, ctx context.Context, db *sql.DB, journal *operation.Journal,
	ref provider.WorkspaceRef, owner, launch string, tunnels *proxy.Tunnels,
) {
	t.Helper()
	op := waitNativeOperation(t, ctx, journal, launch, "succeeded")
	for _, step := range op.Steps {
		var start, finish string
		if err := db.QueryRowContext(ctx, `SELECT started_at,finished_at FROM operation_steps
			WHERE operation_id=$1 AND step_key=$2`, op.ID, step.Key).Scan(&start, &finish); err != nil {
			t.Fatal(err)
		}
		started, startErr := time.Parse(time.RFC3339Nano, start)
		finished, finishErr := time.Parse(time.RFC3339Nano, finish)
		if err := errors.Join(startErr, finishErr); err != nil {
			t.Fatal(err)
		}
		t.Logf("launch %s took %s, complete at +%s", step.Key,
			finished.Sub(started).Round(time.Millisecond), finished.Sub(op.CreatedAt).Round(time.Millisecond))
	}
	select {
	case timing := <-f.timings:
		t.Logf("guest configuration timings (phase, milliseconds, outcome):\n%s", timing)
		for _, phase := range []string{"repository_preparation", "devcontainer_up", "docker_container_ready", "retained_home",
			"project_permissions", "required_hooks", "ide_install", "ide_start", "remaining_hooks"} {
			if !strings.Contains("\n"+timing, "\n"+phase+" ") {
				t.Errorf("guest timing report omitted %s", phase)
			}
		}
		if strings.Contains(timing, "docker_image_pull ") {
			t.Error("native launch pulled its already-preloaded base image")
		}
	case <-time.After(15 * time.Second):
		t.Fatal("guest did not return measured Dev Container phases after launch")
	case <-ctx.Done():
		t.Fatalf("guest did not return measured Dev Container phases: %v", ctx.Err())
	}
	waitARM64Evidence(t, ctx, f.native, "the devcontainer reported aarch64")
	host := ref.WorkspaceID + ".ide.test"
	if err := tunnels.ProbeIDE(ctx, ref.WorkspaceID, ref.Generation, host); err != nil {
		t.Fatalf("native devcontainer workbench: %v", err)
	}

	close(f.breakConfig)
	waitARM64Evidence(t, ctx, f.broken, "the deliberately invalid configuration was written")
	rebuild := admitARM64WorkbenchesAction(t, ctx, db, journal, ref.WorkspaceID, owner, "rebuild")
	waitNativeOperation(t, ctx, journal, rebuild, "failed")
	repair := admitARM64WorkbenchesAction(t, ctx, db, journal, ref.WorkspaceID, owner, "repair")
	waitNativeOperation(t, ctx, journal, repair, "succeeded")
	waitARM64Evidence(t, ctx, f.offline, "the repair container used the preloaded ARM64 image with no network and a read-only root")
	if err := tunnels.ProbeRepair(ctx, ref.WorkspaceID, ref.Generation, host); err != nil {
		t.Fatalf("offline repair workbench: %v", err)
	}
}

func admitARM64WorkbenchesAction(t *testing.T, ctx context.Context, db *sql.DB, journal *operation.Journal,
	workspaceID, owner, action string,
) string {
	t.Helper()
	var op operation.Operation
	if err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
		var version int64
		if err := tx.QueryRowContext(ctx, "SELECT version FROM workspaces WHERE id=$1", workspaceID).Scan(&version); err != nil {
			return err
		}
		var err error
		op, err = workspace.AdmitPower(ctx, tx, journal, workspace.PowerIntent{
			WorkspaceID: workspaceID, ActorID: owner, Version: version, RequestKey: "arm64-workbench-" + action, Action: action,
		}, time.Now())
		return err
	}); err != nil {
		t.Fatalf("admit %s on the ARM64 workspace: %v", action, err)
	}
	return op.ID
}

func waitARM64Evidence(t *testing.T, ctx context.Context, evidence <-chan struct{}, detail string) {
	t.Helper()
	select {
	case <-evidence:
		t.Log(detail)
	case <-ctx.Done():
		t.Fatalf("missing ARM64 evidence (%s): %v", detail, ctx.Err())
	}
}

const arm64WorkbenchesScript = `
report() {
    curl -fsS --retry 3 --max-time 5 -X POST "$base$1"
}

test "$(uname -m)" = aarch64
project=/var/lib/shiftbox-agent/project
config=.devcontainer/devcontainer.json
install -d -m 0700 "$project"
cd "$project"
mkdir -p .devcontainer
cat > "$config" <<'JSON'
{
  "image": "mcr.microsoft.com/devcontainers/base:ubuntu-24.04",
  "waitFor": "postCreateCommand",
  "postCreateCommand": "sleep 5 && uname -m > .fixture-architecture && grep -qx aarch64 .fixture-architecture"
}
JSON
git init --quiet
git add "$config"
git -c user.name=Fixture -c user.email=fixture@example.test commit -qm 'ARM64 fixture'
printf '{"url":"%s","ref":"","config":"%s","path":"%s","commit":"%s"}\n' \
    "$repository" "$config" "$project" "$(git rev-parse HEAD)" > .git/shiftbox-checkout.json
report prepared

until docker info >/dev/null 2>&1; do sleep 1; done
base_image='mcr.microsoft.com/devcontainers/base:ubuntu-24.04'
pinned_image="$base_image@sha256:309df9dde7aeb9e734a8f7ead76ab4d18ee4eb17b6047d2a2d008fa52fdea67b"
test "$(docker image inspect --format '{{.Os}}/{{.Architecture}}' "$base_image")" = linux/arm64
test "$(docker image inspect --format '{{.Id}}' "$base_image")" = "$(docker image inspect --format '{{.Id}}' "$pinned_image")"

until grep -qx aarch64 .fixture-architecture 2>/dev/null; do sleep 1; done
report native
timings=$(mktemp)
until grep -q '^remaining_hooks ' "$timings"; do
    journalctl -u shiftbox-agent.service -o cat --no-pager |
        sed -n 's/.*Dev Container phase timing.* phase=\([a-z_]*\) duration_ms=\([0-9]*\) outcome=\([a-z]*\)$/\1 \2 \3/p' > "$timings"
    sleep 1
done
curl -fsS --retry 3 --max-time 5 --data-binary @"$timings" "${base}timings"
rm -f "$timings"
until curl -fsS --max-time 3 "${base}break" 2>/dev/null; do sleep 1; done
printf '{\n' > "$config"
report broken

expected="$(cat /usr/local/share/shiftbox/repair-image) none true true"
until test "$(docker container inspect --format '{{.Image}} {{.HostConfig.NetworkMode}} {{.HostConfig.ReadonlyRootfs}} {{.State.Running}}' shiftbox-repair 2>/dev/null)" = "$expected"; do
    sleep 1
done
test "$(docker exec shiftbox-repair uname -m)" = aarch64
report offline
`
