package imagecache

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"time"

	"github.com/jflatten/ShiftBox/internal/database"
)

// StaleWarning is the workspace message for a complete cache hit during an outage.
const StaleWarning = "The upstream registry could not be contacted. Using cached version."

// ImageFreshness records startup resolution separately from later verification.
type ImageFreshness struct {
	Origin         string    `json:"origin"`
	Repository     string    `json:"repository"`
	Tag            string    `json:"tag"`
	ResolvedDigest string    `json:"resolvedDigest"`
	VerifiedDigest string    `json:"verifiedDigest,omitempty"`
	VerifiedAt     time.Time `json:"verifiedAt,omitempty"`
	WarningActive  bool      `json:"warningActive"`
}

// WorkspaceFreshness is the bounded cache evidence for the active workspace generation.
type WorkspaceFreshness struct {
	References      []ImageFreshness `json:"references,omitempty"`
	WarningActive   bool             `json:"warningActive"`
	WarningMessage  string           `json:"warningMessage,omitempty"`
	UpdateAvailable bool             `json:"updateAvailable"`
	OperationID     string           `json:"operationId,omitempty"`
	Bytes           PullStats        `json:"bytes"`
}

func (f *WorkspaceFreshness) record(o Observation, duringStartup bool) {
	f.Bytes.add(o.PullStats)
	if o.Reference == "" || !o.Stale && !o.Verified {
		return
	}
	var image *ImageFreshness
	for i := range f.References {
		current := &f.References[i]
		if current.Origin == o.Origin && current.Repository == o.Repository && current.Tag == o.Reference {
			image = current
			break
		}
	}
	if image == nil {
		if len(f.References) >= 32 {
			// Do not discard existing warnings to resolve unrelated images.
			return
		}
		f.References = append(f.References, ImageFreshness{Origin: o.Origin, Repository: o.Repository, Tag: o.Reference})
		image = &f.References[len(f.References)-1]
	}
	if duringStartup || image.ResolvedDigest == "" {
		image.ResolvedDigest = o.Digest
	}
	image.WarningActive = o.Stale
	if o.Verified {
		image.VerifiedDigest = o.Digest
		image.VerifiedAt = time.Now().UTC()
	}
	f.WarningActive, f.UpdateAvailable, f.WarningMessage = false, false, ""
	for _, ref := range f.References {
		f.WarningActive = f.WarningActive || ref.WarningActive
		f.UpdateAvailable = f.UpdateAvailable || !ref.WarningActive && ref.VerifiedDigest != "" &&
			ref.ResolvedDigest != "" && ref.VerifiedDigest != ref.ResolvedDigest
	}
	if f.WarningActive {
		f.WarningMessage = StaleWarning
	}
}

type freshnessQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// ReadWorkspaceFreshness returns recorded evidence for the current workspace generation.
func ReadWorkspaceFreshness(ctx context.Context, db freshnessQueryer, workspaceID string, generation int64) (*WorkspaceFreshness, error) {
	var raw string
	err := db.QueryRowContext(ctx, `SELECT image_cache_json FROM workspaces
		WHERE id=$1 AND generation=$2 AND image_cache_generation=$2`, workspaceID, generation).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) || err == nil && raw == "" {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var result WorkspaceFreshness
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// RecordWorkspaceFreshness fences cache observations by workspace generation.
// It preserves the last startup digest during background verification.
func RecordWorkspaceFreshness(ctx context.Context, db *sql.DB, workspaceID string, generation int64, observation Observation, background bool) error {
	if observation.Reference == "" && observation.PullStats == (PullStats{}) {
		return nil
	}
	return database.Transaction(ctx, db, func(tx *sql.Tx) error {
		row, err := tx.ExecContext(ctx, `UPDATE workspaces SET image_cache_json=image_cache_json
			WHERE id=$1 AND generation=$2 AND desired_state='running'`, workspaceID, generation)
		if err != nil {
			return err
		}
		count, err := row.RowsAffected()
		if err != nil || count == 0 {
			return err
		}
		current, err := ReadWorkspaceFreshness(ctx, tx, workspaceID, generation)
		if err != nil {
			return err
		}
		if current == nil {
			current = &WorkspaceFreshness{}
		}
		var operationID string
		if !background {
			err = tx.QueryRowContext(ctx, `SELECT id FROM operations WHERE workspace_id=$1 AND generation=$2
				AND type IN ('provision','start','rebuild') AND status IN ('pending','running','waiting')
				ORDER BY created_at DESC,id DESC LIMIT 1`, workspaceID, generation).Scan(&operationID)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if operationID != "" && current.OperationID != operationID {
				current = &WorkspaceFreshness{OperationID: operationID}
			}
		}
		current.record(observation, operationID != "")
		encoded, err := json.Marshal(current)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE workspaces SET image_cache_json=$1,image_cache_generation=$2
			WHERE id=$3 AND generation=$2`, string(encoded), generation, workspaceID)
		return err
	})
}

// VerifyReference checks a stored tag through the same guarded upstream path.
func (c *Cache) VerifyReference(ctx context.Context, image ImageFreshness) (Observation, error) {
	endpoint := &url.URL{Path: "/v2/" + image.Repository + "/manifests/" + image.Tag,
		RawQuery: url.Values{"ns": []string{image.Origin}}.Encode()}
	origin, repo, ok := pullTarget(endpoint)
	if !ok {
		return Observation{}, errPublicDestination
	}
	request := &pullRequest{remote: remoteRegistry{origin: origin, repository: repo, client: c.client}}
	remote := &repository{cache: c, request: request}
	_, desc, err := remote.fetchManifest(ctx, image.Tag)
	if err != nil {
		return Observation{}, err
	}
	return Observation{Origin: image.Origin, Repository: repo, Reference: image.Tag,
		Digest: desc.Digest.String(), Verified: true, PullStats: request.observation.PullStats}, nil
}
