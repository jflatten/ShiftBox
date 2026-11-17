package web

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jflatten/ShiftBox/internal/provider"
	"github.com/jflatten/ShiftBox/internal/template"
	_ "modernc.org/sqlite"
)

// TestEC2LaunchImageMatchesArchitecture verifies that launch rejects a mismatched
// profile AMI even when another verified AMI matches the selected type.
func TestEC2LaunchImageMatchesArchitecture(t *testing.T) {
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
		guest_release TEXT, ec2_image_json TEXT
	);
	INSERT INTO platform_artifacts VALUES
		('template-amd64','amd64','ec2','available','1','current','{"amiId":"ami-x86"}'),
		('verified-arm64','arm64','ec2','available','1','current','{"amiId":"ami-arm"}'),
		('unverified-arm64','arm64','ec2','available','1','current','{"amiId":"ami-unverified"}'),
		('old-arm64','arm64','ec2','available','1','old','{"amiId":"ami-old"}'),
		('utm-amd64','amd64','utm','available','1','current','');
	CREATE TABLE installations(id TEXT, singleton INTEGER, active_release TEXT, min_guest_release TEXT);
	INSERT INTO installations VALUES ('install',1,'current','previous');
	CREATE TABLE operations(id TEXT, requested_by TEXT, request_key TEXT);
	CREATE TABLE resource_profiles(id TEXT, enabled INTEGER, cpu_count INTEGER, memory_mib INTEGER, disk_mib INTEGER, instance_type TEXT,platform_artifact_id TEXT);
	INSERT INTO resource_profiles VALUES ('profile',1,2,8192,32768,'m7g.large','template-amd64');`)
	if err != nil {
		t.Fatal(err)
	}
	policy := WorkspaceLaunchPolicy{Protocols: []string{"1"}, EC2VerifiedAMIs: []string{"ami-x86", "ami-arm", "ami-old"},
		Capabilities: provider.Capabilities{Name: "ec2", Create: true, Start: true, Architectures: []string{"amd64", "arm64"},
			InstanceTypes: []provider.InstanceType{{Name: "m7g.large", Architecture: "arm64", CPUCount: 2, MemoryMiB: 8192}}}}
	if _, err := verifyEC2LaunchImage(t.Context(), tx, policy, "template-amd64", "arm64", "current", "previous"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("launch with a mismatched template AMI = %v; want no match despite an available ARM64 AMI", err)
	}
	if _, err := verifyEC2LaunchImage(t.Context(), tx, policy, "verified-arm64", "arm64", "current", "previous"); err != nil {
		t.Fatalf("launch after explicitly choosing the ARM64 AMI = %v; want success", err)
	}
	if _, err := verifyEC2LaunchImage(t.Context(), tx, policy, "template-amd64", "amd64", "current", "previous"); err != nil {
		t.Fatalf("launch with the template's compatible AMD64 AMI = %v; want success", err)
	}
	if _, err := verifyEC2LaunchImage(t.Context(), tx, policy, "utm-amd64", "amd64", "current", "previous"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("launch with a UTM image = %v; want no match", err)
	}
	if _, err := verifyEC2LaunchImage(t.Context(), tx, policy, "old-arm64", "arm64", "current", "previous"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("launch with an outdated guest release = %v; want no match", err)
	}
	if _, err := verifyEC2LaunchImage(t.Context(), tx, policy, "unverified-arm64", "arm64", "current", "previous"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("launch with an unverified ARM64 AMI = %v; want no match", err)
	}

	// Admission must stop before reaching the workspace and reservation writes.
	input := workspaceLaunchRequest{TemplateID: "template", TemplateRevision: 1, key: "fixture-launch-key",
		revision: template.LaunchRevision{ProfileID: "profile", ArtifactID: "template-amd64"}}
	_, err = applyWorkspaceLaunch(t.Context(), tx, nil, "member", input, policy, time.Now())
	var problem Problem
	if !errors.As(err, &problem) || problem.Status != http.StatusConflict || problem.Code != "workspace.platform_incompatible" ||
		!strings.Contains(problem.Detail, "verified arm64 AMI on the resource profile") {
		t.Fatalf("launch refusal = %+v (%v); want conflict with explicit ARM64 resource profile guidance", problem, err)
	}
}
