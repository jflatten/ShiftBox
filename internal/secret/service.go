package secret

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"time"

	"github.com/jflatten/ShiftBox/internal/audit"
	"github.com/jflatten/ShiftBox/internal/cryptostore"
)

var (
	// ErrInvalidKey indicates an unsupported secret scope or name.
	ErrInvalidKey = errors.New("invalid secret scope or name")
	// ErrWorkspaceDenied indicates the workspace cannot receive secrets.
	ErrWorkspaceDenied = errors.New("workspace secret access denied")
)

var keyName = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,126}$`)

// Scope identifies the exact owner of an application secret.
type Scope struct{ Type, ID string }

func (s Scope) valid() bool {
	return s.ID != "" && (s.Type == "user" || s.Type == "template" || s.Type == "organization")
}

// ValidName checks the normalized name accepted by the management and guest APIs.
func ValidName(name string) bool { return keyName.MatchString(name) }

// Metadata is the management view of a secret. It contains no protected value.
type Metadata struct {
	Scope         string `json:"scope"`
	ScopeID       string `json:"scopeId"`
	Name          string `json:"name"`
	ExplicitEmpty bool   `json:"explicitEmpty"`
	FallbackScope string `json:"fallbackScope,omitempty"`
	UpdatedAt     string `json:"updatedAt"`
}

// Service protects application secret values with the installation key.
type Service struct{ cipher *cryptostore.Store }

// NewService binds the installation key used for secret storage.
func NewService(cipher *cryptostore.Store) *Service { return &Service{cipher: cipher} }

// Set writes an encrypted value after the caller authorizes its exact scope.
func (s *Service) Set(ctx context.Context, tx *sql.Tx, scope Scope, name string, value []byte, actor string, now time.Time) error {
	if !scope.valid() || !ValidName(name) {
		return ErrInvalidKey
	}
	if err := s.put(ctx, tx, scope, name, value, actor, now); err != nil {
		return err
	}
	return recordChange(ctx, tx, scope, actor, "value_saved", now)
}

// Delete removes an override after the caller authorizes and acknowledges the change.
func (s *Service) Delete(ctx context.Context, tx *sql.Tx, scope Scope, name, actor string, now time.Time) error {
	if !scope.valid() || !ValidName(name) {
		return ErrInvalidKey
	}
	result, err := tx.ExecContext(ctx, "DELETE FROM secret_values WHERE scope_type=$1 AND scope_id=$2 AND name=$3", scope.Type, scope.ID, name)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil || count == 0 {
		return err
	}
	return recordChange(ctx, tx, scope, actor, "override_deleted", now)
}

func recordChange(ctx context.Context, tx *sql.Tx, scope Scope, actor, reason string, now time.Time) error {
	_, err := audit.Append(ctx, tx, audit.Event{
		OccurredAt: now, ActorType: "user", ActorID: actor, Action: "secret.manage_" + scope.Type,
		ResourceType: scope.Type, ResourceID: scope.ID, Outcome: "allowed", ReasonCode: reason,
	})
	return err
}
