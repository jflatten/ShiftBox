package web

import (
	"database/sql"
	"errors"
	"testing"

	"github.com/jflatten/ShiftBox/internal/provider"
	_ "modernc.org/sqlite"
)

// TestProfileAMIValidation prevents forged, unverified, incompatible, and
// undersized profile selections from bypassing the browser's AMI filtering.
func TestProfileAMIValidation(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_, err = db.Exec(`CREATE TABLE installations(singleton INTEGER, active_release TEXT, min_guest_release TEXT);
	INSERT INTO installations VALUES(1,'current','previous');
	CREATE TABLE platform_artifacts(id TEXT,release_id TEXT,architecture TEXT,protocol_version TEXT,guest_release TEXT,
	 is_default INTEGER,provider_kind TEXT,state TEXT,ec2_image_json TEXT);
	INSERT INTO platform_artifacts VALUES
	 ('x86','Ubuntu x86','amd64','1','current',1,'ec2','available','{"amiId":"ami-x86","minimumRootDiskGiB":32}'),
	 ('arm','Ubuntu ARM','arm64','1','current',0,'ec2','available','{"amiId":"ami-arm","minimumRootDiskGiB":48}'),
	 ('unverified','Unverified','amd64','1','current',0,'ec2','available','{"amiId":"ami-other","minimumRootDiskGiB":32}'),
	 ('old','Old','amd64','1','old',0,'ec2','available','{"amiId":"ami-x86","minimumRootDiskGiB":32}');`)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })
	policy := WorkspaceLaunchPolicy{Protocols: []string{"1"}, EC2VerifiedAMIs: []string{"ami-x86", "ami-arm"},
		Capabilities: provider.Capabilities{Name: "ec2", Architectures: []string{"amd64", "arm64"},
			InstanceTypes: []provider.InstanceType{{Name: "t3.medium", Architecture: "amd64"}, {Name: "m7g.large", Architecture: "arm64"}}}}
	for _, tc := range []struct {
		image, instance string
		disk            int64
		valid           bool
	}{
		{"x86", "t3.medium", 32768, true}, {"arm", "m7g.large", 65536, true},
		{"", "t3.medium", 32768, false}, {"missing", "t3.medium", 32768, false},
		{"arm", "t3.medium", 65536, false}, {"arm", "m7g.large", 32768, false},
		{"unverified", "t3.medium", 32768, false}, {"old", "t3.medium", 32768, false},
	} {
		input := profileInput{PlatformArtifactID: tc.image, InstanceType: tc.instance, workspaceResources: workspaceResources{DiskMiB: tc.disk}}
		err := input.validateAMI(t.Context(), tx, policy)
		if (err == nil) != tc.valid {
			t.Errorf("validateAMI(%+v) = %v, want valid=%t", tc, err, tc.valid)
		}
		var problem Problem
		if err != nil && (!errors.As(err, &problem) || problem.Status != 409) {
			t.Errorf("validateAMI(%+v) = %v, want compatibility conflict", tc, err)
		}
	}
}
