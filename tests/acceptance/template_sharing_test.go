package acceptance_test

import (
	"database/sql"
	"encoding/json"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

type sharingResult struct {
	WorkspaceID     string `json:"workspaceId"`
	TemplateID      string `json:"templateId"`
	MemberID        string `json:"memberId"`
	AdministratorID string `json:"administratorId"`
	SecretKey       string `json:"secretKey"`
	ProjectOnlyKey  string `json:"projectOnlyKey"`
}

// TestTemplateSharingJournal verifies that the controller preserved owner
// provenance and committed the access changes exercised by the browser.
func TestTemplateSharingJournal(t *testing.T) {
	dbPath, resultPath := os.Getenv("SHIFTBOX_SHARING_DATABASE"), os.Getenv("SHIFTBOX_SHARING_RESULT")
	if dbPath == "" && resultPath == "" {
		t.Skip("set SHIFTBOX_SHARING_DATABASE and SHIFTBOX_SHARING_RESULT after the browser journey")
	}
	if !filepath.IsAbs(dbPath) || !filepath.IsAbs(resultPath) {
		t.Fatal("provide absolute paths for the disposable database and browser result")
	}
	result := readSharingResult(t, resultPath)
	dsn := url.URL{Scheme: "file", Path: dbPath, RawQuery: "mode=ro"}
	db, err := sql.Open("sqlite", dsn.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	for _, check := range []struct {
		label string
		query string
		args  []any
	}{
		{"workspace retained its member owner, template and verified guest", `SELECT EXISTS(
			SELECT 1 FROM workspaces w JOIN project_templates t ON t.id=w.template_id
			JOIN template_revisions v ON v.template_id=t.id AND v.revision=w.template_revision
			JOIN guest_registrations g ON g.workspace_id=w.id AND g.generation=w.generation
			WHERE w.id=$1 AND t.id=$2 AND w.owner_user_id=$3 AND t.owner_user_id=$4
			AND t.state='archived' AND w.desired_state='running' AND w.observed_state='running'
			AND g.state='active' AND g.last_authenticated_at IS NOT NULL
			AND EXISTS (SELECT 1 FROM operations o WHERE o.workspace_id=w.id AND o.type='provision' AND o.status='succeeded')
		)`, []any{result.WorkspaceID, result.TemplateID, result.MemberID, result.AdministratorID}},
		{"project grant was acknowledged, revoked and audited", `SELECT EXISTS(
			SELECT 1 FROM template_grants g WHERE g.template_id=$1 AND g.user_id=$2 AND g.granted_by=$3
			AND length(g.project_secret_warning_ack_at)>0 AND g.revoked_at IS NOT NULL
		) AND NOT EXISTS (
			SELECT 1 FROM template_grants WHERE template_id=$1 AND user_id=$2 AND revoked_at IS NULL
		) AND EXISTS (
			SELECT 1 FROM audit_events WHERE resource_id=$1 AND actor_id=$3 AND action='template.share' AND reason_code='launch_revoked'
		)`, []any{result.TemplateID, result.MemberID, result.AdministratorID}},
		{"distinct GitHub identities retained separate authorizations and only the member's credential was revoked", `SELECT EXISTS(
			SELECT 1 FROM github_authorizations member JOIN github_authorizations administrator ON member.app_id=administrator.app_id
			WHERE member.user_id=$1 AND member.state='revoked' AND length(member.ciphertext)=0
			AND administrator.user_id=$2 AND administrator.state='connected' AND length(administrator.ciphertext)>0
			AND length(member.provider_user_id)>0 AND length(administrator.provider_user_id)>0
			AND member.provider_user_id<>administrator.provider_user_id
		)`, []any{result.MemberID, result.AdministratorID}},
		{"administrator connection was audited in member context and revoked", `SELECT EXISTS(
			SELECT 1 FROM resource_grants g JOIN origin_grants o ON o.resource_grant_id=g.id
			JOIN audit_events a ON a.resource_id=g.resource_id AND a.actor_id=g.subject_user_id
			WHERE g.resource_id=$1 AND g.subject_user_id=$2 AND g.action='workspace.connect_admin'
			AND g.revoked_at IS NOT NULL AND o.service_key='ide' AND o.consumed_at IS NOT NULL AND o.revoked_at IS NOT NULL
			AND a.action='workspace.connect_admin' AND json_extract(a.metadata_json,'$.ownerId')=$3
			AND json_extract(a.metadata_json,'$.grantId')=g.id
		) AND NOT EXISTS (
			SELECT 1 FROM resource_grants WHERE resource_id=$1 AND subject_user_id=$2
			AND action='workspace.connect_admin' AND revoked_at IS NULL
		)`, []any{result.WorkspaceID, result.AdministratorID, result.MemberID}},
		{"deleting the member override retained encrypted lower scopes", `SELECT NOT EXISTS(
			SELECT 1 FROM secret_values WHERE scope_type='user' AND scope_id=$1 AND name=$3
		) AND EXISTS (
			SELECT 1 FROM secret_values WHERE scope_type='template' AND scope_id=$2 AND name=$4 AND length(ciphertext_json)>0
		) AND EXISTS (
			SELECT 1 FROM secret_values s JOIN installations i ON i.id=s.scope_id
			WHERE s.scope_type='organization' AND s.name=$3 AND length(s.ciphertext_json)>0
		)`, []any{result.MemberID, result.TemplateID, result.SecretKey, result.ProjectOnlyKey}},
	} {
		var verified bool
		if err := db.QueryRowContext(t.Context(), check.query, check.args...).Scan(&verified); err != nil {
			t.Errorf("verify %s: %v", check.label, err)
		} else if !verified {
			t.Errorf("browser fixture has no durable evidence that %s", check.label)
		}
	}
}

func readSharingResult(t *testing.T, path string) sharingResult {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 4096 {
		t.Fatal("browser result must be a private regular file up to 4096 bytes")
	}
	decoder := json.NewDecoder(io.LimitReader(file, 4097))
	decoder.DisallowUnknownFields()
	var result sharingResult
	if err := decoder.Decode(&result); err != nil || decoder.Decode(new(any)) != io.EOF {
		t.Fatal("invalid browser result")
	}
	for _, id := range []string{result.WorkspaceID, result.TemplateID, result.MemberID, result.AdministratorID} {
		if parsed, err := uuid.Parse(id); err != nil || parsed.String() != id {
			t.Fatal("browser result contains an invalid fixture identifier")
		}
	}
	if !regexp.MustCompile(`^SHARING_[A-F0-9]{32}$`).MatchString(result.SecretKey) ||
		result.ProjectOnlyKey != result.SecretKey+"_PROJECT" || result.MemberID == result.AdministratorID {
		t.Fatal("browser result contains invalid fixture identities or secret names")
	}
	return result
}
