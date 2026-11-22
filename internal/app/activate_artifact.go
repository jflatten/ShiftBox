package app

import (
	"context"
	"database/sql"
	"errors"
)

type setupArtifact struct {
	id, release, arch, platform, guest, protocol                        string
	resources, digests, provenance, manifest, sha256, bundle, actor, at string
}

func readSetupArtifact(ctx context.Context, db interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) (*setupArtifact, error) {
	a := new(setupArtifact)
	err := db.QueryRowContext(ctx, `SELECT id,release_id,architecture,platform_version,guest_release,protocol_version,
		owned_resources_json,digests_json,provenance_json,manifest_json,manifest_sha256,bundle_name,imported_by,imported_at
		FROM platform_artifacts WHERE state='available' AND is_default=1`).
		Scan(&a.id, &a.release, &a.arch, &a.platform, &a.guest, &a.protocol, &a.resources, &a.digests, &a.provenance,
			&a.manifest, &a.sha256, &a.bundle, &a.actor, &a.at)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return a, err
}

func (s setupSnapshot) stageArtifact(ctx context.Context, tx *sql.Tx) error {
	a := s.artifact
	if a == nil {
		return nil
	}
	// The managed files already live under their digest in the installation
	// directory. Move only the database record across the setup boundary.
	_, err := tx.ExecContext(ctx, `INSERT INTO platform_artifacts
		(id,release_id,architecture,platform_version,guest_release,protocol_version,owned_resources_json,digests_json,
		provenance_json,manifest_json,manifest_sha256,bundle_name,imported_by,imported_at,state,is_default)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,'available',1) ON CONFLICT DO NOTHING`,
		a.id, a.release, a.arch, a.platform, a.guest, a.protocol, a.resources, a.digests,
		a.provenance, a.manifest, a.sha256, a.bundle, a.actor, a.at)
	if err != nil {
		return err
	}
	stored, err := readSetupArtifact(ctx, tx)
	if err != nil {
		return err
	}
	if stored == nil || *stored != *a {
		return ErrSetupConflict
	}
	return nil
}
