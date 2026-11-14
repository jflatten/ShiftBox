package web

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
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

// GuestPollHandler verifies current authority on every poll, including requests
// on an existing TLS connection. Mount it on the internal mTLS listener.
func GuestPollHandler(compatibility GuestCompatibility,
	authenticate func(context.Context, *x509.Certificate, time.Time) (guest.Contact, error), now func() time.Time,
) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /internal/v1/guest/poll", func(w http.ResponseWriter, r *http.Request) {
		requestID := RequestID(r.Context())
		w.Header().Set("Cache-Control", "no-store")
		guestDenied := Problem{Status: 403, Code: "guest.identity_denied", Title: "Guest identity is not authorized"}
		if r.TLS == nil || r.TLS.Version < tls.VersionTLS13 || len(r.TLS.VerifiedChains) == 0 || len(r.TLS.VerifiedChains[0]) == 0 {
			WriteProblem(w, requestID, guestDenied)
			return
		}
		var input struct {
			ProtocolVersion      string          `json:"protocolVersion"`
			GuestRelease         string          `json:"guestRelease"`
			LastOperationID      *string         `json:"lastOperationId"`
			LastOperationVersion int64           `json:"lastOperationVersion"`
			OperationReport      json.RawMessage `json:"operationReport"`
			Activity             *struct {
				SampledAt      time.Time `json:"sampledAt"`
				CPUPercent     float64   `json:"cpuPercent"`
				IDEConnections int64     `json:"ideConnections"`
			} `json:"activity"`
			Tunnel *struct {
				Connected bool   `json:"connected"`
				SessionID string `json:"sessionId"`
			} `json:"tunnel"`
		}
		if err := DecodeJSON(w, r, &input, 16<<10); err != nil {
			WriteProblem(w, requestID, err)
			return
		}
		if !compatibility.accepts(input.ProtocolVersion, input.GuestRelease) {
			WriteProblem(w, requestID, Problem{Status: 409, Code: "guest.release_unsupported", Title: "Guest release or protocol is unsupported"})
			return
		}
		if len(input.OperationReport) != 0 && string(input.OperationReport) != "null" {
			WriteProblem(w, requestID, Problem{Status: 409, Code: "operation.conflict", Title: "No guest operation has been dispatched"})
			return
		}
		if authenticate == nil || now == nil {
			WriteProblem(w, requestID, errors.New("guest polling is incompletely wired"))
			return
		}
		stamp := now().UTC()
		contact, err := authenticate(r.Context(), r.TLS.VerifiedChains[0][0], stamp)
		if errors.Is(err, guest.ErrGuestUnauthorized) {
			WriteProblem(w, requestID, guestDenied)
			return
		}
		if err != nil {
			WriteProblem(w, requestID, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"protocolVersion": compatibility.Protocol, "serverTime": stamp,
			"workspaceGeneration": contact.Generation, "desiredState": contact.DesiredState, "operation": nil,
			"certificate": map[string]time.Time{"renewAfter": contact.RenewAfter},
		})
	})
	return WithRequestID(mux)
}
