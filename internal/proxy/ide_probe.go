package proxy

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const maxIDEProbeDocument = 512 << 10

// ProbeIDE verifies the workbench and WebSocket on the tunnel used by browsers.
// It checks the workspace tunnel independently of waitFor.
func (t *Tunnels) ProbeIDE(ctx context.Context, workspace string, generation int64, host string) error {
	return t.probeWorkbench(ctx, workspace, generation, host, "ide")
}

// ProbeRepair checks the independently registered repair workbench service.
func (t *Tunnels) ProbeRepair(ctx context.Context, workspace string, generation int64, host string) error {
	return t.probeWorkbench(ctx, workspace, generation, host, "repair")
}

func (t *Tunnels) probeWorkbench(ctx context.Context, workspace string, generation int64, host, service string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+tunnelHTTPHost+"/", nil)
	if err != nil {
		return fmt.Errorf("create IDE probe request: %w", err)
	}
	req.Host = host
	transport := tunnelTransport(t.Open, workspace, generation, StreamHeader{
		ServiceKey: service, RequestID: "ide-readiness", Protocol: "http",
	})
	defer transport.CloseIdleConnections()
	response, err := transport.RoundTrip(req)
	if err != nil {
		return fmt.Errorf("request IDE workbench: %w", err)
	}
	if response.StatusCode == http.StatusFound {
		location := response.Header.Get("Location")
		response.Body.Close()
		target, err := url.Parse(location)
		if err != nil || target.Host != "" || target.Scheme != "" || target.Fragment != "" ||
			(target.Path != "/" && target.Path != "./" && target.Path != "") ||
			!strings.HasPrefix(target.Query().Get("folder"), "/") {
			return errors.New("guest IDE returned an unexpected redirect")
		}
		req = req.Clone(ctx)
		req.URL.RawQuery = url.Values{"folder": {target.Query().Get("folder")}}.Encode()
		response, err = transport.RoundTrip(req)
		if err != nil {
			return fmt.Errorf("request IDE project workbench: %w", err)
		}
	}
	defer response.Body.Close()
	kind, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if response.StatusCode != http.StatusOK || err != nil || kind != "text/html" {
		return errors.New("guest proxy did not return a successful HTML response")
	}
	document, err := io.ReadAll(io.LimitReader(response.Body, maxIDEProbeDocument+1))
	if err != nil {
		return fmt.Errorf("read IDE workbench: %w", err)
	}
	if len(document) > maxIDEProbeDocument ||
		!bytes.Contains(document, []byte(`id="vscode-workbench-web-configuration"`)) {
		return errors.New("guest proxy did not return a VS Code workbench document")
	}
	response.Body.Close()
	return t.probeWorkbenchWebSocket(ctx, workspace, generation, host, service)
}

// probeWorkbenchWebSocket warms the first upgrade before Open IDE becomes
// available. Serving HTML does not establish that the socket backend is ready.
func (t *Tunnels) probeWorkbenchWebSocket(ctx context.Context, workspace string, generation int64, host, service string) error {
	target := "http://" + tunnelHTTPHost + "/?" + url.Values{
		"reconnectionToken": {rand.Text()}, "reconnection": {"false"}, "skipWebSocketFrames": {"false"},
	}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return fmt.Errorf("create IDE WebSocket probe: %w", err)
	}
	keyBytes := make([]byte, 16)
	if _, err := rand.Read(keyBytes); err != nil {
		return fmt.Errorf("create IDE WebSocket challenge: %w", err)
	}
	key := base64.StdEncoding.EncodeToString(keyBytes)
	req.Host = host
	req.Header.Set("Origin", "https://"+host)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Sec-WebSocket-Version", "13")
	req.Header.Set("Sec-WebSocket-Key", key)
	transport := tunnelTransport(t.Open, workspace, generation, StreamHeader{
		ServiceKey: service, RequestID: "ide-readiness-websocket", Protocol: "websocket",
	})
	defer transport.CloseIdleConnections()
	response, err := transport.RoundTrip(req)
	if err != nil {
		return fmt.Errorf("request IDE WebSocket: %w", err)
	}
	defer response.Body.Close()
	// RFC 6455 requires SHA-1 here to verify that the peer accepted this challenge.
	accept := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	if response.StatusCode != http.StatusSwitchingProtocols || !hasConnectionUpgrade(response.Header) ||
		!strings.EqualFold(response.Header.Get("Upgrade"), "websocket") ||
		response.Header.Get("Sec-WebSocket-Accept") != base64.StdEncoding.EncodeToString(accept[:]) {
		return errors.New("guest IDE WebSocket is not ready")
	}
	return nil
}
