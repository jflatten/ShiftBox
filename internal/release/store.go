package release

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jflatten/ShiftBox/internal/database"
)

// ErrCatalogConflict indicates that the requested catalog transition cannot
// apply to the current release or compatibility policy.
var ErrCatalogConflict = errors.New("platform catalog conflict")

// CatalogEntry is an imported release and its current selection state.
type CatalogEntry struct {
	ID         string
	Artifact   Artifact
	ImportedBy string
	ImportedAt time.Time
	State      string
	IsDefault  bool
	EC2Retired bool
}

// Store persists platform catalog metadata in the selected management database.
type Store struct {
	db *sql.DB
}

// NewStore binds the catalog to the installation database.
func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

// BeginImport records a release awaiting provider and guest validation.
func (s *Store) BeginImport(ctx context.Context, id string, artifact Artifact, actor string, now time.Time) error {
	return insertCatalogArtifact(ctx, s.db, id, artifact, actor, now, "validating", 0)
}

// InsertOfficialStarterInTx records an exact deployment-pinned image for the
// starter profiles. The caller must first verify the image against EC2.
func InsertOfficialStarterInTx(ctx context.Context, tx *sql.Tx, id string, pin OfficialEC2Image, artifact Artifact, actor string, now time.Time) error {
	image := artifact.EC2
	if image == nil || pin.Validate(image.Region, image.AccountID) != nil || !pin.MatchesManifest(artifact) ||
		!pin.MatchesCatalog(*image, artifact.ManifestSHA256) {
		return ErrInvalidArtifact
	}
	return insertCatalogArtifact(ctx, tx, id, artifact, actor, now, "available", 1)
}

func insertCatalogArtifact(ctx context.Context, db interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, id string, artifact Artifact, actor string, now time.Time, state string, isDefault int) error {
	if _, err := uuid.Parse(id); err != nil || !validDigest(artifact.ManifestSHA256) || actor == "" || now.IsZero() {
		return ErrInvalidArtifact
	}
	kind := "utm"
	var ec2Image []byte
	var family string
	var imageCreatedAt int64
	var digests map[string]string
	if artifact.EC2 == nil {
		bundle, inventory, err := manifestInventory(artifact.Manifest)
		if err != nil {
			return err
		}
		if artifact.Bundle != bundle {
			return ErrInvalidArtifact
		}
		digests = inventory
	} else {
		if !validEC2Artifact(artifact) {
			return ErrInvalidArtifact
		}
		var err error
		ec2Image, err = json.Marshal(artifact.EC2)
		if err != nil {
			return err
		}
		kind = "ec2"
		family, imageCreatedAt = artifact.EC2.Family, artifact.EC2.CreatedAtUnixMillis
		digests = map[string]string{"guest": artifact.Manifest.Guest.SHA256}
	}
	manifest, err := json.Marshal(artifact.Manifest)
	if err != nil {
		return err
	}
	resources, err := json.Marshal(artifact.Manifest.Resources)
	if err != nil {
		return err
	}
	digestJSON, err := json.Marshal(digests)
	if err != nil {
		return err
	}
	provenance, err := json.Marshal(artifact.Manifest.Provenance)
	if err != nil {
		return err
	}
	m := artifact.Manifest
	result, err := db.ExecContext(ctx, `INSERT INTO platform_artifacts
		(id,release_id,architecture,platform_version,guest_release,protocol_version,owned_resources_json,
		digests_json,provenance_json,manifest_json,manifest_sha256,bundle_name,imported_by,imported_at,state,provider_kind,ec2_image_json,
		ec2_family,ec2_created_at,is_default)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20)
		ON CONFLICT DO NOTHING`, id, m.ReleaseID, m.Architecture, m.PlatformVersion, m.Guest.Version,
		m.ProtocolVersion, string(resources), string(digestJSON), string(provenance), string(manifest),
		artifact.ManifestSHA256, artifact.Bundle, actor, now.UTC().Format(time.RFC3339Nano), state, kind, string(ec2Image), family, imageCreatedAt, isDefault)
	return catalogTransition(result, err)
}

