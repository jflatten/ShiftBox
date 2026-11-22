package web

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jflatten/ShiftBox/internal/guest"
	"github.com/jflatten/ShiftBox/internal/secret"
)

// GuestSecretListHandler returns effective names and scopes for the verified
// guest identity. The caller cannot supply workspace, owner, or scope arguments.
func GuestSecretListHandler(list func(context.Context, *x509.Certificate, time.Time) ([]secret.Key, error), now func() time.Time) http.Handler {
	return WithRequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		id := RequestID(r.Context())
		denied := Problem{Status: 403, Code: "guest.identity_denied", Title: "Guest identity is not authorized"}
		if r.TLS == nil || r.TLS.Version < tls.VersionTLS13 || len(r.TLS.VerifiedChains) == 0 || len(r.TLS.VerifiedChains[0]) == 0 {
			WriteProblem(w, id, denied)
			return
		}
		if r.Method != http.MethodGet || r.URL.Path != guest.SecretListPath || r.URL.RawQuery != "" || r.URL.ForceQuery || r.ContentLength != 0 {
			WriteProblem(w, id, Problem{Status: 400, Code: "request.invalid_body", Title: "Secret listing takes no scope or identity arguments"})
			return
		}
		keys, err := list(r.Context(), r.TLS.VerifiedChains[0][0], now())
		switch {
		case errors.Is(err, guest.ErrGuestUnauthorized):
			WriteProblem(w, id, denied)
		case err != nil:
			WriteProblem(w, id, err)
		default:
			if keys == nil {
				keys = []secret.Key{}
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(struct {
				Keys []secret.Key `json:"keys"`
			}{Keys: keys})
		}
	}))
}
