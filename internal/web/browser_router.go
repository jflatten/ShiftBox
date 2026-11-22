package web

import (
	"errors"
	"net/http"

	"github.com/jflatten/ShiftBox/internal/config"
)

// NewBrowserRouter dispatches TLS requests by the configured browser authority.
// A service hostname never enters the management mux, regardless of its path.
func NewBrowserRouter(hosts *config.ServiceHosts, management, services http.Handler) (http.Handler, error) {
	authority := hosts.ManagementAuthority()
	if authority == "" || management == nil || services == nil {
		return nil, errors.New("browser origin routing is incompletely configured")
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS != nil {
			if r.Host == authority {
				management.ServeHTTP(w, r)
				return
			}
			if _, _, ok := hosts.ResolveAuthority(r.Host); ok {
				services.ServeHTTP(w, r)
				return
			}
		}
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusMisdirectedRequest)
	}), nil
}
