package app

import (
	"context"
	"crypto/x509"
	"database/sql"
	"errors"
	"net/http"
	"os"
	"time"

	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/identity"
	"github.com/jflatten/ShiftBox/internal/operation"
	"github.com/jflatten/ShiftBox/internal/web"
)

// PostgresCredentialTransfer imports the changed owner credential into managed Dex.
type PostgresCredentialTransfer func(context.Context, identity.DexPostgres, string, string) error

// WithPostgresTransfer wires the controller-owned Dex transfer into protected setup.
func (s SetupHTTP) WithPostgresTransfer(transfer PostgresCredentialTransfer) SetupHTTP {
	s.transferPostgres = transfer
	return s
}

func (s SetupHTTP) postgresActivationResponse(actor, key, token string, selected selectedPostgresProfile) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
		defer cancel()
		target, err := openSelectedPostgres(ctx, selected.Credentials, true)
		if err != nil {
			setupProblem(w, r, err)
			return
		}
		defer target.Close()
		owner, err := StagePostgresSetup(ctx, s.DB, target, s.InstallationID, selected.Credentials, s.Cipher, s.Now())
		if err != nil || owner.ID != actor {
			setupProblem(w, r, ErrSetupConflict)
			return
		}
		dex, err := loadPostgresDexStorage(ctx, target, s.InstallationID, selected.Credentials.ApplicationURI, s.Cipher)
		if err != nil {
			setupProblem(w, r, err)
			return
		}
		if err := s.transferPostgres(ctx, *dex, owner.PrimaryEmail, owner.ID); err != nil {
			setupProblem(w, r, errors.New("could not transfer the initial administrator credential"))
			return
		}
		op, err := s.activatePostgres(ctx, target, actor, key, selected.RequestKey, token)
		if err != nil {
			setupProblem(w, r, err)
			return
		}
		web.ClearSessionCookie(w)
		writeSetupJSON(w, http.StatusAccepted, op)
	})
}

func openSelectedPostgres(ctx context.Context, credentials PostgresSetupCredentials, migrate bool) (*sql.DB, error) {
	roots, err := x509.SystemCertPool()
	if err != nil {
		return nil, errors.New("cannot load PostgreSQL system trust")
	}
	if credentials.CAFile != "" {
		roots = x509.NewCertPool()
		pem, err := os.ReadFile(credentials.CAFile)
		if err != nil || !roots.AppendCertsFromPEM(pem) {
			return nil, errors.New("cannot load the PostgreSQL certificate authority")
		}
	}
	target, err := database.OpenPostgresSetupApplication(ctx, database.PostgresSetupTarget{
		ApplicationURI: credentials.ApplicationURI, DexURI: credentials.DexURI, Roots: roots,
	})
	if err != nil {
		return nil, err
	}
	if migrate {
		if err := database.Migrate(ctx, target, "postgres"); err != nil {
			target.Close()
			return nil, errors.New("cannot migrate the scoped PostgreSQL schema")
		}
	}
	return target, nil
}

// ResumeRetiredPostgresSetup probes only a retired bootstrap installation and
// completes its recorded cutover using the encrypted selected connection.
func (s SetupHTTP) ResumeRetiredPostgresSetup(ctx context.Context) (operation.Operation, error) {
	selected, err := s.readPendingPostgres(ctx, s.DB)
	if err != nil {
		return operation.Operation{}, ErrSetupConflict
	}
	target, err := openSelectedPostgres(ctx, selected.Credentials, false)
	if err != nil {
		return operation.Operation{}, err
	}
	defer target.Close()
	return s.ResumePostgresActivation(ctx, target)
}
