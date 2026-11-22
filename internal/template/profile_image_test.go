package template

import (
	"database/sql"
	"errors"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// TestSaveUsesProfileAMI verifies that AWS authors can omit the AMI and that
// a stale independently supplied template AMI cannot override the profile.
func TestSaveUsesProfileAMI(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_, err = db.Exec(`CREATE TABLE installations(singleton INTEGER, state TEXT, version INTEGER);
	INSERT INTO installations VALUES(1,'active',1);
	CREATE TABLE template_requests(actor_id TEXT, request_key TEXT, request_digest TEXT, response_json TEXT);
	CREATE TABLE resource_profiles(id TEXT, enabled INTEGER, platform_artifact_id TEXT);
	INSERT INTO resource_profiles VALUES('profile',1,'profile-image');
	CREATE TABLE platform_artifacts(id TEXT,state TEXT,provider_kind TEXT);
	INSERT INTO platform_artifacts VALUES('profile-image','available','ec2');
	CREATE TABLE network_policies(id TEXT,enabled INTEGER);
	CREATE TABLE project_templates(id TEXT,owner_user_id TEXT,name TEXT,state TEXT,current_revision INTEGER,created_at TEXT,updated_at TEXT,version INTEGER);
	CREATE TABLE template_revisions(template_id TEXT,revision INTEGER,repository_url TEXT,repository_ref TEXT,devcontainer_path TEXT,
	 resource_profile_id TEXT,platform_artifact_id TEXT,network_policy_id TEXT,secret_names_json TEXT,created_by TEXT,created_at TEXT);`)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })
	if _, err := tx.Exec("INSERT INTO resource_profiles VALUES('unbound',1,NULL)"); err != nil {
		t.Fatal(err)
	}
	_, err = Save(t.Context(), tx, SaveIntent{Actor: "owner", Key: "unbound-request", NewID: "unbound-template",
		Input: Input{Name: "Unbound", RepositoryURL: "https://github.com/example/repo", ProfileID: "unbound", ArtifactID: "profile-image"}}, time.Now())
	if !errors.Is(err, ErrUnavailable) {
		t.Errorf("Save(unbound profile, explicit EC2 image)=%v,want ErrUnavailable", err)
	}
	for _, image := range []string{"", "stale-image"} {
		view, err := Save(t.Context(), tx, SaveIntent{Actor: "owner", Key: "request-" + image, NewID: "template-" + image,
			Input: Input{Name: "Demo", RepositoryURL: "https://github.com/example/repo", ProfileID: "profile", ArtifactID: image}}, time.Now())
		if err != nil {
			t.Fatalf("Save(profile, image=%q) = %v, want profile AMI", image, err)
		}
		if view.ArtifactID != "profile-image" {
			t.Errorf("Save(profile, image=%q).ArtifactID=%q, want profile-image", image, view.ArtifactID)
		}
	}
}
