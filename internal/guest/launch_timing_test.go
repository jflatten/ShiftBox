package guest

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jflatten/ShiftBox/internal/launchlog"
)

// TestLaunchTimingRetries preserves the sample ID when retrying a temporarily unavailable controller.
func TestLaunchTimingRetries(t *testing.T) {
	var attempts atomic.Int32
	received := make(chan launchTimingReport, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var sample launchTimingReport
		if err := json.NewDecoder(r.Body).Decode(&sample); err != nil {
			t.Errorf("decode launch timing: %v", err)
		}
		received <- sample
		if attempts.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	want := launchTimingReport{OperationID: uuid.NewString(), DispatchVersion: 5,
		Sample: launchlog.GuestSample{ID: uuid.NewString(), Phase: "ide_start", Outcome: "succeeded", DurationMS: 17}}
	queue := make(chan launchTimingReport, 1)
	queue <- want
	close(queue)
	runLaunchTiming(ctx, server.Client(), server.URL, queue, time.Millisecond)
	if len(received) != 2 {
		t.Fatalf("received %d attempts, want 2", len(received))
	}
	for range 2 {
		if got := <-received; got != want {
			t.Errorf("report = %+v, want %+v", got, want)
		}
	}
}

// TestConfigureTimingUploadCompatibility keeps detailed samples compatible with older controllers.
func TestConfigureTimingUploadCompatibility(t *testing.T) {
	queue := make(chan launchTimingReport, 8)
	runtime := devContainer{launchTiming: queue}
	dispatch := guestDispatch{ID: uuid.NewString(), Version: 1}
	runtime.uploadLaunchTiming(t.Context(), dispatch, "initial_devcontainer_setup", time.Second, "succeeded")
	runtime.uploadLaunchTiming(t.Context(), dispatch, "ide_extensions", time.Second, "failed")
	if len(queue) != 1 || (<-queue).Sample.Phase != "devcontainer_up" {
		t.Fatal("old controller should receive only its supported setup sample")
	}
	runtime.detailedConfigureTiming = true
	runtime.uploadLaunchTiming(t.Context(), dispatch, "initial_devcontainer_setup", time.Second, "succeeded")
	runtime.uploadLaunchTiming(t.Context(), dispatch, "ide_extensions", time.Second, "failed")
	if len(queue) != 2 || (<-queue).Sample.Phase != "initial_devcontainer_setup" {
		t.Fatal("new controller should receive the distinct initial setup sample")
	}
	if sample := (<-queue).Sample; sample.Phase != "ide_extensions" || sample.Outcome != "failed" || sample.DurationMS != 1000 {
		t.Errorf("unexpected extension install sample: %+v", sample)
	}
}

// TestImagePreparationReportsResolutionAndFetch records bounded phases even when the pull fails.
func TestImagePreparationReportsResolutionAndFetch(t *testing.T) {
	var phases []string
	runtime := devContainer{workspace: t.TempDir(), config: "devcontainer.json",
		run: func(_ context.Context, executable string, args ...string) ([]byte, error) {
			if executable == devContainerCLI {
				return []byte(`{"configuration":{"image":"ubuntu:24.04"}}`), nil
			}
			return nil, errors.New("pull failed")
		},
		timing: func(phase string, _ time.Duration, outcome string) { phases = append(phases, phase+":"+outcome) },
	}
	if err := runtime.prepareImage(t.Context()); err == nil {
		t.Fatal("expected image pull failure")
	}
	if got := strings.Join(phases, ","); got != "image_configuration:succeeded,image_prefetch:failed" {
		t.Errorf("timing sequence = %q", got)
	}
}
