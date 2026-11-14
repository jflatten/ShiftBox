package auth

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"net/mail"
	"net/url"
	"strings"
	"time"
)

// ErrInvalidGrant rejects permissions outside the reviewed resource vocabulary.
var ErrInvalidGrant = errors.New("invalid resource grant")

// Store persists membership and identity under caller-owned authorization transactions.
type Store struct{ db *sql.DB }

// NewStore attaches identity persistence to the active application database.
func NewStore(db *sql.DB) *Store { return &Store{db: db} }

func authTime(value time.Time) string { return value.UTC().Format("2006-01-02T15:04:05.000000000Z") }
func nullableTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return authTime(*value)
}

// CreateUser normalizes display/login email without using it to link identities.
func (s *Store) CreateUser(ctx context.Context, tx *sql.Tx, user User, actor string, now time.Time) (User, error) {
	user.PrimaryEmail = strings.ToLower(strings.TrimSpace(user.PrimaryEmail))
	address, err := mail.ParseAddress(user.PrimaryEmail)
	if err != nil || address.Name != "" || address.Address != user.PrimaryEmail {
		return User{}, errors.New("invalid user email")
	}
	user.Version = 1
	change := 0
	if user.MustChangePassword {
		change = 1
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO users(id,primary_email,display_name,role,membership_state,credential_mode,must_change_password,external_authorized_at,created_by,created_at,updated_at,version) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$10,1)`, user.ID, user.PrimaryEmail, user.DisplayName, user.Role, user.MembershipState, user.CredentialMode, change, nullableTime(user.ExternalAuthorizedAt), actor, authTime(now))
	return user, err
}

// User reads current membership rather than trusting a browser session's old role.
func (s *Store) User(ctx context.Context, tx *sql.Tx, id string) (User, error) {
	var user User
	var external sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT id,primary_email,display_name,role,membership_state,credential_mode,must_change_password,external_authorized_at,version FROM users WHERE id=$1`, id).Scan(&user.ID, &user.PrimaryEmail, &user.DisplayName, &user.Role, &user.MembershipState, &user.CredentialMode, &user.MustChangePassword, &external, &user.Version)
	if err != nil {
		return User{}, err
	}
	if external.Valid {
		value, err := time.Parse(time.RFC3339Nano, external.String)
		if err != nil {
			return User{}, err
		}
		user.ExternalAuthorizedAt = &value
	}
	return user, nil
}

