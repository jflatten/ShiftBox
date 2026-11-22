package web

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jflatten/ShiftBox/internal/guest"
	"github.com/jflatten/ShiftBox/internal/secret"
)

// GuestSecretHandler provides a single effective secret for the TLS peer.
func GuestSecretHandler(resolve func(context.Context, *x509.Certificate, time.Time, string) ([]byte, string, error),
	now func() time.Time,
) http.Handler {
	return WithRequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		id := RequestID(r.Context())
		denied := Problem{Status: 403, Code: "guest.identity_denied", Title: "Guest identity is not authorized"}
		if r.TLS == nil || r.TLS.Version < tls.VersionTLS13 || len(r.TLS.VerifiedChains) == 0 || len(r.TLS.VerifiedChains[0]) == 0 {
			WriteProblem(w, id, denied)
			return
		}
		tail, found := strings.CutPrefix(r.URL.Path, guest.SecretResolvePrefix)
		name, suffix := strings.CutSuffix(tail, ":resolve")
		if !found || !suffix || !secret.ValidName(name) {
			WriteProblem(w, id, Problem{Status: 400, Code: "secret.invalid_name", Title: "Choose a valid secret name"})
			return
		}
		var hints *struct{}
		if r.URL.RawQuery != "" || r.URL.ForceQuery || (r.ContentLength != 0 &&
			(DecodeJSON(w, r, &hints, 1024) != nil || hints == nil)) {
			WriteProblem(w, id, Problem{Status: 400, Code: "request.invalid_body", Title: "Secret requests take no scope or identity arguments"})
			return
		}
		stamp := now()
		value, source, err := resolve(r.Context(), r.TLS.VerifiedChains[0][0], stamp, name)
		defer clear(value)
		switch {
		case errors.Is(err, guest.ErrGuestUnauthorized):
			WriteProblem(w, id, denied)
		case errors.Is(err, sql.ErrNoRows):
			WriteProblem(w, id, Problem{Status: 404, Code: "secret.not_found", Title: "No effective secret was found"})
		case err != nil:
			WriteProblem(w, id, err)
		default:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(struct {
				Name        string    `json:"name"`
				Value       string    `json:"value"`
				Source      string    `json:"source"`
				RetrievedAt time.Time `json:"retrievedAt"`
			}{name, string(value), source, stamp.UTC()})
		}
	}))
}
