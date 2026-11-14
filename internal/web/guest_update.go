package web

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/jflatten/ShiftBox/internal/guest"
	"github.com/jflatten/ShiftBox/internal/release"
)

// GuestUpdateHandler serves a pinned artifact selected by workspace authority.
func GuestUpdateHandler(resolve func(context.Context, *x509.Certificate, time.Time) (string, error),
	load func(context.Context, string) (release.GuestUpdate, error), now func() time.Time,
) http.Handler {
	return WithRequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		requestID := RequestID(r.Context())
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			WriteProblem(w, requestID, Problem{Status: 405, Code: "request.method_not_allowed", Title: "Method not allowed"})
			return
		}
		denied := Problem{Status: 403, Code: "guest.identity_denied", Title: "Guest update is not authorized"}
		if r.TLS == nil || r.TLS.Version < tls.VersionTLS13 || len(r.TLS.VerifiedChains) == 0 || len(r.TLS.VerifiedChains[0]) == 0 {
			WriteProblem(w, requestID, denied)
			return
		}
		if r.URL.RawQuery != "" || r.URL.ForceQuery || r.ContentLength != 0 || len(r.TransferEncoding) != 0 {
			WriteProblem(w, requestID, Problem{Status: 400, Code: "request.invalid_body", Title: "Guest update requests take no arguments"})
			return
		}
		ctx, leaf := r.Context(), r.TLS.VerifiedChains[0][0]
		id, err := resolve(ctx, leaf, now())
		if errors.Is(err, guest.ErrGuestUnauthorized) {
			WriteProblem(w, requestID, denied)
			return
		}
		if err != nil {
			WriteProblem(w, requestID, err)
			return
		}
		artifact, err := load(ctx, id)
		if err != nil || artifact.Metadata.Release != id {
			WriteProblem(w, requestID, Problem{Status: 503, Code: "guest.update_unavailable", Title: "The pinned guest update is unavailable"})
			return
		}
		current, err := resolve(ctx, leaf, now())
		if err != nil || current != id {
			WriteProblem(w, requestID, denied)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.FormatInt(artifact.Metadata.Size, 10))
		w.Header().Set("X-ShiftBox-Guest-Release", artifact.Metadata.Release)
		w.Header().Set("X-ShiftBox-Guest-Protocol", artifact.Metadata.Protocol)
		w.Header().Set("X-ShiftBox-Guest-Digest", hex.EncodeToString(artifact.Metadata.Digest[:]))
		w.Header().Set("X-ShiftBox-Guest-Signature", base64.StdEncoding.EncodeToString(artifact.Signature))
		_, _ = w.Write(artifact.Binary)
	}))
}
