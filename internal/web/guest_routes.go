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
	"github.com/jflatten/ShiftBox/internal/workspace"
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
		csr := guestCSR(input.CSRPEM)
		if csr == nil {
			WriteProblem(w, requestID, Problem{Status: 400, Code: "guest.invalid_csr", Title: "Invalid guest certificate request"})
			return
		}
		if enroll == nil || now == nil {
			WriteProblem(w, requestID, errors.New("enrollment handler is incomplete"))
			return
		}
		der, err := enroll(r.Context(), input.Grant, csr, now())
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

func guestCSR(value string) []byte {
	raw := bytes.TrimSpace([]byte(value))
	csr, rest := pem.Decode(raw)
	if csr == nil || csr.Type != "CERTIFICATE REQUEST" || len(csr.Headers) != 0 ||
		len(bytes.TrimSpace(rest)) != 0 || !bytes.HasPrefix(raw, []byte("-----BEGIN CERTIFICATE REQUEST-----")) {
		return nil
	}
	return csr.Bytes
}

// GuestRenewalHandler serves renewal on the internal mutual TLS listener.
func GuestRenewalHandler(renew func(context.Context, *x509.Certificate, []byte, time.Time) ([]byte, error), now func() time.Time) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+guest.RenewalPath, func(w http.ResponseWriter, r *http.Request) {
		requestID := RequestID(r.Context())
		w.Header().Set("Cache-Control", "no-store")
		denied := Problem{Status: 403, Code: "guest.identity_denied", Title: "Guest identity is not authorized"}
		if r.TLS == nil || r.TLS.Version < tls.VersionTLS13 || len(r.TLS.VerifiedChains) == 0 || len(r.TLS.VerifiedChains[0]) == 0 {
			WriteProblem(w, requestID, denied)
			return
		}
		var input struct {
			CSRPEM string `json:"csrPem"`
		}
		if err := DecodeJSON(w, r, &input, 16<<10); err != nil {
			WriteProblem(w, requestID, err)
			return
		}
		csr := guestCSR(input.CSRPEM)
		if csr == nil {
			WriteProblem(w, requestID, Problem{Status: 400, Code: "guest.invalid_csr", Title: "Invalid guest certificate request"})
			return
		}
		der, err := renew(r.Context(), r.TLS.VerifiedChains[0][0], csr, now())
		if errors.Is(err, guest.ErrGuestUnauthorized) {
			WriteProblem(w, requestID, denied)
			return
		}
		if err != nil {
			WriteProblem(w, requestID, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"certificatePem": string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		})
	})
	return WithRequestID(mux)
}

// GuestPollHandler verifies current authority on every poll, including requests
// on an existing TLS connection. Mount it on the internal mTLS listener.
func GuestPollHandler(compatibility GuestCompatibility,
	exchange func(context.Context, *x509.Certificate, time.Time, guest.PollReport) (guest.Contact, *guest.PollOperation, error), now func() time.Time, tunnelAddress string,
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
			ProtocolVersion      string                  `json:"protocolVersion"`
			GuestRelease         string                  `json:"guestRelease"`
			LastOperationID      *string                 `json:"lastOperationId"`
			LastOperationVersion int64                   `json:"lastOperationVersion"`
			OperationReport      *guest.OperationReport  `json:"operationReport"`
			Probes               []workspace.ProbeResult `json:"probes"`
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
		if !workspace.ValidProbeResults(input.Probes) {
			WriteProblem(w, requestID, Problem{Status: 400, Code: "request.invalid_body", Title: "Invalid guest probe results"})
			return
		}
		if exchange == nil || now == nil {
			WriteProblem(w, requestID, errors.New("guest polling is incompletely wired"))
			return
		}
		stamp := now().UTC()
		report := guest.PollReport{GuestRelease: input.GuestRelease, ProtocolVersion: input.ProtocolVersion,
			Version: input.LastOperationVersion, Operation: input.OperationReport, Probes: input.Probes}
		if input.Activity != nil {
			report.Activity = &workspace.CPUActivity{SampledAt: input.Activity.SampledAt, Percent: input.Activity.CPUPercent}
		}
		if input.LastOperationID != nil {
			report.LastOperationID = *input.LastOperationID
		}
		contact, dispatch, err := exchange(r.Context(), r.TLS.VerifiedChains[0][0], stamp, report)
		if errors.Is(err, guest.ErrGuestUnauthorized) {
			WriteProblem(w, requestID, guestDenied)
			return
		}
		if errors.Is(err, guest.ErrGuestReportConflict) {
			WriteProblem(w, requestID, Problem{Status: 409, Code: "operation.conflict", Title: "Guest operation report conflicted with the current dispatch"})
			return
		}
		if err != nil {
			WriteProblem(w, requestID, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set(guest.ProbeReportsHeader, "1")
		if tunnelAddress != "" && contact.DesiredState == "running" {
			w.Header().Set(guest.TunnelAddressHeader, tunnelAddress)
			w.Header().Set(guest.PreviewPortsHeader, contact.PreviewPorts)
		}
		delaySeconds := 30
		if contact.LaunchAwaitingDispatch {
			delaySeconds = 1
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"protocolVersion": compatibility.Protocol, "serverTime": stamp,
			"workspaceGeneration": contact.Generation, "desiredState": contact.DesiredState, "operation": dispatch,
			"certificate":          map[string]time.Time{"renewAfter": contact.RenewAfter},
			"nextPollAfterSeconds": delaySeconds,
		})
	})
	return WithRequestID(mux)
}
