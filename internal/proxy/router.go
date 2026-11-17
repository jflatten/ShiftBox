package proxy

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"net/http/httputil"
	"strings"
	"time"
)

type openTunnelStream func(context.Context, string, int64, StreamHeader) (net.Conn, error)

const tunnelHTTPHost = "guest.invalid"

// Forward serves one already-authorized request through the named guest service.
// The caller obtains workspace, generation and service from the browser grant.
func (t *Tunnels) Forward(w http.ResponseWriter, r *http.Request, workspace string, generation int64, service, requestID string) {
	tunnelProxy(t.Open, workspace, generation, service, requestID).ServeHTTP(w, r)
}

func tunnelProxy(open openTunnelStream, workspace string, generation int64, service, requestID string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		protocol := "http"
		if r.Method == http.MethodConnect || (r.URL.IsAbs() && r.URL.Host != r.Host) {
			http.Error(w, "Unsupported service request", http.StatusBadRequest)
			return
		}
		if upgrades := r.Header.Values("Upgrade"); len(upgrades) > 0 {
			if len(upgrades) != 1 || !strings.EqualFold(upgrades[0], "websocket") ||
				r.Method != http.MethodGet || !hasConnectionUpgrade(r.Header) {
				http.Error(w, "Unsupported service upgrade", http.StatusBadRequest)
				return
			}
			if origins := r.Header.Values("Origin"); r.TLS == nil || len(origins) != 1 || origins[0] != "https://"+r.Host {
				http.Error(w, "Service origin denied", http.StatusForbidden)
				return
			}
			protocol = "websocket"
		}
		header := StreamHeader{ServiceKey: service, RequestID: requestID, Protocol: protocol}
		workbench := (service == "ide" || service == "repair") && protocol == "http" && r.Method == http.MethodGet && r.URL.Path == "/"
		transport := tunnelTransport(open, workspace, generation, header)
		defer transport.CloseIdleConnections()
		forward := httputil.ReverseProxy{
			Transport: transport,
			Rewrite: func(req *httputil.ProxyRequest) {
				Sanitize(req.Out)
				if workbench {
					req.Out.Header.Set("Accept-Encoding", "identity")
					for _, name := range []string{"If-None-Match", "If-Modified-Since", "Range"} {
						req.Out.Header.Del(name)
					}
				}
				req.Out.Trailer = nil
				req.Out.URL.Scheme = "http"
				req.Out.URL.Host = tunnelHTTPHost
				req.Out.URL.User = nil
				if protocol == "websocket" {
					req.Out.Header.Set("Connection", "Upgrade")
					req.Out.Header.Set("Upgrade", "websocket")
				}
			},
			ModifyResponse: func(response *http.Response) error {
				if err := sanitizeServiceResponse(response); err != nil {
					return err
				}
				if workbench {
					return instrumentWorkbench(response)
				}
				return nil
			},
			ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
				http.Error(w, "Workspace service unavailable", http.StatusBadGateway)
			},
		}
		if protocol == "websocket" {
			upgrade := &cancelableUpgrade{ResponseWriter: w, ctx: r.Context()}
			defer func() {
				if upgrade.stop != nil {
					upgrade.stop()
				}
			}()
			w = upgrade
		}
		forward.ServeHTTP(w, r)
	})
}

// cancelableUpgrade closes the browser socket when authority for a hijacked
// connection ends. A normal HTTP write deadline cannot reach that socket.
type cancelableUpgrade struct {
	http.ResponseWriter
	ctx  context.Context
	stop func() bool
}

// Unwrap lets the HTTP response controller reach the underlying writer.
func (w *cancelableUpgrade) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// Hijack ties an upgraded connection to request cancellation so revocation can close it.
func (w *cancelableUpgrade) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, rw, err := http.NewResponseController(w.ResponseWriter).Hijack()
	if err == nil {
		w.stop = context.AfterFunc(w.ctx, func() { _ = conn.Close() })
	}
	return conn, rw, err
}

func tunnelTransport(open openTunnelStream, workspace string, generation int64, header StreamHeader) *http.Transport {
	return &http.Transport{
		DisableKeepAlives:      true,
		DisableCompression:     true,
		ResponseHeaderTimeout:  30 * time.Second,
		MaxResponseHeaderBytes: 64 << 10,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return open(ctx, workspace, generation, header)
		},
	}
}

func hasConnectionUpgrade(headers http.Header) bool {
	for _, value := range headers.Values("Connection") {
		for token := range strings.SplitSeq(value, ",") {
			if strings.EqualFold(strings.TrimSpace(token), "upgrade") {
				return true
			}
		}
	}
	return false
}

func sanitizeServiceResponse(response *http.Response) error {
	values := response.Header.Values("Set-Cookie")
	response.Header.Del("Set-Cookie")
	for _, value := range values {
		cookie, err := http.ParseSetCookie(value)
		if err == nil && !shiftBoxCookie(cookie.Name) {
			response.Header.Add("Set-Cookie", value)
		}
	}
	return nil
}
