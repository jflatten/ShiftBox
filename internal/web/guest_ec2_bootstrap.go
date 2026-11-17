package web

import (
	"crypto/tls"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/jflatten/ShiftBox/internal/guest"
)

// GuestEC2BootstrapHandler handles the challenge exchange on the private TLS enrollment listener.
func GuestEC2BootstrapHandler(compatibility GuestCompatibility, bootstrap *guest.EC2Bootstrap) http.Handler {
	mux := http.NewServeMux()
	handle := func(grant bool) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			requestID := RequestID(r.Context())
			w.Header().Set("Cache-Control", "no-store")
			if r.TLS == nil || r.TLS.Version < tls.VersionTLS13 {
				WriteProblem(w, requestID, Problem{Status: 403, Code: "guest.tls_required", Title: "Enrollment requires internal TLS"})
				return
			}
			var input struct {
				Reference string `json:"launchReference"`
				Protocol  string `json:"protocolVersion"`
				Release   string `json:"guestRelease"`
				CSR       string `json:"csrPem"`
				Nonce     string `json:"nonce,omitempty"`
				Signature []byte `json:"signature,omitempty"`
				Proof     string `json:"stsProof,omitempty"`
			}
			if err := DecodeJSON(w, r, &input, 28<<10); err != nil {
				WriteProblem(w, requestID, err)
				return
			}
			if !compatibility.accepts(input.Protocol, input.Release) {
				WriteProblem(w, requestID, Problem{Status: 409, Code: "guest.release_unsupported", Title: "Guest release or protocol is unsupported"})
				return
			}
			csr := guestCSR(input.CSR)
			if csr == nil {
				WriteProblem(w, requestID, Problem{Status: 400, Code: "guest.invalid_csr", Title: "Invalid guest certificate request"})
				return
			}
			var response any
			var err error
			if grant {
				response, err = bootstrap.Grant(r.Context(), input.Reference, input.Nonce, csr, input.Signature, input.Proof)
			} else {
				response, err = bootstrap.Challenge(r.Context(), input.Reference, csr)
			}
			if errors.Is(err, guest.ErrEnrollmentDenied) {
				WriteProblem(w, requestID, Problem{Status: 403, Code: "guest.enrollment_denied", Title: "Guest enrollment was denied"})
				return
			}
			if err != nil {
				WriteProblem(w, requestID, Problem{Status: 503, Code: "guest.enrollment_unavailable", Title: "Guest enrollment is temporarily unavailable"})
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(response)
		}
	}
	mux.HandleFunc("POST /internal/v1/enroll/ec2/challenge", handle(false))
	mux.HandleFunc("POST /internal/v1/enroll/ec2/grant", handle(true))
	return WithRequestID(mux)
}
