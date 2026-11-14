-- +goose Up
CREATE TABLE project_templates (
 id TEXT NOT NULL PRIMARY KEY,
 -- Owner; administrator management does not silently transfer ownership
 owner_user_id TEXT NOT NULL REFERENCES users(id),
 -- Non-empty display name
 name TEXT NOT NULL CHECK (length(trim(name)) > 0),
 -- `active` or `archived`
 state TEXT NOT NULL CHECK (state IN ('active','archived')),
 -- Existing immutable revision number
 current_revision BIGINT NOT NULL CHECK (current_revision > 0),
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 archived_at TEXT,
 version BIGINT NOT NULL CHECK (version > 0),
 CHECK ((state = 'archived') = (archived_at IS NOT NULL)),
 FOREIGN KEY (id,current_revision) REFERENCES template_revisions(template_id,revision) DEFERRABLE INITIALLY DEFERRED
);

CREATE TABLE template_revisions (
 template_id TEXT NOT NULL REFERENCES project_templates(id),
 revision BIGINT NOT NULL CHECK (revision > 0),
 -- HTTPS GitHub.com URL in MVP
 repository_url TEXT NOT NULL CHECK (lower(substr(repository_url,1,19)) = 'https://github.com/'),
 repository_ref TEXT,
 -- Normalized repository-relative path
 devcontainer_path TEXT NOT NULL CHECK (length(devcontainer_path) > 0 AND substr(devcontainer_path,1,1) <> '/'),
 resource_profile_id TEXT NOT NULL REFERENCES resource_profiles(id),
 platform_artifact_id TEXT NOT NULL REFERENCES platform_artifacts(id),
 network_policy_id TEXT,
 -- References only; never plaintext values
 secret_names_json TEXT NOT NULL,
 created_by TEXT NOT NULL REFERENCES users(id),
 created_at TEXT NOT NULL,
 -- Composite immutable identity
 PRIMARY KEY (template_id, revision)
);

CREATE TABLE template_grants (
 template_id TEXT NOT NULL REFERENCES project_templates(id),
 user_id TEXT NOT NULL REFERENCES users(id),
 can_launch INTEGER NOT NULL CHECK (can_launch = 1),
 project_secret_warning_ack_at TEXT NOT NULL,
 granted_by TEXT NOT NULL REFERENCES users(id),
 granted_at TEXT NOT NULL,
 revoked_at TEXT
);
CREATE UNIQUE INDEX one_active_template_grant ON template_grants(template_id,user_id) WHERE revoked_at IS NULL;

-- +goose StatementBegin
CREATE TRIGGER immutable_template_owner BEFORE UPDATE OF owner_user_id ON project_templates
WHEN NEW.owner_user_id <> OLD.owner_user_id
BEGIN
 SELECT RAISE(ABORT, 'template ownership is immutable');
END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER immutable_template_revision BEFORE UPDATE ON template_revisions
BEGIN
 SELECT RAISE(ABORT, 'template revisions are immutable');
END;
-- +goose StatementEnd
