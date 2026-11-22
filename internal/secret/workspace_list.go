package secret

import (
	"context"
	"database/sql"
	"slices"
	"strings"
)

// Key identifies an effective secret without exposing its protected value.
// Project is the display scope for secrets stored on a project template.
type Key struct {
	Name  string `json:"name"`
	Scope string `json:"scope"`
}

// ListWorkspace returns sorted effective keys under the same current authority
// as ResolveWorkspace. It selects names only and never reads or opens ciphertext.
func (s *Service) ListWorkspace(ctx context.Context, tx *sql.Tx, workspace string, generation int64) ([]Key, error) {
	scopes, err := workspaceSecretScopes(ctx, tx, workspace, generation)
	if err != nil {
		return nil, err
	}
	winners := make(map[string]string)
	for _, scope := range scopes {
		rows, err := tx.QueryContext(ctx, "SELECT name FROM secret_values WHERE scope_type=$1 AND scope_id=$2", scope.Type, scope.ID)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				rows.Close()
				return nil, err
			}
			if _, found := winners[name]; !found {
				label := scope.Type
				if label == "template" {
					label = "project"
				}
				winners[name] = label
			}
		}
		err = rows.Err()
		closeErr := rows.Close()
		if err != nil {
			return nil, err
		}
		if closeErr != nil {
			return nil, closeErr
		}
	}
	keys := make([]Key, 0, len(winners))
	for name, scope := range winners {
		keys = append(keys, Key{Name: name, Scope: scope})
	}
	slices.SortFunc(keys, func(a, b Key) int { return strings.Compare(a.Name, b.Name) })
	return keys, nil
}
