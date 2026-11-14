package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jflatten/ShiftBox/internal/audit"
	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/cryptostore"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/identity"
	"github.com/jflatten/ShiftBox/internal/web"
)

type temporaryCredential struct {
	Password string
	Hash     []byte
}

func (s *ControllerState) createLocalUserHandler(dex PasswordAdministrator, now func() time.Time) web.AuthorizedHandler {
	store := auth.NewStore(s.DB)
	return web.AuthorizedHandler{
		DB: s.DB, Origin: s.Installation.Settings.ManagementOrigin, Now: now,
		Route: web.ManagementRoute{Method: http.MethodPost, Path: "/admin/users", OperationID: "createLocalUser", Action: "user.create"},
		Lookup: func(ctx context.Context, tx *sql.Tx, token string) (auth.Principal, error) {
			// Serialize membership mutations and local email reservations.
			if _, err := tx.ExecContext(ctx, "UPDATE installations SET version=version WHERE singleton=1"); err != nil {
				return auth.Principal{}, err
			}
			return store.Principal(ctx, tx, token)
		},
		Resolve: func(context.Context, *sql.Tx, *http.Request, auth.Principal) (web.AuthorizationTarget, error) {
			return web.AuthorizationTarget{Action: "user.create"}, nil
		},
		Apply: func(ctx context.Context, tx *sql.Tx, r *http.Request, actor auth.Principal, _ web.AuthorizationTarget, _ auth.Decision) (http.Handler, error) {
			var input struct {
				Email string `json:"email"`
				Role  string `json:"role"`
			}
			if err := web.DecodeJSON(nil, r, &input, 4096); err != nil {
				return nil, err
			}
			email := strings.ToLower(strings.TrimSpace(input.Email))
			address, err := mail.ParseAddress(email)
			if err != nil || address.Name != "" || address.Address != email || (input.Role != "member" && input.Role != "administrator") {
				return nil, web.Problem{Status: 400, Code: "user.invalid_local_account", Title: "Enter an email address and choose a role"}
			}
			if s.cipher == nil || dex == nil {
				return nil, localUserUnavailable()
			}
			user, credential, err := s.prepareLocalUser(ctx, tx, email, input.Role)
			if err != nil {
				return nil, err
			}
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
				defer cancel()
				if err := dex.Ensure(ctx, user.PrimaryEmail, user.ID, credential.Hash); err != nil {
					if errors.Is(err, identity.ErrLocalIdentityConflict) {
						err = localUserConflict()
					} else {
						err = localUserUnavailable()
					}
					web.WriteProblem(w, web.RequestID(ctx), err)
					return
				}
				created, err := s.finishLocalUser(ctx, r, store, user, actor.User.ID, now())
				if err != nil {
					web.WriteProblem(w, web.RequestID(ctx), err)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusCreated)
				_ = json.NewEncoder(w).Encode(struct {
					User              auth.User `json:"user"`
					TemporaryPassword string    `json:"temporaryPassword"`
				}{created, credential.Password})
			}), nil
		},
	}
}

func (s *ControllerState) prepareLocalUser(ctx context.Context, tx *sql.Tx, email, role string) (auth.User, temporaryCredential, error) {
	var exists bool
	if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM users WHERE credential_mode='local' AND lower(primary_email)=$1)", email).Scan(&exists); err != nil {
		return auth.User{}, temporaryCredential{}, err
	}
	if exists {
		return auth.User{}, temporaryCredential{}, localUserConflict()
	}
	user := auth.User{PrimaryEmail: email, Role: role, MembershipState: "active", CredentialMode: "local", MustChangePassword: true}
	var reservedRole, encoded string
	err := tx.QueryRowContext(ctx, "SELECT user_id,role,credential_json FROM local_user_creations WHERE email=$1", email).
		Scan(&user.ID, &reservedRole, &encoded)
	if err == nil {
		if reservedRole != role {
			return auth.User{}, temporaryCredential{}, localUserConflict()
		}
		var sealed cryptostore.Value
		if err := json.Unmarshal([]byte(encoded), &sealed); err != nil {
			return auth.User{}, temporaryCredential{}, err
		}
		plaintext, err := s.cipher.Open(s.localCredentialContext(user.ID), sealed)
		if err != nil {
			return auth.User{}, temporaryCredential{}, err
		}
		defer clear(plaintext)
		var credential temporaryCredential
		err = json.Unmarshal(plaintext, &credential)
		return user, credential, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return auth.User{}, temporaryCredential{}, err
	}
	user.ID = uuid.NewString()
	password, hash, err := identity.NewTemporaryPassword()
	if err != nil {
		return auth.User{}, temporaryCredential{}, err
	}
	credential := temporaryCredential{Password: password, Hash: hash}
	plaintext, err := json.Marshal(credential)
	if err != nil {
		return auth.User{}, temporaryCredential{}, err
	}
	defer clear(plaintext)
	sealed, err := s.cipher.Seal(s.localCredentialContext(user.ID), plaintext)
	if err != nil {
		return auth.User{}, temporaryCredential{}, err
	}
	value, err := json.Marshal(sealed)
	if err != nil {
		return auth.User{}, temporaryCredential{}, err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO local_user_creations(email,user_id,role,credential_json) VALUES($1,$2,$3,$4)", email, user.ID, role, string(value))
	return user, credential, err
}

func (s *ControllerState) finishLocalUser(ctx context.Context, r *http.Request, store *auth.Store, user auth.User, actorID string, now time.Time) (auth.User, error) {
	token, err := web.SessionToken(r)
	if err != nil {
		return auth.User{}, err
	}
	var created auth.User
	err = database.Transaction(ctx, s.DB, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "UPDATE installations SET version=version WHERE singleton=1"); err != nil {
			return err
		}
		actor, err := store.Principal(ctx, tx, token)
		if err != nil {
			return err
		}
		if actor.User.ID != actorID || !auth.Authorize(actor, "user.create", auth.Resource{}, nil, now).Allowed {
			return web.Problem{Status: 403, Code: "auth.forbidden", Title: "This action is not permitted"}
		}
		// Only one transaction can claim delivery for this reservation.
		result, err := tx.ExecContext(ctx, "DELETE FROM local_user_creations WHERE email=$1 AND user_id=$2", user.PrimaryEmail, user.ID)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if count != 1 {
			return localUserConflict()
		}
		created, err = store.CreateUser(ctx, tx, user, actorID, now)
		if err != nil {
			return err
		}
		_, err = audit.Append(ctx, tx, audit.Event{OccurredAt: now, ActorType: "user", ActorID: actorID, Action: "user.create",
			ResourceType: "user", ResourceID: user.ID, Outcome: "allowed", ReasonCode: "local_account_created"})
		return err
	})
	return created, err
}

func (s *ControllerState) localCredentialContext(userID string) cryptostore.Context {
	return cryptostore.Context{KeyVersion: 1, RecordType: "local_user_creation", Scope: "installation",
		OwnerID: s.Installation.InstallationID, RecordID: userID, Name: "temporary_credential"}
}

func localUserConflict() web.Problem {
	return web.Problem{Status: 409, Code: "user.local_account_conflict", Title: "A local account or pending creation already uses this email"}
}

func localUserUnavailable() web.Problem {
	return web.Problem{Status: 503, Code: "user.local_identity_unavailable", Title: "Local account creation could not be completed", Retryable: true}
}
