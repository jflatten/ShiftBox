-- +goose Up
ALTER TABLE platform_artifacts ADD COLUMN ec2_family TEXT NOT NULL DEFAULT '';
ALTER TABLE platform_artifacts ADD COLUMN ec2_created_at BIGINT NOT NULL DEFAULT 0;
ALTER TABLE platform_artifacts ADD COLUMN ec2_retired INTEGER NOT NULL DEFAULT 0 CHECK (ec2_retired IN (0,1));
ALTER TABLE platform_imports ADD COLUMN ec2_family TEXT NOT NULL DEFAULT '';
ALTER TABLE resource_profiles ADD COLUMN ec2_ami_family TEXT NOT NULL DEFAULT '';
CREATE INDEX platform_artifact_ec2_family
    ON platform_artifacts (ec2_family, architecture, ec2_created_at DESC, id)
    WHERE provider_kind='ec2' AND state='available' AND ec2_retired=0 AND ec2_family<>'';
CREATE UNIQUE INDEX platform_artifact_ec2_family_membership
    ON platform_artifacts (bundle_name) WHERE provider_kind='ec2' AND ec2_family<>'';

-- +goose Down
DROP INDEX platform_artifact_ec2_family;
DROP INDEX platform_artifact_ec2_family_membership;
ALTER TABLE resource_profiles DROP COLUMN ec2_ami_family;
ALTER TABLE platform_imports DROP COLUMN ec2_family;
ALTER TABLE platform_artifacts DROP COLUMN ec2_retired;
ALTER TABLE platform_artifacts DROP COLUMN ec2_created_at;
ALTER TABLE platform_artifacts DROP COLUMN ec2_family;
