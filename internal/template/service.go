package template

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

var (
	// ErrInvalidInput indicates invalid template fields.
	ErrInvalidInput = errors.New("invalid template fields")
	// ErrUnavailable indicates an unavailable launch configuration.
	ErrUnavailable = errors.New("template launch configuration unavailable")
	// ErrRequestConflict indicates a request key reused for a different change.
	ErrRequestConflict = errors.New("template request key conflicts with an earlier change")
	// ErrVersionMismatch indicates a superseded template version.
	ErrVersionMismatch = errors.New("template version changed")
)

// Input describes a template and its pinned launch configuration.
type Input struct {
	Name             string   `json:"name"`
	RepositoryURL    string   `json:"repositoryUrl"`
	RepositoryRef    string   `json:"repositoryRef,omitempty"`
	DevcontainerPath string   `json:"devcontainerPath"`
	ProfileID        string   `json:"resourceProfileId"`
	ArtifactID       string   `json:"platformArtifactId"`
	NetworkPolicyID  string   `json:"networkPolicyId,omitempty"`
	SecretNames      []string `json:"secretNames"`
}

// View represents the current immutable revision and mutable template identity.
type View struct {
	Input
	ID              string `json:"id"`
	OwnerID         string `json:"ownerId"`
	State           string `json:"state"`
	CurrentRevision int64  `json:"currentRevision"`
	Version         int64  `json:"version"`
}

// SaveIntent identifies a creation or revision admitted in an authority transaction.
// An empty TemplateID creates a template with NewID.
type SaveIntent struct {
	Actor, Key, TemplateID, NewID string
	Version                       int64
	Input                         Input
}

// Save writes an immutable revision and records its result for idempotent retries.
func Save(ctx context.Context, tx *sql.Tx, intent SaveIntent, now time.Time) (View, error) {
	input := intent.Input
	input.Name = strings.TrimSpace(input.Name)
	if input.DevcontainerPath == "" {
		input.DevcontainerPath = ".devcontainer/devcontainer.json"
	}
	if input.SecretNames == nil {
		input.SecretNames = []string{}
	}
	if !input.valid() {
		return View{}, ErrInvalidInput
	}
	encoded, err := json.Marshal(struct {
		TemplateID string
		Version    int64
		Input
	}{intent.TemplateID, intent.Version, input})
	if err != nil {
		return View{}, err
	}
	hash := sha256.Sum256(encoded)
	digest := hex.EncodeToString(hash[:])

	// Serialize with other installation mutations before checking the replay record.
	if _, err := tx.ExecContext(ctx, "UPDATE installations SET version=version WHERE singleton=1 AND state='active'"); err != nil {
		return View{}, err
	}
	var savedDigest, response string
	err = tx.QueryRowContext(ctx, "SELECT request_digest,response_json FROM template_requests WHERE actor_id=$1 AND request_key=$2",
		intent.Actor, intent.Key).Scan(&savedDigest, &response)
	if err == nil {
		if savedDigest != digest {
			return View{}, ErrRequestConflict
		}
		var view View
		err := json.Unmarshal([]byte(response), &view)
		return view, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return View{}, err
	}

	view := View{Input: input, ID: intent.NewID, OwnerID: intent.Actor, State: "active", CurrentRevision: 1, Version: 1}
	if intent.TemplateID != "" {
		view.ID = intent.TemplateID
		err := tx.QueryRowContext(ctx, `SELECT owner_user_id,state,current_revision,version FROM project_templates WHERE id=$1`, view.ID).
			Scan(&view.OwnerID, &view.State, &view.CurrentRevision, &view.Version)
		if err != nil {
			return View{}, err
		}
		if view.Version != intent.Version {
			return View{}, ErrVersionMismatch
		}
		view.CurrentRevision++
		view.Version++
	}
	if err := saveRevision(ctx, tx, intent, view, now); err != nil {
		return View{}, err
	}
	encoded, err = json.Marshal(view)
	if err != nil {
		return View{}, err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO template_requests(actor_id,request_key,request_digest,response_json) VALUES($1,$2,$3,$4)",
		intent.Actor, intent.Key, digest, string(encoded))
	return view, err
}

func saveRevision(ctx context.Context, tx *sql.Tx, intent SaveIntent, view View, now time.Time) error {
	var available bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM resource_profiles p CROSS JOIN platform_artifacts a
		WHERE p.id=$1 AND p.enabled=1 AND a.id=$2 AND a.state='available'
		AND ($3='' OR EXISTS (SELECT 1 FROM network_policies WHERE id=$3 AND enabled=1)))`,
		view.ProfileID, view.ArtifactID, view.NetworkPolicyID).Scan(&available)
	if err != nil {
		return err
	}
	if !available {
		return ErrUnavailable
	}
	stamp := now.UTC().Format(time.RFC3339Nano)
	if intent.TemplateID == "" {
		_, err := tx.ExecContext(ctx, `INSERT INTO project_templates(id,owner_user_id,name,state,current_revision,created_at,updated_at,version)
			VALUES($1,$2,$3,'active',1,$4,$4,1)`, view.ID, intent.Actor, view.Name, stamp)
		if err != nil {
			return err
		}
	}
	names, err := json.Marshal(view.SecretNames)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO template_revisions(template_id,revision,repository_url,repository_ref,devcontainer_path,
		resource_profile_id,platform_artifact_id,network_policy_id,secret_names_json,created_by,created_at)
		VALUES($1,$2,$3,NULLIF($4,''),$5,$6,$7,NULLIF($8,''),$9,$10,$11)`, view.ID, view.CurrentRevision,
		view.RepositoryURL, view.RepositoryRef, view.DevcontainerPath, view.ProfileID, view.ArtifactID, view.NetworkPolicyID, string(names), intent.Actor, stamp); err != nil {
		return err
	}
	if intent.TemplateID == "" {
		return nil
	}
	result, err := tx.ExecContext(ctx, `UPDATE project_templates SET name=$1,current_revision=$2,updated_at=$3,version=$4
		WHERE id=$5 AND version=$6`, view.Name, view.CurrentRevision, stamp, view.Version, view.ID, intent.Version)
	if err != nil {
		return err
	}
	updated, err := result.RowsAffected()
	if err == nil && updated != 1 {
		return ErrVersionMismatch
	}
	return err
}

func (input Input) valid() bool {
	if input.Name == "" || utf8.RuneCountInString(input.Name) > 120 || strings.ContainsRune(input.Name, 0) ||
		len(input.RepositoryURL) > 2048 || len(input.DevcontainerPath) > 2048 ||
		!ValidSource(input.RepositoryURL, input.DevcontainerPath) || !ValidRepositoryRef(input.RepositoryRef) {
		return false
	}
	for _, name := range input.SecretNames {
		if strings.TrimSpace(name) == "" || strings.ContainsAny(name, "\x00\r\n") {
			return false
		}
	}
	return true
}
