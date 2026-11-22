-- +goose Up
ALTER TABLE platform_artifacts ADD COLUMN provider_kind TEXT NOT NULL DEFAULT 'utm'
 CHECK (provider_kind IN ('utm','ec2'));
ALTER TABLE platform_artifacts ADD COLUMN ec2_image_json TEXT NOT NULL DEFAULT ''
 CHECK ((provider_kind='utm' AND ec2_image_json='') OR
        (provider_kind='ec2' AND architecture='amd64' AND ec2_image_json<>''));

-- Workspaces pin platform_artifact_id through the existing immutable launch
-- snapshot. Preserve the provider, account, region, and image it identifies.
CREATE TRIGGER immutable_platform_provider_binding
BEFORE UPDATE OF provider_kind,ec2_image_json ON platform_artifacts
FOR EACH ROW EXECUTE FUNCTION reject_available_platform_artifact_update();
