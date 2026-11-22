package guest

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jflatten/ShiftBox/internal/workspace"
)

// TestCodeServerProbe distinguishes a serving editor from a missing or failing service.
func TestCodeServerProbe(t *testing.T) {
	for _, tc := range []struct {
		name string
		code int
		body string
		want string
	}{
		{name: "active editor", code: 200, body: `{"status":"alive","lastHeartbeat":123}`, want: workspace.ProbeHealthy},
		{name: "idle editor", code: 200, body: `{"status":"expired","lastHeartbeat":0}`, want: workspace.ProbeHealthy},
		{name: "missing socket", want: workspace.ProbeError},
		{name: "server error", code: 503, body: `{"status":"alive"}`, want: workspace.ProbeError},
		{name: "redirect", code: 302, want: workspace.ProbeError},
		{name: "invalid response", code: 200, body: `{"status":"starting"}`, want: workspace.ProbeError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Use a short directory to stay within the Unix socket address limit.
			dir, err := os.MkdirTemp("/tmp", "sb-probe-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := os.RemoveAll(dir); err != nil {
					t.Error(err)
				}
			})
			socket := filepath.Join(dir, "ide.sock")
			if tc.code != 0 {
				listener, err := net.Listen("unix", socket)
				if err != nil {
					t.Fatal(err)
				}
				server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method != http.MethodGet || r.URL.Path != "/healthz" {
						t.Errorf("unexpected probe request: %s %s", r.Method, r.URL.Path)
					}
					if tc.code == 302 {
						w.Header().Set("Location", "/")
					}
					w.WriteHeader(tc.code)
					if _, err := fmt.Fprint(w, tc.body); err != nil {
						t.Error(err)
					}
				})}
				done := make(chan error, 1)
				go func() { done <- server.Serve(listener) }()
				t.Cleanup(func() {
					if err := server.Close(); err != nil {
						t.Error(err)
					}
					if err := <-done; err != http.ErrServerClosed {
						t.Error(err)
					}
				})
			}
			if got := probeCodeServer(t.Context(), socket); got != tc.want {
				t.Errorf("probeCodeServer() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestGuestProbePollNegotiation checks that old controllers keep receiving
// the original poll body and that probes cease after a stop response.
func TestGuestProbePollNegotiation(t *testing.T) {
	scenarios := []struct {
		advertise bool
		desired   string
		wantProbe bool
	}{
		{desired: "running"},
		{advertise: true, desired: "running"},
		{advertise: true, desired: "stopped", wantProbe: true},
		{advertise: true, desired: "stopped"},
	}
	reporter := &probeReporter{socket: filepath.Join(t.TempDir(), "absent.sock")}
	input := newGuestPollRequest("1", "test", nil)
	for index, tc := range scenarios {
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var input guestPollRequest
			err := json.NewDecoder(r.Body).Decode(&input)
			probes := input.Probes
			if err != nil || (!tc.wantProbe && probes != nil) ||
				(tc.wantProbe && (len(probes) != 1 || probes[0].ID != workspace.ProbeCodeServer || probes[0].Status != workspace.ProbeError)) {
				t.Errorf("poll %d sent probes %+v (%v), want code-server error report present = %t", index+1, probes, err, tc.wantProbe)
			}
			if tc.advertise {
				w.Header().Set(ProbeReportsHeader, "1")
			}
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(map[string]any{
				"protocolVersion": "1", "workspaceGeneration": 1, "desiredState": tc.desired,
				"serverTime": time.Now(), "certificate": map[string]time.Time{"renewAfter": time.Now().Add(time.Hour)},
			}); err != nil {
				t.Error(err)
			}
		}))
		_, err := pollAgent(t.Context(), server.Client(), server.URL+pollPath, input, nil, devContainer{}, nil, reporter)
		server.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
}