// CompleteImport records the outcome of validation against the exact manifest.
func (s *Store) CompleteImport(ctx context.Context, id, digest string, accepted bool) error {
	state := "rejected"
	if accepted {
		state = "available"
	}
	result, err := s.db.ExecContext(ctx, `UPDATE platform_artifacts SET state=$1
		WHERE id=$2 AND manifest_sha256=$3 AND state='validating'`, state, id, digest)
	return catalogTransition(result, err)
}

// SelectDefault selects an available release with a supported guest protocol
// while preserving the previous default if selection fails.
func (s *Store) SelectDefault(ctx context.Context, id string, protocols []string) error {
	return database.Transaction(ctx, s.db, func(tx *sql.Tx) error {
		var protocol string
		err := tx.QueryRowContext(ctx, `SELECT protocol_version FROM platform_artifacts
			WHERE id=$1 AND state='available' AND ec2_retired=0`, id).Scan(&protocol)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrCatalogConflict
		}
		if err != nil {
			return err
		}
		if !slices.Contains(protocols, protocol) {
			return ErrCatalogConflict
		}
		if _, err := tx.ExecContext(ctx, `UPDATE platform_artifacts SET is_default=0 WHERE is_default=1 AND id<>$1`, id); err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `UPDATE platform_artifacts SET is_default=1 WHERE id=$1 AND state='available'`, id)
		return catalogTransition(result, err)
	})
}

const catalogColumns = `id,manifest_json,manifest_sha256,bundle_name,imported_by,imported_at,state,is_default,provider_kind,ec2_image_json,ec2_retired`

// Get reads a release by its pinned catalog ID.
func (s *Store) Get(ctx context.Context, id string) (CatalogEntry, error) {
	return scanCatalogEntry(s.db.QueryRowContext(ctx, `SELECT `+catalogColumns+` FROM platform_artifacts WHERE id=$1`, id))
}

func (s *Store) getByRelease(ctx context.Context, releaseID string) (CatalogEntry, error) {
	return scanCatalogEntry(s.db.QueryRowContext(ctx, `SELECT `+catalogColumns+` FROM platform_artifacts WHERE release_id=$1`, releaseID))
}

// Default reads the selected available release for new workspaces.
func (s *Store) Default(ctx context.Context) (CatalogEntry, error) {
	return scanCatalogEntry(s.db.QueryRowContext(ctx, `SELECT `+catalogColumns+` FROM platform_artifacts WHERE is_default=1`))
}

func scanCatalogEntry(row *sql.Row) (CatalogEntry, error) {
	var entry CatalogEntry
	var manifest, stamp, kind, ami string
	var selected, retired int
	if err := row.Scan(&entry.ID, &manifest, &entry.Artifact.ManifestSHA256, &entry.Artifact.Bundle,
		&entry.ImportedBy, &stamp, &entry.State, &selected, &kind, &ami, &retired); err != nil {
		return CatalogEntry{}, err
	}
	if err := json.Unmarshal([]byte(manifest), &entry.Artifact.Manifest); err != nil {
		return CatalogEntry{}, fmt.Errorf("read stored platform manifest: %w", err)
	}
	if kind == "ec2" {
		image := new(EC2Image)
		if err := json.Unmarshal([]byte(ami), image); err != nil {
			return CatalogEntry{}, ErrInvalidArtifact
		}
		entry.Artifact.EC2 = image
		if !validEC2Artifact(entry.Artifact) {
			return CatalogEntry{}, ErrInvalidArtifact
		}
	}
	var err error
	entry.ImportedAt, err = time.Parse(time.RFC3339Nano, stamp)
	entry.IsDefault = selected == 1
	entry.EC2Retired = retired == 1
	return entry, err
}

func catalogTransition(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrCatalogConflict
	}
	return nil
}
