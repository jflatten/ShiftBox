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
			if len(services) != 2 || services[0].Key != "ide" || services[0].Target != "/run/shiftbox-ide/code-server.sock" ||
				services[1].Key != "repair" || services[1].Target != "/run/shiftbox-repair/repair.sock" {
				t.Error("supervisor did not advertise distinct development and repair IDE sockets")
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

// TestAgentAdvertisesConfiguredPreview verifies that a controller-selected
// port gets one named loopback service and that malformed lists fail closed.
func TestAgentAdvertisesConfiguredPreview(t *testing.T) {
	headers := http.Header{TunnelAddressHeader: {"internal.test:9443"}, PreviewPortsHeader: {"[8080]"}}
	offer, err := readTunnelOffer("https://internal.test:8443"+pollPath, headers, 1, "running")
	if err != nil {
		t.Fatal(err)
	}
	advertised := make(chan []proxy.TunnelService, 1)
	supervisor := agentTunnel{retry: time.Second, serve: func(ctx context.Context, _ string, _ *tls.Config, _ string, _ int64, services []proxy.TunnelService) error {
		advertised <- services
		<-ctx.Done()
		return ctx.Err()
	}}
	defer supervisor.close()
	supervisor.update(t.Context(), offer)
	select {
	case services := <-advertised:
		if len(services) != 3 || services[2].Key != "preview-8080" || services[2].Target != "127.0.0.1:8080" {
			t.Fatalf("tunnel services = %+v, want IDE, repair, and configured loopback preview", services)
		}
	case <-time.After(time.Second):
		t.Fatal("configured workspace did not register a preview service")
	}
	headers.Set(PreviewPortsHeader, "[0]")
	if _, err := readTunnelOffer("https://internal.test:8443"+pollPath, headers, 1, "running"); err == nil {
		t.Fatal("accepted an invalid preview port")
	}
}
