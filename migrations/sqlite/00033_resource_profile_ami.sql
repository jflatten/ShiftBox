-- +goose Up
-- Resource profiles own the AWS image used by future workspace admissions.
ALTER TABLE resource_profiles ADD COLUMN platform_artifact_id TEXT REFERENCES platform_artifacts(id);

-- Preserve a historical image only when the operator's prior choice is unique.
-- Profiles with multiple images require an explicit administrator selection.
UPDATE resource_profiles SET platform_artifact_id=(
 SELECT MIN(r.platform_artifact_id) FROM template_revisions r
 JOIN platform_artifacts a ON a.id=r.platform_artifact_id
 WHERE r.resource_profile_id=resource_profiles.id AND a.provider_kind='ec2'
) WHERE (SELECT COUNT(DISTINCT r.platform_artifact_id) FROM template_revisions r
 JOIN platform_artifacts a ON a.id=r.platform_artifact_id
 WHERE r.resource_profile_id=resource_profiles.id AND a.provider_kind='ec2')=1;
