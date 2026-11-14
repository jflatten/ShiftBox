package web

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/jflatten/ShiftBox/internal/guest"
	"github.com/jflatten/ShiftBox/internal/identity"
)

// GuestGitTokenHandler supplies the current owner token over verified guest TLS.
// Inputs cannot specify the workspace, owner, or repository.
func GuestGitTokenHandler(
	authenticate func(context.Context, *x509.Certificate, time.Time) (guest.Contact, error),
	resolve func(context.Context, string, string) (string, error),
	now func() time.Time,
) http.Handler {
	return WithRequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		requestID := RequestID(r.Context())
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			WriteProblem(w, requestID, Problem{Status: 405, Code: "request.method_not_allowed", Title: "Method not allowed"})
			return
		}
		denied := Problem{Status: 403, Code: "guest.identity_denied", Title: "Guest identity is not authorized"}
		if r.TLS == nil || r.TLS.Version < tls.VersionTLS13 || len(r.TLS.VerifiedChains) == 0 || len(r.TLS.VerifiedChains[0]) == 0 {
			WriteProblem(w, requestID, denied)
			return
		}
		if r.URL.RawQuery != "" || r.URL.ForceQuery || !emptyCredentialRequest(r.Body) {
			WriteProblem(w, requestID, Problem{Status: 400, Code: "request.invalid_body", Title: "Git token requests take no arguments"})
			return
		}
		if authenticate == nil || resolve == nil || now == nil {
			WriteProblem(w, requestID, Problem{Status: 503, Code: "git.unavailable", Title: "GitHub authorization is unavailable"})
			return
		}
		ctx := r.Context()
		leaf := r.TLS.VerifiedChains[0][0]
		authority, err := authenticate(ctx, leaf, now())
		if errors.Is(err, guest.ErrGuestUnauthorized) || err == nil && !gitAuthority(authority) {
			WriteProblem(w, requestID, denied)
			return
		}
		if err != nil {
			WriteProblem(w, requestID, err)
			return
		}
		token, err := resolve(ctx, authority.OwnerID, authority.Repository)
		if errors.Is(err, identity.ErrGitHubRepositoryDenied) {
			WriteProblem(w, requestID, Problem{Status: 403, Code: "git.repository_denied", Title: "GitHub authorization does not cover this repository"})
			return
		}
		if err != nil {
			WriteProblem(w, requestID, gitProblem(err))
			return
		}
		// An upstream refresh or coverage lookup may outlive the workspace
		// registration. Recheck the certificate and owner before disclosure.
		current, err := authenticate(ctx, leaf, now())
		if errors.Is(err, guest.ErrGuestUnauthorized) || err == nil && !sameGitAuthority(authority, current) {
			WriteProblem(w, requestID, denied)
			return
		}
		if err != nil || !guest.ValidGitAccessToken(token) {
			WriteProblem(w, requestID, errors.New("could not deliver the GitHub credential"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(struct {
			AccessToken string `json:"accessToken"`
		}{token})
	}))
}

func gitAuthority(contact guest.Contact) bool {
	return contact.WorkspaceID != "" && contact.Generation > 0 && contact.OwnerID != "" &&
		contact.Repository != "" && contact.DesiredState == "running"
}

func sameGitAuthority(before, after guest.Contact) bool {
	return gitAuthority(after) && before.WorkspaceID == after.WorkspaceID && before.Generation == after.Generation &&
		before.OwnerID == after.OwnerID && before.Repository == after.Repository
}

func emptyCredentialRequest(body io.Reader) bool {
	if body == nil {
		return true
	}
	// Reading a byte rejects caller-supplied scope even on chunked requests.
	var sample [1]byte
	_, err := io.ReadFull(body, sample[:])
	return err == io.EOF
}
