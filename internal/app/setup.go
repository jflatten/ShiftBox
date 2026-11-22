package app

import (
	"errors"
	"github.com/jflatten/ShiftBox/internal/web"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
)

// ProtectBootstrap restricts the setup surface to direct loopback HTTPS requests
// with the configured literal-IP authority. It deliberately ignores forwarding
// headers; setup is not exposed through a reverse proxy or a guest service host.
// The listener must also bind loopback, and inner handlers must enforce the
// temporary-password, session, Origin/CSRF, and installation-state gates.
func ProtectBootstrap(origin string, next http.Handler) (http.Handler, error) {
	parsed, err := url.Parse(origin)
	if err != nil || next == nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || parsed.String() != origin {
		return nil, errors.New("setup requires a canonical loopback HTTPS origin")
	}
	address, err := netip.ParseAddr(parsed.Hostname())
	if err != nil || !address.IsLoopback() || address.Zone() != "" {
		return nil, errors.New("setup origin must use a loopback IP address")
	}
	host := address.String()
	if address.Is6() {
		host = "[" + host + "]"
	}
	if port := parsed.Port(); port != "" {
		value, err := strconv.Atoi(port)
		if err != nil || value < 1 || value > 65535 || value == 443 || strconv.Itoa(value) != port {
			return nil, errors.New("setup origin has an invalid or noncanonical port")
		}
		host = net.JoinHostPort(address.String(), port)
	}
	if parsed.Host != host {
		return nil, errors.New("setup origin must be canonical")
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		peer, _, err := net.SplitHostPort(r.RemoteAddr)
		ip, parseErr := netip.ParseAddr(peer)
		if err != nil || parseErr != nil || !ip.IsLoopback() || r.TLS == nil || r.Host != host {
			web.WriteProblem(w, web.RequestID(r.Context()), web.Problem{Status: 403, Code: "setup.local_access_required", Title: "Open setup on the controller's local HTTPS address"})
			return
		}
		next.ServeHTTP(w, r)
	}), nil
}
