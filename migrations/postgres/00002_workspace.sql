-- +goose Up
CREATE TABLE platform_artifacts (
 id TEXT NOT NULL PRIMARY KEY,
 -- Unique human-readable release identity
 release_id TEXT NOT NULL UNIQUE CHECK (length(trim(release_id)) > 0),
 -- Must be `amd64` for MVP
 architecture TEXT NOT NULL CHECK (architecture = 'amd64'),
 platform_version TEXT NOT NULL,
 guest_release TEXT NOT NULL,
 protocol_version TEXT NOT NULL,
 owned_resources_json TEXT NOT NULL,
 digests_json TEXT NOT NULL,
 provenance_json TEXT NOT NULL,
 manifest_json TEXT NOT NULL,
 manifest_sha256 TEXT NOT NULL CHECK (manifest_sha256 ~ '^[a-f0-9]{64}$'),
 bundle_name TEXT NOT NULL,
 imported_by TEXT NOT NULL REFERENCES users(id),
 imported_at TEXT NOT NULL,
 is_default INTEGER NOT NULL DEFAULT 0 CHECK (is_default IN (0,1)),
 -- `validating`, `available`, or `rejected`
 state TEXT NOT NULL CHECK (state IN ('validating','available','rejected')),
 CHECK (is_default = 0 OR state = 'available')
);
-- At most one compatible default
CREATE UNIQUE INDEX one_default_platform_artifact ON platform_artifacts(is_default) WHERE is_default = 1;

-- Available content/manifest fields are immutable. The default flag can change.
-- +goose StatementBegin
CREATE FUNCTION reject_available_platform_artifact_update() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
 RAISE EXCEPTION 'available platform artifacts are immutable';
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER immutable_available_platform_artifact
BEFORE UPDATE OF id, release_id, architecture, platform_version, guest_release, protocol_version,
 owned_resources_json, digests_json, provenance_json, manifest_json, manifest_sha256, bundle_name,
 imported_by, imported_at, state ON platform_artifacts
FOR EACH ROW WHEN (OLD.state = 'available')
EXECUTE FUNCTION reject_available_platform_artifact_update();
