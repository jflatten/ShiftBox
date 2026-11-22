package secret

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jflatten/ShiftBox/internal/cryptostore"
)

func binding(scope Scope, id, name string, version int) cryptostore.Context {
	return cryptostore.Context{KeyVersion: version, RecordType: "application_secret",
		Scope: scope.Type, OwnerID: scope.ID, RecordID: id, Name: name}
}

func (s *Service) put(ctx context.Context, tx *sql.Tx, scope Scope, name string, value []byte, actor string, now time.Time) error {
	var id string
	err := tx.QueryRowContext(ctx, "SELECT id FROM secret_values WHERE scope_type=$1 AND scope_id=$2 AND name=$3",
		scope.Type, scope.ID, name).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		id = uuid.NewString()
	} else if err != nil {
		return err
	}
	sealed, err := s.cipher.Seal(binding(scope, id, name, 1), value)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(sealed)
	if err != nil {
		return err
	}
	empty := 0
	if len(value) == 0 {
		empty = 1
	}
	stamp := now.UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `INSERT INTO secret_values(id,scope_type,scope_id,name,ciphertext_json,key_version,
		is_explicit_empty,created_by,created_at,updated_at,version)
		VALUES($1,$2,$3,$4,$5,1,$6,$7,$8,$8,1)
		ON CONFLICT(scope_type,scope_id,name) DO UPDATE SET ciphertext_json=excluded.ciphertext_json,
		  key_version=excluded.key_version,is_explicit_empty=excluded.is_explicit_empty,updated_at=excluded.updated_at,
		  version=secret_values.version+1 WHERE secret_values.id=excluded.id`, id, scope.Type, scope.ID, name, string(raw), empty, actor, stamp)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err == nil && count != 1 {
		return errors.New("secret was modified concurrently")
	}
	return err
}

// List returns sorted metadata and organization fallback for the selected scope.
// A personal secret can also have template fallbacks that vary by workspace.
func (s *Service) List(ctx context.Context, tx *sql.Tx, scope Scope, installation string) ([]Metadata, error) {
	if !scope.valid() {
		return nil, ErrInvalidKey
	}
	rows, err := tx.QueryContext(ctx, `SELECT v.name,v.is_explicit_empty,v.updated_at,EXISTS(
		SELECT 1 FROM secret_values o WHERE o.scope_type='organization' AND o.scope_id=$3 AND o.name=v.name
		) FROM secret_values v WHERE v.scope_type=$1 AND v.scope_id=$2 ORDER BY v.name`, scope.Type, scope.ID, installation)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Metadata{}
	for rows.Next() {
		item := Metadata{Scope: scope.Type, ScopeID: scope.ID}
		var empty int
		var fallback bool
		if err := rows.Scan(&item.Name, &empty, &item.UpdatedAt, &fallback); err != nil {
			return nil, err
		}
		item.ExplicitEmpty = empty == 1
		if scope.Type != "organization" && fallback {
			item.FallbackScope = "organization"
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
