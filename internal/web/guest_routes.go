package web

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"time"

	"github.com/jflatten/ShiftBox/internal/guest"
)

// GuestCompatibility selects the protocol and two supported agent releases.
type GuestCompatibility struct {
	Protocol                        string
	CurrentRelease, PreviousRelease string
}

func (c GuestCompatibility) accepts(protocol, release string) bool {
	return c.Protocol != "" && c.Protocol == protocol && c.CurrentRelease != "" &&
		release != "" && (release == c.CurrentRelease || release == c.PreviousRelease)
}

// GuestEnrollmentHandler serves the only route available on the initial
// enrollment TLS listener. The grant and CSR prove the new guest identity.
func GuestEnrollmentHandler(compatibility GuestCompatibility,
	enroll func(context.Context, string, []byte, time.Time) ([]byte, error), now func() time.Time,
) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /internal/v1/enroll", func(w http.ResponseWriter, r *http.Request) {
		requestID := RequestID(r.Context())
		w.Header().Set("Cache-Control", "no-store")
		if r.TLS == nil || r.TLS.Version < tls.VersionTLS13 {
			WriteProblem(w, requestID, Problem{Status: 403, Code: "guest.tls_required", Title: "Enrollment requires internal TLS"})
			return
		}
		var input struct {
			Grant           string `json:"grant"`
			ProtocolVersion string `json:"protocolVersion"`
			GuestRelease    string `json:"guestRelease"`
			CSRPEM          string `json:"csrPem"`
		}
		if err := DecodeJSON(w, r, &input, 16<<10); err != nil {
			WriteProblem(w, requestID, err)
			return
		}
		if !compatibility.accepts(input.ProtocolVersion, input.GuestRelease) {
			WriteProblem(w, requestID, Problem{Status: 409, Code: "guest.release_unsupported", Title: "Guest release or protocol is unsupported"})
			return
		}
		raw := bytes.TrimSpace([]byte(input.CSRPEM))
		csr, rest := pem.Decode(raw)
		if csr == nil || csr.Type != "CERTIFICATE REQUEST" || len(csr.Headers) != 0 ||
			len(bytes.TrimSpace(rest)) != 0 || !bytes.HasPrefix(raw, []byte("-----BEGIN CERTIFICATE REQUEST-----")) {
			WriteProblem(w, requestID, Problem{Status: 400, Code: "guest.invalid_csr", Title: "Invalid guest certificate request"})
			return
		}
		if enroll == nil || now == nil {
			WriteProblem(w, requestID, errors.New("enrollment handler is incomplete"))
			return
		}
		der, err := enroll(r.Context(), input.Grant, csr.Bytes, now())
		if errors.Is(err, guest.ErrEnrollmentDenied) {
			WriteProblem(w, requestID, Problem{Status: 403, Code: "guest.enrollment_denied", Title: "Guest enrollment was denied"})
			return
		}
		if err != nil {
			WriteProblem(w, requestID, err)
			return
		}
		certificate := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(struct {
			ProtocolVersion string `json:"protocolVersion"`
			CertificatePEM  string `json:"certificatePem"`
		}{compatibility.Protocol, string(certificate)})
	})
	return WithRequestID(mux)
}
