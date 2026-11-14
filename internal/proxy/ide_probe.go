package proxy

import (
	"bytes"
	"context"
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

// ProbeIDE requests the workbench on the same tunnel service used by browsers.
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
	return nil
}
