package secret

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/jflatten/ShiftBox/internal/cryptostore"
)

// ResolveWorkspace derives current owner and template access for a verified
// workspace generation. The caller must authenticate the guest in the same
// transaction. A value that fails authentication never falls through.
func (s *Service) ResolveWorkspace(ctx context.Context, tx *sql.Tx, workspace string, generation int64, name string) ([]byte, string, error) {
	if !ValidName(name) {
		return nil, "", ErrInvalidKey
	}
	scopes, err := workspaceSecretScopes(ctx, tx, workspace, generation)
	if err != nil {
		return nil, "", err
	}
	for _, scope := range scopes {
		var id, raw string
		var version int
		err := tx.QueryRowContext(ctx, `SELECT id,ciphertext_json,key_version FROM secret_values
			WHERE scope_type=$1 AND scope_id=$2 AND name=$3`, scope.Type, scope.ID, name).Scan(&id, &raw, &version)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, "", err
		}
		var value cryptostore.Value
		if err := json.Unmarshal([]byte(raw), &value); err != nil {
			return nil, "", errors.New("invalid protected secret")
		}
		plaintext, err := s.cipher.Open(binding(scope, id, name, version), value)
		return plaintext, scope.Type, err
	}
	return nil, "", sql.ErrNoRows
}

// Both value resolution and key listing use the same live workspace authority.
func workspaceSecretScopes(ctx context.Context, tx *sql.Tx, workspace string, generation int64) ([]Scope, error) {
	var installation, owner, template string
	var templateAllowed bool
	// Archived templates prevent new launches. Existing workspaces keep
	// their project-secret context until the underlying access is revoked.
	err := tx.QueryRowContext(ctx, `SELECT w.installation_id,w.owner_user_id,w.template_id,EXISTS(
		SELECT 1 FROM project_templates t WHERE t.id=w.template_id
		  AND (t.owner_user_id=w.owner_user_id OR u.role='administrator' OR EXISTS (
		    SELECT 1 FROM template_grants g WHERE g.template_id=t.id AND g.user_id=u.id AND g.can_launch=1 AND g.revoked_at IS NULL
		  ))
		) FROM workspaces w JOIN installations i ON i.id=w.installation_id JOIN users u ON u.id=w.owner_user_id
		WHERE w.id=$1 AND w.generation=$2 AND w.provider_instance_id IS NOT NULL
		  AND w.desired_state='running' AND w.observed_state='running'
		  AND i.state='active' AND u.membership_state='active'`, workspace, generation).
		Scan(&installation, &owner, &template, &templateAllowed)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrWorkspaceDenied
	}
	if err != nil {
		return nil, err
	}
	scopes := []Scope{{"user", owner}}
	if templateAllowed {
		scopes = append(scopes, Scope{"template", template})
	}
	scopes = append(scopes, Scope{"organization", installation})
	return scopes, nil
}
