package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/cryptostore"
	"github.com/jflatten/ShiftBox/internal/identity"
	"github.com/jflatten/ShiftBox/internal/web"
)

// ErrSetupConflict means protected setup no longer permits this initial-owner intent.
var ErrSetupConflict = errors.New("initial administrator setup conflicts with durable state")

// AdministratorIntent supplies the stable IDs chosen before credential creation.
type AdministratorIntent struct {
	InstallationID string
	UserID         string
	Email          string
	CreatedAt      time.Time
}

// AdministratorRecord is internal reconciliation input, never an API read model.
// The hash may be sent only to the authenticated private Dex administration API.
type AdministratorRecord struct {
	UserID       string `json:"userId"`
	Email        string `json:"email"`
	PasswordHash []byte `json:"-"`
}

// PrepareAdministrator atomically records the single initial owner, its hash and
// an encrypted one-time delivery value. A retry reads the existing intent, never
// generates a replacement. Caller rolls back on error and contacts Dex only
// after commit. Setup HTTP access must already have passed the local guard.
func PrepareAdministrator(ctx context.Context, tx *sql.Tx, input AdministratorIntent, cipher *cryptostore.Store) (AdministratorRecord, error) {
	if cipher == nil || !web.ValidUUID(input.InstallationID) || !web.ValidUUID(input.UserID) || input.CreatedAt.IsZero() {
		return AdministratorRecord{}, ErrSetupConflict
	}
	var state string
	if err := tx.QueryRowContext(ctx, `SELECT state FROM installations WHERE id=$1`, input.InstallationID).Scan(&state); err != nil {
		return AdministratorRecord{}, err
	}
	if state != "bootstrap" && state != "configuring" {
		return AdministratorRecord{}, ErrSetupConflict
	}
	email := strings.ToLower(strings.TrimSpace(input.Email))
	var existing AdministratorRecord
	err := tx.QueryRowContext(ctx, `SELECT u.id,u.primary_email,b.password_hash FROM bootstrap_administrator b JOIN users u ON u.id=b.user_id WHERE b.installation_id=$1`, input.InstallationID).Scan(&existing.UserID, &existing.Email, &existing.PasswordHash)
	if err == nil {
		if existing.Email != email {
			return AdministratorRecord{}, ErrSetupConflict
		}
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return AdministratorRecord{}, err
	}
	var users int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM users`).Scan(&users); err != nil {
		return AdministratorRecord{}, err
	}
	if users != 0 {
		return AdministratorRecord{}, ErrSetupConflict
	}
	password, hash, err := identity.NewTemporaryPassword()
	if err != nil {
		return AdministratorRecord{}, err
	}
	encrypted, err := cipher.Seal(bootstrapPasswordContext(input.InstallationID, input.UserID), []byte(password))
	if err != nil {
		return AdministratorRecord{}, err
	}
	value, err := json.Marshal(encrypted)
	if err != nil {
		return AdministratorRecord{}, err
	}
	user, err := auth.NewStore(nil).CreateUser(ctx, tx, auth.User{ID: input.UserID, PrimaryEmail: email, Role: "administrator", MembershipState: "active", CredentialMode: "local", MustChangePassword: true}, "local_setup", input.CreatedAt)
	if err != nil {
		return AdministratorRecord{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO protected_values(id,value_json) VALUES($1,$2)`, input.UserID, string(value)); err != nil {
		return AdministratorRecord{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO bootstrap_administrator(installation_id,user_id,password_hash,password_ciphertext_id,created_at) VALUES($1,$2,$3,$2,$4)`, input.InstallationID, input.UserID, string(hash), input.CreatedAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return AdministratorRecord{}, err
	}
	return AdministratorRecord{UserID: user.ID, Email: user.PrimaryEmail, PasswordHash: hash}, nil
}

func bootstrapPasswordContext(installationID, userID string) cryptostore.Context {
	return cryptostore.Context{KeyVersion: 1, RecordType: "bootstrap_password", Scope: "installation", OwnerID: installationID, RecordID: userID, Name: "temporary_password"}
}
