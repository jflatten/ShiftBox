package web

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/http"
	"time"

	"github.com/jflatten/ShiftBox/internal/guest"
	"github.com/jflatten/ShiftBox/internal/launchlog"
)

// GuestLaunchTimingHandler accepts strictly typed measurements from a current guest.
func GuestLaunchTimingHandler(record func(context.Context, *x509.Certificate, time.Time, string, int64, launchlog.GuestSample) error, now func() time.Time) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+launchlog.GuestPath, func(w http.ResponseWriter, r *http.Request) {
		requestID := RequestID(r.Context())
		denied := Problem{Status: 403, Code: "guest.identity_denied", Title: "Guest timing is not authorized"}
		if r.TLS == nil || r.TLS.Version < tls.VersionTLS13 || len(r.TLS.VerifiedChains) == 0 || len(r.TLS.VerifiedChains[0]) == 0 {
			WriteProblem(w, requestID, denied)
			return
		}
		var input struct {
			OperationID     string                `json:"operationId"`
			DispatchVersion int64                 `json:"dispatchVersion"`
			Sample          launchlog.GuestSample `json:"sample"`
		}
		if err := DecodeJSON(w, r, &input, 2048); err != nil {
			WriteProblem(w, requestID, err)
			return
		}
		invalid := Problem{Status: 400, Code: "guest.invalid_launch_timing", Title: "Invalid launch timing sample"}
		if !ValidUUID(input.OperationID) || input.DispatchVersion < 1 {
			WriteProblem(w, requestID, invalid)
			return
		}
		err := record(r.Context(), r.TLS.VerifiedChains[0][0], now(), input.OperationID, input.DispatchVersion, input.Sample)
		switch {
		case errors.Is(err, guest.ErrGuestUnauthorized), errors.Is(err, launchlog.ErrGuestScope):
			WriteProblem(w, requestID, denied)
		case errors.Is(err, launchlog.ErrInvalidSample):
			WriteProblem(w, requestID, invalid)
		case err != nil:
			WriteProblem(w, requestID, err)
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	})
	return WithRequestID(mux)
}
