package web

import (
	"database/sql"
	"errors"
	"testing"
)

// TestSelectEC2FamilyImage picks the newest compatible, validated family member
// and ignores patches that cannot safely launch on the requested profile.
func TestSelectEC2FamilyImage(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })
	_, err = tx.Exec(`CREATE TABLE platform_artifacts (
		id TEXT, architecture TEXT, provider_kind TEXT, state TEXT, protocol_version TEXT,
		guest_release TEXT, ec2_image_json TEXT, ec2_family TEXT, ec2_created_at BIGINT, ec2_retired INTEGER DEFAULT 0
	);
	INSERT INTO platform_artifacts(id,architecture,provider_kind,state,protocol_version,guest_release,ec2_image_json,ec2_family,ec2_created_at) VALUES
		('current','amd64','ec2','available','1','current','{"amiId":"ami-current","minimumRootDiskGiB":32}','ubuntu',100),
		('incompatible','amd64','ec2','available','1','unsupported','{"amiId":"ami-incompatible","minimumRootDiskGiB":32}','ubuntu',400),
		('oversized','amd64','ec2','available','1','current','{"amiId":"ami-oversized","minimumRootDiskGiB":64}','ubuntu',400),
		('arm','arm64','ec2','available','1','current','{"amiId":"ami-arm","minimumRootDiskGiB":32}','ubuntu',400),
		('other','amd64','ec2','available','1','current','{"amiId":"ami-other","minimumRootDiskGiB":32}','other',400),
		('patch','amd64','ec2','validating','1','current','{"amiId":"ami-patch","minimumRootDiskGiB":32}','ubuntu',200);`)
	if err != nil {
		t.Fatal(err)
	}
	policy := WorkspaceLaunchPolicy{Protocols: []string{"1"}}
	check := func(family, want string) {
		t.Helper()
		got, err := selectEC2FamilyImage(t.Context(), tx, policy, family, "amd64", 32*1024, "current", "previous")
		if got != want || (want == "" && !errors.Is(err, sql.ErrNoRows)) || (want != "" && err != nil) {
			t.Errorf("selectEC2FamilyImage(%q) = %q, %v; want %q", family, got, err, want)
		}
	}
	check("ubuntu", "current")
	check("unknown", "")
	if _, err := tx.Exec(`UPDATE platform_artifacts SET state='available' WHERE id='patch'`); err != nil {
		t.Fatal(err)
	}
	check("ubuntu", "patch")
	if _, err := tx.Exec(`UPDATE platform_artifacts SET ec2_retired=1 WHERE id='patch'`); err != nil {
		t.Fatal(err)
	}
	check("ubuntu", "current")
}
