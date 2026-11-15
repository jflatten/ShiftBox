-- +goose Up
-- Defer child references while replacing the parent within the migration transaction.
PRAGMA defer_foreign_keys = ON;

CREATE TABLE platform_artifacts_new (
 id TEXT NOT NULL PRIMARY KEY,
 -- Unique human-readable release identity
 release_id TEXT NOT NULL UNIQUE CHECK (length(trim(release_id)) > 0),
 architecture TEXT NOT NULL CHECK (architecture IN ('amd64','arm64')),
 platform_version TEXT NOT NULL,
 guest_release TEXT NOT NULL,
 protocol_version TEXT NOT NULL,
 owned_resources_json TEXT NOT NULL,
 digests_json TEXT NOT NULL,
 provenance_json TEXT NOT NULL,
 manifest_json TEXT NOT NULL,
 manifest_sha256 TEXT NOT NULL CHECK (length(manifest_sha256) = 64 AND manifest_sha256 NOT GLOB '*[^0-9a-f]*'),
 bundle_name TEXT NOT NULL,
 imported_by TEXT NOT NULL REFERENCES users(id),
 imported_at TEXT NOT NULL,
 is_default INTEGER NOT NULL DEFAULT 0 CHECK (is_default IN (0,1)),
 -- `validating`, `available`, or `rejected`
 state TEXT NOT NULL CHECK (state IN ('validating','available','rejected')),
 CHECK (is_default = 0 OR state = 'available')
);

INSERT INTO platform_artifacts_new
SELECT id, release_id, architecture, platform_version, guest_release, protocol_version,
 owned_resources_json, digests_json, provenance_json, manifest_json, manifest_sha256,
 bundle_name, imported_by, imported_at, is_default, state
FROM platform_artifacts;

DROP TABLE platform_artifacts;
ALTER TABLE platform_artifacts_new RENAME TO platform_artifacts;

CREATE UNIQUE INDEX one_default_platform_artifact ON platform_artifacts(is_default) WHERE is_default = 1;

-- Available content/manifest fields are immutable. The default flag can change.
-- +goose StatementBegin
CREATE TRIGGER immutable_available_platform_artifact
BEFORE UPDATE OF id, release_id, architecture, platform_version, guest_release, protocol_version,
 owned_resources_json, digests_json, provenance_json, manifest_json, manifest_sha256, bundle_name,
 imported_by, imported_at, state ON platform_artifacts
WHEN OLD.state = 'available'
BEGIN
 SELECT RAISE(ABORT, 'available platform artifacts are immutable');
END;
-- +goose StatementEnd

-- Check the rebuilt catalog and its references before clearing SQLite's deferred
-- violations, which otherwise still count the temporary removal of the parent.
CREATE TEMP TABLE platform_architecture_fk_check (violations INTEGER CHECK (violations = 0));
INSERT INTO platform_architecture_fk_check SELECT count(*) FROM pragma_foreign_key_check;
DROP TABLE platform_architecture_fk_check;
PRAGMA defer_foreign_keys = OFF;
