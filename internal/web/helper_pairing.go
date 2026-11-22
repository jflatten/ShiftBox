package web

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/database"
)

type helperPairing struct {
	db      *sql.DB
	origin  string
	now     func() time.Time
	mu      sync.Mutex
	pending map[string]pairingApproval
}

type pairingApproval struct {
	state, challenge, redirectURI string
	sessionID, accountID          string
	expires                       time.Time
}

type pairingRequest struct {
	State       string `json:"state"`
	Challenge   string `json:"challenge"`
	RedirectURI string `json:"redirectUri"`
}

type pairingExchange struct {
	Code        string `json:"code"`
	State       string `json:"state"`
	Verifier    string `json:"verifier"`
	RedirectURI string `json:"redirectUri"`
}

var badPairing = Problem{Status: http.StatusUnauthorized, Code: "ssh.pairing_invalid", Title: "Start pairing the local helper again"}

func (s *helperPairing) approve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.TLS == nil || s.origin == "" {
		WriteProblem(w, RequestID(r.Context()), sshDenied)
		return
	}
	browser, err := SessionToken(r)
	if err != nil {
		WriteProblem(w, RequestID(r.Context()), err)
		return
	}
	var input pairingRequest
	err = DecodeJSON(w, r, &input, 4096)
	redirect := pairingLoopback(input.RedirectURI)
	if err == nil && (redirect == nil || !pairingOpaque(input.State, 22, 128) || !pairingDigest(input.Challenge)) {
		err = Problem{Status: http.StatusBadRequest, Code: "ssh.pairing_request_invalid", Title: "Invalid local helper pairing request"}
	}
	if err != nil {
		WriteProblem(w, RequestID(r.Context()), err)
		return
	}
	now := s.now()
	var approval pairingApproval
	err = database.Transaction(r.Context(), s.db, func(tx *sql.Tx) error {
		principal, err := auth.NewStore(s.db).Principal(r.Context(), tx, browser)
		if errors.Is(err, sql.ErrNoRows) {
			return sshSignIn
		}
		if err != nil {
			return err
		}
		resource := auth.Resource{Type: "workspace", OwnerID: principal.User.ID}
		if principal.Session.Mode != "normal" || !auth.Authorize(principal, "workspace.list", resource, nil, now).Allowed {
			return sshSignIn
		}
		if err := checkMutation(r, s.origin, principal.Session); err != nil {
			return err
		}
		approval = pairingApproval{state: input.State, challenge: input.Challenge, redirectURI: input.RedirectURI,
			sessionID: principal.Session.ID, accountID: principal.User.ID, expires: now.Add(5 * time.Minute)}
		return nil
	})
	if err != nil {
		WriteProblem(w, RequestID(r.Context()), err)
		return
	}
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		WriteProblem(w, RequestID(r.Context()), err)
		return
	}
	code := base64.RawURLEncoding.EncodeToString(secret[:])
	if !s.remember(auth.TokenDigest(code), approval, now) {
		WriteProblem(w, RequestID(r.Context()), Problem{Status: http.StatusServiceUnavailable, Code: "ssh.pairing_busy", Title: "Try pairing the local helper again shortly"})
		return
	}
	redirect.RawQuery = url.Values{"code": {code}, "state": {input.State}}.Encode()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"callbackUrl": redirect.String()})
}

func (s *helperPairing) exchange(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.TLS == nil || len(r.Header.Values("Origin")) != 0 {
		WriteProblem(w, RequestID(r.Context()), sshDenied)
		return
	}
	var input pairingExchange
	if err := DecodeJSON(w, r, &input, 4096); err != nil {
		WriteProblem(w, RequestID(r.Context()), err)
		return
	}
	now := s.now()
	approval, ok := s.claim(input, now)
	if !ok {
		WriteProblem(w, RequestID(r.Context()), badPairing)
		return
	}
	credential, expires, err := s.issue(r.Context(), approval, now)
	if errors.Is(err, auth.ErrHelperCredential) {
		err = badPairing
	}
	if err != nil {
		WriteProblem(w, RequestID(r.Context()), err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(struct {
		Credential string    `json:"credential"`
		ExpiresAt  time.Time `json:"expiresAt"`
		AccountID  string    `json:"accountId"`
	}{credential, expires, approval.accountID})
}

func (s *helperPairing) issue(ctx context.Context, approval pairingApproval, now time.Time) (string, time.Time, error) {
	var credential string
	var expires time.Time
	err := database.Transaction(ctx, s.db, func(tx *sql.Tx) error {
		var err error
		credential, expires, err = auth.NewStore(s.db).IssueHelperCredential(ctx, tx, approval.sessionID, now)
		return err
	})
	return credential, expires, err
}

func (s *helperPairing) remember(key string, approval pairingApproval, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for digest, pending := range s.pending {
		if !now.Before(pending.expires) {
			delete(s.pending, digest)
		}
	}
	if len(s.pending) >= 1024 {
		return false
	}
	s.pending[key] = approval
	return true
}

func (s *helperPairing) claim(input pairingExchange, now time.Time) (pairingApproval, bool) {
	if !pairingDigest(input.Code) || !pairingOpaque(input.Verifier, 43, 128) {
		return pairingApproval{}, false
	}
	sum := sha256.Sum256([]byte(input.Verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	digest := auth.TokenDigest(input.Code)
	s.mu.Lock()
	defer s.mu.Unlock()
	approval, ok := s.pending[digest]
	if !ok || !now.Before(approval.expires) || approval.state != input.State || approval.redirectURI != input.RedirectURI ||
		subtle.ConstantTimeCompare([]byte(approval.challenge), []byte(challenge)) != 1 {
		return pairingApproval{}, false
	}
	delete(s.pending, digest)
	return approval, true
}

func pairingDigest(value string) bool {
	raw, err := base64.RawURLEncoding.Strict().DecodeString(value)
	return err == nil && len(raw) == sha256.Size && len(value) == base64.RawURLEncoding.EncodedLen(sha256.Size)
}

func pairingOpaque(value string, minimum, maximum int) bool {
	if len(value) < minimum || len(value) > maximum {
		return false
	}
	for _, c := range value {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.' || c == '~') {
			return false
		}
	}
	return true
}

func pairingLoopback(value string) *url.URL {
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" ||
		u.Path == "" || u.RawPath != "" || u.Opaque != "" {
		return nil
	}
	address, err := netip.ParseAddr(u.Hostname())
	port, portErr := strconv.ParseUint(u.Port(), 10, 16)
	if err != nil || !address.IsLoopback() || address.Zone() != "" || portErr != nil || port == 0 {
		return nil
	}
	return u
}
