package app

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/jflatten/ShiftBox/internal/audit"
	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/identity"
	"github.com/jflatten/ShiftBox/internal/proxy"
)

var errMembershipPending = errors.New("membership approval is pending")

// issueExternalSession uses only the verified stable binding to resolve a user.
// First sign-in records a pending membership without issuing an access cookie.
func issueExternalSession(
	ctx context.Context,
	db *sql.DB,
	installation string,
	verified identity.VerifiedIdentity,
	now time.Time,
	newID func() string,
) (auth.Session, string, error) {
	if verified.ConnectorID == "local" || verified.AuthorizedAt.IsZero() || verified.AuthorizedAt.After(now) {
		return auth.Session{}, "", identity.ErrInvalidIdentity
	}
	store := auth.NewStore(db)
	var session auth.Session
	var token string
	var pending bool
	err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
		// Serialize first login with approval and concurrent requests for the
		// same issuer, subject and connector.
		if err := lockExternalSessionWrite(ctx, tx); err != nil {
			return err
		}
		var interval int64
		if err := tx.QueryRowContext(ctx, "SELECT external_auth_interval_seconds FROM installations WHERE id=$1 AND state='active'", installation).Scan(&interval); err != nil {
			return err
		}
		user, err := store.Identity(ctx, tx, verified.Issuer, verified.Subject, verified.ConnectorID)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			pending = true
			return requestExternalMembership(ctx, tx, store, verified, now, newID)
		case err != nil:
			return err
		case user.CredentialMode != "external":
			return identity.ErrInvalidIdentity
		case user.MembershipState == "pending":
			pending = true
			return nil
		case user.MembershipState != "active":
			return identity.ErrInvalidIdentity
		}
		if interval <= 0 || interval > int64((1<<63-1)/time.Second) || !now.Before(verified.AuthorizedAt.Add(time.Duration(interval)*time.Second)) {
			return identity.ErrInvalidIdentity
		}
		session, token, err = saveExternalSession(ctx, tx, store, user.ID, newID(), verified.AuthorizedAt, now)
		return err
	})
	if err != nil {
		return auth.Session{}, "", err
	}
	if pending {
		return auth.Session{}, "", errMembershipPending
	}
	return session, token, nil
}

func reauthorizeExternalSession(
	ctx context.Context,
	db *sql.DB,
	registry *proxy.Registry,
	sessionID string,
	verified identity.VerifiedIdentity,
	now time.Time,
	newID func() string,
) (auth.Session, string, error) {
	store := auth.NewStore(db)
	var session auth.Session
	var token string
	err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
		if err := lockExternalSessionWrite(ctx, tx); err != nil {
			return err
		}
		principal, err := store.PrincipalForSession(ctx, tx, sessionID)
		if errors.Is(err, sql.ErrNoRows) {
			return identity.ErrInvalidIdentity
		}
		if err != nil {
			return err
		}
		if !auth.Authorize(principal, "session.external_reauthorize", auth.Resource{}, nil, now).Allowed ||
			verified.AuthorizedAt.IsZero() || verified.AuthorizedAt.After(now) ||
			!now.Before(verified.AuthorizedAt.Add(principal.ExternalAuthInterval)) ||
			(principal.Session.ExternalAuthorizedAt != nil && !verified.AuthorizedAt.After(*principal.Session.ExternalAuthorizedAt)) {
			return identity.ErrInvalidIdentity
		}
		user, err := store.Identity(ctx, tx, verified.Issuer, verified.Subject, verified.ConnectorID)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && user.ID != principal.User.ID) {
			return identity.ErrInvalidIdentity
		}
		if err != nil {
			return err
		}
		if err := store.RevokeSession(ctx, tx, sessionID, now, "external_reauthorization"); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE origin_grants SET revoked_at=$1 WHERE session_id=$2 AND revoked_at IS NULL",
			now.UTC().Format(time.RFC3339Nano), sessionID); err != nil {
			return err
		}
		session, token, err = saveExternalSession(ctx, tx, store, user.ID, newID(), verified.AuthorizedAt, now)
		if err != nil {
			return err
		}
		_, err = audit.Append(ctx, tx, audit.Event{OccurredAt: now, ActorType: "user", ActorID: user.ID,
			Action: "session.external_reauthorize", ResourceType: "user", ResourceID: user.ID, Outcome: "allowed", ReasonCode: "upstream_authorized"})
		return err
	})
	if err != nil {
		return auth.Session{}, "", err
	}
	if err := registry.CloseMatching(func(scope proxy.Scope) bool { return scope.SessionID == sessionID }); err != nil {
		return auth.Session{}, "", err
	}
	return session, token, nil
}

func lockExternalSessionWrite(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, "UPDATE installations SET version=version WHERE singleton=1")
	return err
}

func saveExternalSession(ctx context.Context, tx *sql.Tx, store *auth.Store, userID, sessionID string, authorizedAt, now time.Time) (auth.Session, string, error) {
	session, token, err := auth.NewSession(sessionID, userID, "normal", now, 30*time.Minute)
	if err != nil {
		return auth.Session{}, "", err
	}
	session.ExternalAuthorizedAt = &authorizedAt
	if err := store.SaveSession(ctx, tx, session); err != nil {
		return auth.Session{}, "", err
	}
	_, err = tx.ExecContext(ctx, `UPDATE users SET external_authorized_at=$1
		WHERE id=$2 AND (external_authorized_at IS NULL OR external_authorized_at < $1)`,
		authorizedAt.UTC().Format("2006-01-02T15:04:05.000000000Z"), userID)
	return session, token, err
}

func requestExternalMembership(
	ctx context.Context,
	tx *sql.Tx,
	store *auth.Store,
	verified identity.VerifiedIdentity,
	now time.Time,
	newID func() string,
) error {
	if !verified.EmailVerified {
		return identity.ErrInvalidIdentity
	}
	id := newID()
	user := auth.User{ID: id, PrimaryEmail: verified.Email, Role: "member", MembershipState: "pending", CredentialMode: "external"}
	if verified.Name != "" {
		user.DisplayName = &verified.Name
	}
	user, err := store.CreateUser(ctx, tx, user, id, now)
	if err != nil {
		return err
	}
	if err := store.BindIdentity(ctx, tx, auth.IdentityBinding{ID: newID(), UserID: id, Issuer: verified.Issuer,
		Subject: verified.Subject, ConnectorID: verified.ConnectorID, EmailAtLink: user.PrimaryEmail, LinkedBy: id, LinkedAt: now}); err != nil {
		return err
	}
	_, err = audit.Append(ctx, tx, audit.Event{OccurredAt: now, ActorType: "user", ActorID: id,
		Action: "user.request_membership", ResourceType: "user", ResourceID: id, Outcome: "pending", ReasonCode: "verified_external_identity"})
	return err
}
