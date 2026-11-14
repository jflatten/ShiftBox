package template

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io/fs"
	"net/url"
	"strings"

	"github.com/jflatten/ShiftBox/internal/auth"
)

// LaunchRevision combines pinned template settings with current template
// ownership and the requesting user's current launch grant.
type LaunchRevision struct {
	Resource         auth.Resource
	RepositoryURL    string
	RepositoryRef    string
	DevcontainerPath string
	ProfileID        string
	ArtifactID       string
	NetworkPolicyID  string
	SecretNames      []string
}

// ResolveLaunchRevision reads the specified revision in the caller's authority
// transaction. The caller applies the launch permission to Resource.
func ResolveLaunchRevision(ctx context.Context, tx *sql.Tx, id string, revision int64, user string) (LaunchRevision, error) {
	result := LaunchRevision{Resource: auth.Resource{Type: "template", ID: id}}
	var names string
	err := tx.QueryRowContext(ctx, `SELECT t.owner_user_id,t.state,r.repository_url,COALESCE(r.repository_ref,''),
		r.devcontainer_path,r.resource_profile_id,r.platform_artifact_id,COALESCE(r.network_policy_id,''),r.secret_names_json,
		EXISTS(SELECT 1 FROM template_grants g
			WHERE g.template_id=t.id AND g.user_id=$3 AND g.can_launch=1 AND g.revoked_at IS NULL)
		FROM project_templates t JOIN template_revisions r ON r.template_id=t.id
		WHERE t.id=$1 AND r.revision=$2`, id, revision, user).
		Scan(&result.Resource.OwnerID, &result.Resource.State, &result.RepositoryURL, &result.RepositoryRef,
			&result.DevcontainerPath, &result.ProfileID, &result.ArtifactID, &result.NetworkPolicyID, &names, &result.Resource.LaunchGranted)
	if err != nil {
		return LaunchRevision{}, err
	}
	if !ValidSource(result.RepositoryURL, result.DevcontainerPath) || !ValidRepositoryRef(result.RepositoryRef) ||
		json.Unmarshal([]byte(names), &result.SecretNames) != nil || result.SecretNames == nil {
		return LaunchRevision{}, errors.New("invalid stored template revision")
	}
	return result, nil
}

// ValidSource checks the repository and configuration path accepted in MVP.
func ValidSource(repository, devcontainer string) bool {
	repo, err := url.Parse(repository)
	if err != nil || repo.Scheme != "https" || !strings.EqualFold(repo.Host, "github.com") ||
		repo.User != nil || repo.RawQuery != "" || repo.ForceQuery || repo.Fragment != "" {
		return false
	}
	parts := strings.Split(strings.Trim(repo.Path, "/"), "/")
	if len(parts) != 2 || !fs.ValidPath(strings.Join(parts, "/")) || strings.ContainsAny(repo.Path, "\\ \r\n\t") {
		return false
	}
	return devcontainer != "." && fs.ValidPath(devcontainer) && !strings.ContainsAny(devcontainer, "\\\x00")
}

// ValidRepositoryRef accepts refs supported by the guest checkout.
func ValidRepositoryRef(ref string) bool {
	if ref == "" {
		return true
	}
	name := strings.TrimPrefix(strings.TrimPrefix(ref, "refs/heads/"), "refs/tags/")
	return name != "" && len(ref) <= 255 && !strings.HasPrefix(name, "-") &&
		!strings.ContainsAny(name, " \r\n\t\x00:~^?*[\\") &&
		!strings.Contains(name, "..") && !strings.Contains(name, "@{") &&
		(name != ref || !strings.HasPrefix(name, "refs/"))
}

// List reads a page of templates visible to the caller and their current revisions.
func List(ctx context.Context, tx *sql.Tx, actor string, admin bool, state, cursor string) ([]View, *string, error) {
	const limit = 50
	rows, err := tx.QueryContext(ctx, `SELECT t.id,t.owner_user_id,t.name,t.state,t.current_revision,t.version,
		r.repository_url,COALESCE(r.repository_ref,''),r.devcontainer_path,r.resource_profile_id,
		r.platform_artifact_id,COALESCE(r.network_policy_id,''),r.secret_names_json
		FROM project_templates t JOIN template_revisions r ON r.template_id=t.id AND r.revision=t.current_revision
		WHERE t.id>$1 AND t.state=$2 AND ($3 OR (t.state='active' AND (t.owner_user_id=$4 OR EXISTS (
		  SELECT 1 FROM template_grants g WHERE g.template_id=t.id AND g.user_id=$4 AND g.can_launch=1 AND g.revoked_at IS NULL
		))))
		ORDER BY t.id LIMIT $5`, cursor, state, admin, actor, limit+1)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	items := make([]View, 0, limit)
	for rows.Next() {
		if len(items) == limit {
			return items, &items[limit-1].ID, nil
		}
		var view View
		var names string
		err := rows.Scan(&view.ID, &view.OwnerID, &view.Name, &view.State, &view.CurrentRevision, &view.Version,
			&view.RepositoryURL, &view.RepositoryRef, &view.DevcontainerPath, &view.ProfileID, &view.ArtifactID, &view.NetworkPolicyID, &names)
		if err != nil {
			return nil, nil, err
		}
		if err := json.Unmarshal([]byte(names), &view.SecretNames); err != nil {
			return nil, nil, err
		}
		items = append(items, view)
	}
	return items, nil, rows.Err()
}
