package app

import (
	"context"
	"crypto/x509"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"github.com/jflatten/ShiftBox/internal/cryptostore"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/identity"
	"github.com/jflatten/ShiftBox/internal/operation"
	"github.com/jflatten/ShiftBox/internal/web"
)

// SetupHTTP composes protected local setup with committed database authority.
type SetupHTTP struct {
	DB               *sql.DB
	InstallationID   string
	Origin           string
	Cipher           *cryptostore.Store
	Admin            PasswordAdministrator
	Login            *SetupLogin
	CertificateRoots *x509.CertPool
	Postgres         *PostgresProfileSetup
	transferPostgres PostgresCredentialTransfer
	Journal          *operation.Journal
	Now              func() time.Time
	NewID            func() string
}

// NewSetupHandler exposes only currently implemented local bootstrap routes.
// It must be served by a loopback-bound TLS listener, not the public management mux.
func NewSetupHandler(setup SetupHTTP) (http.Handler, error) {
	if setup.DB == nil || setup.Cipher == nil || setup.Admin == nil || setup.Journal == nil || setup.Now == nil || setup.NewID == nil || !web.ValidUUID(setup.InstallationID) {
		return nil, ErrSetupConflict
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/setup/status", setup.status)
	mux.HandleFunc("POST /api/v1/setup/administrator", setup.administrator)
	if setup.Login != nil {
		flow := loginFlow{login: setup.Login, cookie: setupLoginCookie, cookiePath: "/auth/callback",
			redirect: "/api/v1/session", deny: setupLoginDenied, issue: setup.issueBootstrapSession}
		mux.HandleFunc("GET /auth/login", flow.start)
		mux.HandleFunc("GET /auth/callback", flow.finish)
		sessions := setup.sessionHandler()
		mux.Handle("/api/v1/session", sessions)
		mux.Handle("PUT /api/v1/setup/data-profile", setup.profileHandler(database.PreflightPostgresSetup))
		mux.Handle("PUT /api/v1/setup/certificate", setup.certificateHandler())
		mux.Handle("PUT /api/v1/admin/capacity", setup.capacityHandler())
		mux.Handle("POST /api/v1/setup/activate", setup.activationHandler())
		if passwords, ok := setup.Admin.(setupPasswords); ok {
			mux.Handle("PUT /api/v1/session/password", setup.passwordHandler(sessions, passwords))
		}
	}
	guarded, err := ProtectBootstrap(setup.Origin, mux)
	if err != nil {
		return nil, err
	}
	return web.WithRequestID(guarded), nil
}

func (s SetupHTTP) status(w http.ResponseWriter, r *http.Request) {
	var state string
	var userID sql.NullString
	var ready, change sql.NullBool
	var profileSelected, certificateConfigured bool
	err := s.DB.QueryRowContext(r.Context(), `SELECT i.state,b.user_id,b.credential_ready,u.must_change_password, EXISTS (
		SELECT 1 FROM operations o JOIN operation_steps step ON step.operation_id=o.id
		WHERE o.requested_by=b.user_id AND o.type='setup' AND o.status='succeeded'
		AND step.step_key IN ($2,$3) AND step.status='succeeded'
	),
		EXISTS (SELECT 1 FROM browser_certificates WHERE state='active') FROM installations i LEFT JOIN bootstrap_administrator b ON b.installation_id=i.id LEFT JOIN users u ON u.id=b.user_id WHERE i.id=$1`, s.InstallationID, profileStep, postgresProfileStep).Scan(&state, &userID, &ready, &change, &profileSelected, &certificateConfigured)
	if err != nil {
		setupProblem(w, r, err)
		return
	}
	completed, available := []string{}, []string{}
	if state == "bootstrap" || state == "configuring" {
		if !userID.Valid || !ready.Bool {
			available = append(available, "administrator")
		} else {
			completed = append(completed, "administrator")
			if change.Bool {
				available = append(available, "password")
			} else {
				completed = append(completed, "password")
				if profileSelected {
					completed = append(completed, "data_profile")
					if certificateConfigured {
						completed = append(completed, "certificate")
						available = append(available, "provider")
					} else {
						available = append(available, "certificate")
					}
				} else {
					available = append(available, "data_profile")
				}
			}
		}
	}
	writeSetupJSON(w, 200, struct {
		State     string   `json:"state"`
		LocalOnly bool     `json:"localOnly"`
		Completed []string `json:"completedSteps"`
		Available []string `json:"availableSteps"`
	}{state, true, completed, available})
}

func (s SetupHTTP) administrator(w http.ResponseWriter, r *http.Request) {
	// The first account has no session yet. Exact Origin prevents a foreign page
	// from driving this otherwise unauthenticated, loopback-only bootstrap mutation.
	origins := r.Header.Values("Origin")
	if len(origins) != 1 || origins[0] != s.Origin {
		setupProblem(w, r, web.Problem{Status: 403, Code: "setup.invalid_origin", Title: "Return to the local setup page"})
		return
	}
	key, err := web.IdempotencyKey(r)
	if err != nil {
		setupProblem(w, r, err)
		return
	}
	var body struct {
		Email string `json:"email"`
	}
	if err := web.DecodeJSON(w, r, &body, 16<<10); err != nil {
		setupProblem(w, r, err)
		return
	}
	body.Email = strings.ToLower(strings.TrimSpace(body.Email))
	address, err := mail.ParseAddress(body.Email)
	if err != nil || address.Name != "" || address.Address != body.Email {
		setupProblem(w, r, web.Problem{Status: 400, Code: "setup.invalid_email", Title: "Enter a valid administrator email"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	admission, err := BeginAdministrator(ctx, s.DB, s.Journal, AdministratorIntent{InstallationID: s.InstallationID, UserID: s.NewID(), Email: body.Email, CreatedAt: s.Now()}, s.Cipher, key)
	if err != nil {
		setupProblem(w, r, err)
		return
	}
	admission.Operation, err = FinishAdministrator(ctx, s.DB, s.Journal, s.InstallationID, s.Admin, admission.Operation)
	if err != nil {
		setupProblem(w, r, err)
		return
	}
	password, err := ClaimTemporaryPassword(ctx, s.DB, s.InstallationID, s.Cipher, s.Now())
	if err != nil {
		setupProblem(w, r, err)
		return
	}
	writeSetupJSON(w, 201, struct {
		AdministratorAdmission
		TemporaryPassword string `json:"temporaryPassword"`
	}{admission, password})
}

func setupProblem(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, ErrSetupConflict) || errors.Is(err, operation.ErrConflict) {
		err = web.Problem{Status: 409, Code: "setup.state_conflict", Title: "Refresh the local setup state", Detail: "An issued password is never shown again. If delivery was lost, use the explicit protected local reset."}
	}
	if errors.Is(err, identity.ErrPasswordAdministration) {
		err = web.Problem{Status: 503, Code: "setup.identity_unavailable", Title: "Local sign-in is not ready", Detail: "Retry setup when the local identity service is available.", Retryable: true}
	}
	web.WriteProblem(w, web.RequestID(r.Context()), err)
}

func writeSetupJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value) // Lost one-time delivery requires explicit local reset.
}
