package app

import (
	"context"
	"crypto/x509"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/cryptostore"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/operation"
	"github.com/jflatten/ShiftBox/internal/web"
)

const (
	postgresProfileStep     = "validate_postgres_profile"
	postgresProfileEvidence = "Scoped PostgreSQL application and identity schemas verified."
)

// PostgresProfileSetup is the scoped identity login supplied by the local
// operator for a pre-provisioned PostgreSQL target.
type PostgresProfileSetup struct {
	DexURI string
	CAFile string
	Roots  *x509.CertPool
}

type setupProfileInput struct {
	Profile            string  `json:"profile"`
	ConnectionURI      string  `json:"connectionUri"`
	SetupConnectionURI *string `json:"setupConnectionUri"`
	TLSMode            string  `json:"tlsMode"`
}

func (s SetupHTTP) selectPostgresProfile(ctx context.Context, tx *sql.Tx, r *http.Request, p auth.Principal, key string,
	input setupProfileInput, preflight func(context.Context, database.PostgresSetupTarget) error,
) (http.Handler, error) {
	if s.Postgres == nil {
		return nil, web.Problem{Status: 409, Code: "setup.postgres_identity_required", Title: "Supply the scoped Dex login in local setup"}
	}
	credentials := PostgresSetupCredentials{ApplicationURI: input.ConnectionURI, DexURI: s.Postgres.DexURI, CAFile: s.Postgres.CAFile}
	if input.TLSMode != "verify-full" || input.SetupConnectionURI != nil {
		return nil, invalidProfile()
	}
	if _, err := postgresDexStorage(credentials.ApplicationURI, credentials.DexURI, credentials.CAFile); err != nil {
		return nil, invalidProfile()
	}
	var version int64
	if err := tx.QueryRowContext(ctx, "SELECT version FROM installations WHERE id=$1 AND data_profile='sqlite'", s.InstallationID).Scan(&version); err != nil {
		return nil, err
	}
	var previous string
	err := tx.QueryRowContext(ctx, "SELECT id FROM operations WHERE requested_by=$1 AND request_key=$2", p.User.ID, key).Scan(&previous)
	if err == nil {
		selected, readErr := s.readPendingPostgres(ctx, tx)
		if errors.Is(readErr, sql.ErrNoRows) || (readErr == nil && (selected.RequestKey != key || selected.Credentials != credentials)) {
			return nil, profileConflict()
		}
		if readErr != nil {
			return nil, readErr
		}
		op, err := s.Journal.GetInTx(ctx, tx, previous)
		if err != nil {
			return nil, err
		}
		return profileResponse(op), nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	token, err := web.SessionToken(r)
	if err != nil {
		return nil, err
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		if err := preflight(ctx, database.PostgresSetupTarget{ApplicationURI: credentials.ApplicationURI,
			DexURI: credentials.DexURI, Roots: s.Postgres.Roots}); err != nil {
			setupProblem(w, r, web.Problem{Status: 409, Code: "setup.postgres_validation_failed",
				Title: "PostgreSQL validation failed", Detail: "Check the database, TLS trust, and isolated ShiftBox and Dex schema owners"})
			return
		}
		var op operation.Operation
		err := database.Transaction(ctx, s.DB, func(tx *sql.Tx) error {
			current, err := s.bootstrapPrincipal(ctx, tx, token)
			if errors.Is(err, sql.ErrNoRows) || (err == nil && !auth.Authorize(current, "setup.mutate", auth.Resource{}, nil, s.Now()).Allowed) {
				return web.Problem{Status: 401, Code: "auth.required", Title: "Sign in to continue"}
			}
			if err != nil {
				return err
			}
			result, err := tx.ExecContext(ctx, `UPDATE installations SET state='configuring',version=version+1,updated_at=$1
				WHERE id=$2 AND version=$3 AND state IN ('bootstrap','configuring') AND data_profile='sqlite'`,
				s.Now().UTC().Format(time.RFC3339Nano), s.InstallationID, version)
			if err := checkCutoverWrite(result, err); err != nil {
				if errors.Is(err, ErrSetupConflict) {
					return profileConflict()
				}
				return err
			}
			if err := s.savePendingPostgres(ctx, tx, selectedPostgresProfile{Credentials: credentials, RequestKey: key}); err != nil {
				return err
			}
			op, err = s.Journal.RecordLocalStepInTx(ctx, tx, operation.Request{
				Actor: current.User.ID, Key: key, Type: "setup", OriginatingAction: "setup.mutate", Generation: 1,
				Steps: []operation.Step{{Key: postgresProfileStep, Label: "Validate PostgreSQL profile"}},
			}, postgresProfileEvidence)
			return err
		})
		if err != nil {
			setupProblem(w, r, err)
			return
		}
		writeSetupJSON(w, http.StatusAccepted, op)
	}), nil
}

type selectedPostgresProfile struct {
	RequestKey  string
	Credentials PostgresSetupCredentials
}

func (s SetupHTTP) pendingPostgresContext() cryptostore.Context {
	return cryptostore.Context{KeyVersion: 1, RecordType: "setup_postgres", Scope: "installation", OwnerID: s.InstallationID,
		RecordID: s.InstallationID + ":setup-postgres", Name: "scoped_credentials"}
}

func (s SetupHTTP) savePendingPostgres(ctx context.Context, tx *sql.Tx, profile selectedPostgresProfile) error {
	raw, err := json.Marshal(profile)
	if err != nil {
		return err
	}
	defer clear(raw)
	binding := s.pendingPostgresContext()
	sealed, err := s.Cipher.Seal(binding, raw)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(sealed)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO protected_values(id,value_json) VALUES($1,$2)
		ON CONFLICT(id) DO UPDATE SET value_json=excluded.value_json`, binding.RecordID, string(encoded))
	return err
}

func (s SetupHTTP) readPendingPostgres(ctx context.Context, db interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) (selectedPostgresProfile, error) {
	var raw string
	var profile selectedPostgresProfile
	binding := s.pendingPostgresContext()
	if err := db.QueryRowContext(ctx, "SELECT value_json FROM protected_values WHERE id=$1", binding.RecordID).Scan(&raw); err != nil {
		return profile, err
	}
	var sealed cryptostore.Value
	if err := json.Unmarshal([]byte(raw), &sealed); err != nil {
		return profile, err
	}
	decoded, err := s.Cipher.Open(binding, sealed)
	if err != nil {
		return profile, err
	}
	defer clear(decoded)
	err = json.Unmarshal(decoded, &profile)
	return profile, err
}
