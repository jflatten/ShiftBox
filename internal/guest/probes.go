package guest

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/jflatten/ShiftBox/internal/workspace"
)

// ProbeReportsHeader advertises support for probe results on the next poll.
const ProbeReportsHeader = "Shiftbox-Probe-Reports"

type probeReporter struct {
	socket  string
	enabled bool
}

func probeCodeServer(ctx context.Context, socket string) string {
	dialer := net.Dialer{}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, "unix", socket)
		},
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport:     transport,
		Timeout:       2 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://localhost/healthz", nil)
	if err != nil {
		return workspace.ProbeError
	}
	response, err := client.Do(request)
	if err != nil {
		return workspace.ProbeError
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return workspace.ProbeError
	}
	var health struct {
		Status string `json:"status"`
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, 1024))
	if err != nil || len(raw) == 1024 || json.Unmarshal(raw, &health) != nil {
		return workspace.ProbeError
	}
	// Code-server's expired heartbeat indicates an idle editor that still serves requests.
	if health.Status == "alive" || health.Status == "expired" {
		return workspace.ProbeHealthy
	}
	return workspace.ProbeError
}
