package secret

import (
	"database/sql"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/jflatten/ShiftBox/internal/database"
)

// TestWorkspaceSecretList lists each effective name once with its winning
// scope, without opening ciphertext, and removes project names after revocation.
func TestWorkspaceSecretList(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`CREATE TABLE installations(id TEXT,state TEXT);
	INSERT INTO installations VALUES('installation','active');
	CREATE TABLE users(id TEXT,role TEXT,membership_state TEXT);
	INSERT INTO users VALUES('member','member','active');
	CREATE TABLE project_templates(id TEXT,owner_user_id TEXT);
	INSERT INTO project_templates VALUES('template','another');
	CREATE TABLE template_grants(template_id TEXT,user_id TEXT,can_launch INTEGER,revoked_at TEXT);
	INSERT INTO template_grants VALUES('template','member',1,NULL);
	CREATE TABLE workspaces(id TEXT,installation_id TEXT,owner_user_id TEXT,template_id TEXT,generation BIGINT,
	provider_instance_id TEXT,desired_state TEXT,observed_state TEXT);
	INSERT INTO workspaces VALUES('work','installation','member','template',3,'vm','running','running');
	CREATE TABLE secret_values(scope_type TEXT,scope_id TEXT,name TEXT,ciphertext_json TEXT);
	INSERT INTO secret_values VALUES
	('organization','installation','SHARED','must-not-leak'),
	('organization','installation','ORG_ONLY','must-not-leak'),
	('template','template','SHARED','must-not-leak'),
	('template','template','PROJECT_ONLY','must-not-leak'),
	('user','member','SHARED','must-not-leak'),
	('user','member','USER_ONLY','must-not-leak'),
	('user','another','HIDDEN_USER','must-not-leak'),
	('template','another','HIDDEN_PROJECT','must-not-leak');`)
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(nil)
	check := func(want []Key) {
		t.Helper()
		if err := database.Transaction(t.Context(), db, func(tx *sql.Tx) error {
			got, err := service.ListWorkspace(t.Context(), tx, "work", 3)
			if err != nil {
				return err
			}
			if diff := cmp.Diff(want, got); diff != "" {
				t.Errorf("ListWorkspace(work,3) mismatch (-want +got):\n%s", diff)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	check([]Key{{Name: "ORG_ONLY", Scope: "organization"}, {Name: "PROJECT_ONLY", Scope: "project"}, {Name: "SHARED", Scope: "user"}, {Name: "USER_ONLY", Scope: "user"}})
	if _, err := db.Exec("UPDATE template_grants SET revoked_at='revoked'"); err != nil {
		t.Fatal(err)
	}
	check([]Key{{Name: "ORG_ONLY", Scope: "organization"}, {Name: "SHARED", Scope: "user"}, {Name: "USER_ONLY", Scope: "user"}})
	if _, err := db.Exec("DELETE FROM secret_values WHERE scope_type='user' AND name='SHARED'"); err != nil {
		t.Fatal(err)
	}
	check([]Key{{Name: "ORG_ONLY", Scope: "organization"}, {Name: "SHARED", Scope: "organization"}, {Name: "USER_ONLY", Scope: "user"}})
	for _, query := range []string{
		"UPDATE workspaces SET generation=4",
		"UPDATE workspaces SET generation=3,desired_state='stopped'",
		"UPDATE workspaces SET desired_state='running',observed_state='stopped'",
		"UPDATE workspaces SET observed_state='running'; UPDATE users SET membership_state='disabled'",
		"UPDATE users SET membership_state='active'; UPDATE installations SET state='inactive'",
	} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
		if err := database.Transaction(t.Context(), db, func(tx *sql.Tx) error {
			got, err := service.ListWorkspace(t.Context(), tx, "work", 3)
			if !errors.Is(err, ErrWorkspaceDenied) || len(got) != 0 {
				t.Errorf("ListWorkspace after %q=%v,%v,want denied/no keys", query, got, err)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
}