// BindIdentity creates an explicit binding; an existing tuple cannot be reassigned.
func (s *Store) BindIdentity(ctx context.Context, tx *sql.Tx, binding IdentityBinding) error {
	issuer, err := url.Parse(binding.Issuer)
	if err != nil || issuer.Scheme != "https" || issuer.Host == "" || issuer.User != nil || issuer.RawQuery != "" || issuer.Fragment != "" || binding.Subject == "" || binding.ConnectorID == "" {
		return errors.New("invalid stable identity binding")
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO identity_bindings(id,user_id,issuer,subject,connector_id,email_at_link,linked_by,linked_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, binding.ID, binding.UserID, binding.Issuer, binding.Subject, binding.ConnectorID, binding.EmailAtLink, binding.LinkedBy, authTime(binding.LinkedAt))
	return err
}

// Identity looks up only the exact upstream tuple, never profile email.
func (s *Store) Identity(ctx context.Context, tx *sql.Tx, issuer, subject, connector string) (User, error) {
	var id string
	if err := tx.QueryRowContext(ctx, `SELECT user_id FROM identity_bindings WHERE issuer=$1 AND subject=$2 AND connector_id=$3`, issuer, subject, connector).Scan(&id); err != nil {
		return User{}, err
	}
	return s.User(ctx, tx, id)
}

// SaveSession accepts only digests, keeping replayable credentials out of persistent state.
func (s *Store) SaveSession(ctx context.Context, tx *sql.Tx, session Session) error {
	for _, digest := range []string{session.TokenDigest, session.CSRFDigest} {
		decoded, err := hex.DecodeString(digest)
		if err != nil || len(decoded) != 32 {
			return errors.New("session requires SHA-256 credential digests")
		}
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO sessions(id,token_digest,user_id,mode,csrf_digest,authenticated_at,external_authorized_at,expires_at,revoked_at,revocation_reason) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, session.ID, session.TokenDigest, session.UserID, session.Mode, session.CSRFDigest, authTime(session.AuthenticatedAt), nullableTime(session.ExternalAuthorizedAt), authTime(session.ExpiresAt), nullableTime(session.RevokedAt), session.RevocationReason)
	return err
}

// Session resolves a raw cookie to its record; callers still apply current authority gates.
func (s *Store) Session(ctx context.Context, tx *sql.Tx, token string) (Session, error) {
	var session Session
	var authenticated, expires string
	var external, revoked, reason sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT id,token_digest,user_id,mode,csrf_digest,authenticated_at,external_authorized_at,expires_at,revoked_at,revocation_reason FROM sessions WHERE token_digest=$1`, TokenDigest(token)).Scan(&session.ID, &session.TokenDigest, &session.UserID, &session.Mode, &session.CSRFDigest, &authenticated, &external, &expires, &revoked, &reason)
	if err != nil {
		return Session{}, err
	}
	session.AuthenticatedAt, err = time.Parse(time.RFC3339Nano, authenticated)
	if err != nil {
		return Session{}, err
	}
	session.ExpiresAt, err = time.Parse(time.RFC3339Nano, expires)
	if err != nil {
		return Session{}, err
	}
	for _, field := range []struct {
		source sql.NullString
		target **time.Time
	}{{external, &session.ExternalAuthorizedAt}, {revoked, &session.RevokedAt}} {
		if field.source.Valid {
			value, err := time.Parse(time.RFC3339Nano, field.source.String)
			if err != nil {
				return Session{}, err
			}
			*field.target = &value
		}
	}
	session.RevocationReason = reason.String
	return session, nil
}

// RevokeSession records invalidation inside the caller's transaction, before connection closure.
func (s *Store) RevokeSession(ctx context.Context, tx *sql.Tx, id string, now time.Time, reason string) error {
	_, err := tx.ExecContext(ctx, `UPDATE sessions SET revoked_at=$1,revocation_reason=$2 WHERE id=$3 AND revoked_at IS NULL`, authTime(now), reason, id)
	return err
}

// Grant stores only reviewed resource-scoped actions; revocation remains explicit.
func (s *Store) Grant(ctx context.Context, tx *sql.Tx, grant ResourceGrant) error {
	if !grantAction(grant.ResourceType, grant.Action) {
		return ErrInvalidGrant
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO resource_grants(id,subject_user_id,resource_type,resource_id,action,granted_by,granted_at,revoked_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, grant.ID, grant.SubjectUserID, grant.ResourceType, grant.ResourceID, grant.Action, grant.GrantedBy, authTime(grant.GrantedAt), nullableTime(grant.RevokedAt))
	return err
}

// ActiveGrants reads current exact-resource permissions in the authorization
// transaction. A grant alone does not bypass membership, role, or audit checks.
func (s *Store) ActiveGrants(ctx context.Context, tx *sql.Tx, subjectID, resourceType, resourceID string) ([]ResourceGrant, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id,subject_user_id,resource_type,resource_id,action,granted_by,granted_at FROM resource_grants WHERE subject_user_id=$1 AND resource_type=$2 AND resource_id=$3 AND revoked_at IS NULL ORDER BY id`, subjectID, resourceType, resourceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var grants []ResourceGrant
	for rows.Next() {
		var grant ResourceGrant
		var granted string
		if err := rows.Scan(&grant.ID, &grant.SubjectUserID, &grant.ResourceType, &grant.ResourceID, &grant.Action, &grant.GrantedBy, &granted); err != nil {
			return nil, err
		}
		grant.GrantedAt, err = time.Parse(time.RFC3339Nano, granted)
		if err != nil {
			return nil, err
		}
		grants = append(grants, grant)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return grants, nil
}

// RevokeGrant preserves the first revocation time in the caller's authorized
// transaction. The caller commits audit evidence here, then closes live access.
func (s *Store) RevokeGrant(ctx context.Context, tx *sql.Tx, id string, now time.Time) error {
	_, err := tx.ExecContext(ctx, `UPDATE resource_grants SET revoked_at=$1 WHERE id=$2 AND revoked_at IS NULL`, authTime(now), id)
	return err
}

func grantAction(kind, action string) bool {
	switch action {
	case "workspace.view", "workspace.start", "workspace.stop", "workspace.rebuild", "workspace.repair", "workspace.delete", "workspace.update", "workspace.update_policy", "workspace.connect_owner", "workspace.connect_admin":
		return kind == "workspace"
	case "template.view", "template.edit", "template.create_revision", "template.share", "template.archive":
		return kind == "template"
	default:
		return false
	}
}
