package template

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"
)

// Update changes template metadata while retaining ownership, revisions and grants.
func Update(ctx context.Context, tx *sql.Tx, id string, version int64, name, state *string, now time.Time) (View, error) {
	if name != nil {
		value := strings.TrimSpace(*name)
		name = &value
	}
	stamp := now.UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `UPDATE project_templates SET name=COALESCE($1,name),state=COALESCE($2,state),
		archived_at=CASE WHEN $2='archived' THEN COALESCE(archived_at,$3) WHEN $2='active' THEN NULL ELSE archived_at END,
		updated_at=$3,version=version+1 WHERE id=$4 AND version=$5`, name, state, stamp, id, version)
	if err != nil {
		return View{}, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return View{}, err
	}
	if count != 1 {
		return View{}, ErrVersionMismatch
	}
	return scanView(tx.QueryRowContext(ctx, templateViewQuery+" WHERE t.id=$1", id))
}

const templateViewQuery = `SELECT t.id,t.owner_user_id,t.name,t.state,t.current_revision,t.version,
		r.repository_url,COALESCE(r.repository_ref,''),r.devcontainer_path,r.resource_profile_id,
		CASE WHEN a.provider_kind='ec2' THEN COALESCE(p.platform_artifact_id,'') ELSE r.platform_artifact_id END,COALESCE(r.network_policy_id,''),r.secret_names_json,
		COALESCE(NULLIF(u.display_name,''),u.primary_email),p.name,CASE WHEN a.provider_kind='ec2' AND p.platform_artifact_id IS NULL THEN '' ELSE a.release_id END,a.provider_kind
		FROM project_templates t JOIN template_revisions r ON r.template_id=t.id AND r.revision=t.current_revision
		JOIN users u ON u.id=t.owner_user_id
		JOIN resource_profiles p ON p.id=r.resource_profile_id
		JOIN platform_artifacts a ON a.id=COALESCE(p.platform_artifact_id,r.platform_artifact_id)`

func scanView(row interface{ Scan(...any) error }) (View, error) {
	var view View
	var names string
	err := row.Scan(&view.ID, &view.OwnerID, &view.Name, &view.State, &view.CurrentRevision, &view.Version,
		&view.RepositoryURL, &view.RepositoryRef, &view.DevcontainerPath, &view.ProfileID, &view.ArtifactID, &view.NetworkPolicyID, &names,
		&view.OwnerName, &view.ProfileName, &view.ImageName, &view.ProviderKind)
	if err != nil {
		return View{}, err
	}
	err = json.Unmarshal([]byte(names), &view.SecretNames)
	return view, err
}
