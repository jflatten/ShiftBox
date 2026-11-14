package guest

import (
	"context"
	"crypto/tls"
	"net/http"
	"testing"
	"time"

	"github.com/jflatten/ShiftBox/internal/proxy"
)

// TestAgentTunnelReconnectAndStop verifies that a dropped tunnel retries and
// that a stopped workspace cancels the connection before it can retry again.
func TestAgentTunnelReconnectAndStop(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	started := make(chan tunnelOffer, 3)
	dropped := make(chan struct{}, 1)
	supervisor := agentTunnel{
		retry: time.Millisecond,
		serve: func(ctx context.Context, address string, _ *tls.Config, _ string, generation int64, services []proxy.TunnelService) error {
			if len(services) != 1 || services[0].Key != "ide" || services[0].Target != "/run/shiftbox-ide/code-server.sock" {
				t.Error("supervisor did not advertise the private browser IDE socket")
			}
			started <- tunnelOffer{address: address, generation: generation}
			select {
			case <-dropped:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
	}
	defer supervisor.close()
	headers := http.Header{TunnelAddressHeader: {"internal.test:9443"}}
	offer, err := readTunnelOffer("https://internal.test:8443"+pollPath, headers, 4, "running")
	if err != nil {
		t.Fatal(err)
	}
	for i := range 2 {
		supervisor.update(ctx, offer)
		select {
		case got := <-started:
			if got != offer {
				t.Errorf("tunnel dial %d = %+v, want %+v", i, got, offer)
			}
		case <-ctx.Done():
			t.Fatal("tunnel did not connect")
		}
		if i == 0 {
			dropped <- struct{}{}
		}
	}
	stopped, err := readTunnelOffer("https://internal.test:8443"+pollPath, headers, 4, "stopped")
	if err != nil {
		t.Fatal(err)
	}
	supervisor.update(ctx, stopped)
	if supervisor.done != nil || stopped != (tunnelOffer{}) {
		t.Error("stopped workspace still has a tunnel supervisor")
	}
}

// TestAgentTunnelRejectsForeignEndpoint keeps the agent's client identity
// bound to the controller hostname used to authenticate the poll.
func TestAgentTunnelRejectsForeignEndpoint(t *testing.T) {
	for _, address := range []string{"other.test:9443", "internal.test:0", "internal.test:+9443", "internal.test:65536"} {
		headers := http.Header{TunnelAddressHeader: {address}}
		if _, err := readTunnelOffer("https://internal.test:8443"+pollPath, headers, 4, "running"); err == nil {
			t.Errorf("readTunnelOffer(%q) = nil, want invalid endpoint", address)
		}
	}
}
