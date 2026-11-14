package guest

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jflatten/ShiftBox/internal/proxy"
	"github.com/jflatten/ShiftBox/internal/workspace"
)

// TunnelAddressHeader advertises the dedicated tunnel address on authenticated polls.
const TunnelAddressHeader = "Shiftbox-Tunnel-Address"

// PreviewPortsHeader carries the controller-authorized JSON array of local ports.
const PreviewPortsHeader = "Shiftbox-Preview-Ports"

type tunnelOffer struct {
	address    string
	generation int64
	previews   string
}

func readTunnelOffer(endpoint string, headers http.Header, generation int64, desired string) (tunnelOffer, error) {
	values := headers.Values(TunnelAddressHeader)
	if desired != "running" || len(values) == 0 {
		return tunnelOffer{}, nil
	}
	if len(values) != 1 {
		return tunnelOffer{}, errors.New("ambiguous guest tunnel address")
	}
	host, port, err := net.SplitHostPort(values[0])
	poll, pollErr := url.Parse(endpoint)
	number, portErr := strconv.ParseUint(port, 10, 16)
	if err != nil || pollErr != nil || portErr != nil || number == 0 ||
		strconv.FormatUint(number, 10) != port || !strings.EqualFold(host, poll.Hostname()) {
		return tunnelOffer{}, errors.New("invalid controller guest tunnel address")
	}
	previews := headers.Values(PreviewPortsHeader)
	if len(previews) > 1 {
		return tunnelOffer{}, errors.New("ambiguous guest preview ports")
	}
	offer := tunnelOffer{address: net.JoinHostPort(poll.Hostname(), port), generation: generation}
	if len(previews) == 1 && previews[0] != "[]" && previews[0] != "" {
		if _, err := workspace.ParsePreviewPorts(previews[0]); err != nil {
			return tunnelOffer{}, err
		}
		offer.previews = previews[0]
	}
	return offer, nil
}

// agentTunnel is owned by the poll loop. Its worker retries independently
// of project setup and is joined before changing generation or stopping.
type agentTunnel struct {
	offer    tunnelOffer
	protocol string
	tls      *tls.Config
	retry    time.Duration
	serve    func(context.Context, string, *tls.Config, string, int64, []proxy.TunnelService) error
	cancel   context.CancelFunc
	done     chan struct{}
}

func (t *agentTunnel) update(ctx context.Context, offer tunnelOffer) {
	if offer == t.offer {
		return
	}
	t.close()
	if offer.address == "" {
		return
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	t.offer, t.cancel, t.done = offer, cancel, done
	go func() {
		defer close(done)
		services := []proxy.TunnelService{
			{Key: "ide", Target: proxy.IDESocket, Protocols: []string{"http", "websocket"}},
			{Key: "repair", Target: proxy.RepairSocket, Protocols: []string{"http", "websocket"}},
		}
		if offer.previews != "" {
			ports, _ := workspace.ParsePreviewPorts(offer.previews) // The poll decoder already validated this value.
			for _, port := range ports {
				text := strconv.Itoa(port)
				services = append(services, proxy.TunnelService{Key: "preview-" + text,
					Target: net.JoinHostPort("127.0.0.1", text), Protocols: []string{"http", "websocket"}})
			}
		}
		for runCtx.Err() == nil {
			_ = t.serve(runCtx, offer.address, t.tls, t.protocol, offer.generation, services)
			timer := time.NewTimer(t.retry)
			select {
			case <-timer.C:
			case <-runCtx.Done():
				timer.Stop()
				return
			}
		}
	}()
}

func (t *agentTunnel) close() {
	if t.done != nil {
		t.cancel()
		<-t.done
		t.done = nil
	}
	t.offer = tunnelOffer{}
}
