package backup

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestRestorePlanStagesOnlyValidatedArchive proves a reopened plan binds both
// replacement and archive, and cleans private staging after failed verification.
func TestRestorePlanStagesOnlyValidatedArchive(t *testing.T) {
	ctx := t.Context()
	meta := restoreFixtureMetadata()
	key := bytes.Repeat([]byte{7}, 32)
	archive := makeRestoreFixture(t, meta, key, false)
	other := makeRestoreFixture(t, meta, key, false)
	inputs := RestoreRequirements{Metadata: meta, MountedKey: key}
	plans := checkpointStage(t)
	target := digestBytes([]byte("replacement A"))
	if _, err := CreateRestorePlan(ctx, plans, target, bytes.NewReader(archive), "wrong password", inputs, nil); !errors.Is(err, errArchiveInvalid) {
		t.Fatalf("CreateRestorePlan with wrong password = %v; want archive rejection", err)
	}
	if entries, err := os.ReadDir(plans); err != nil || len(entries) != 0 {
		t.Fatalf("failed validation left a record: %v", err)
	}
	checks := 0
	checkTarget := func(context.Context) error { checks++; return nil }
	plan, err := CreateRestorePlan(ctx, plans, target, bytes.NewReader(archive), restoreFixturePassword, inputs, checkTarget)
	if err != nil {
		t.Fatal(err)
	}
	if checks != 1 {
		t.Fatalf("target checks = %d, want 1", checks)
	}
	raw, err := os.ReadFile(filepath.Join(plans, plan.ID+".json"))
	if err != nil || bytes.Contains(raw, key) || bytes.Contains(raw, []byte(restoreFixturePassword)) {
		t.Fatal("validation record missing or retained protected inputs")
	}
	loaded, err := LoadRestorePlan(plans, plan.ID)
	if err != nil || loaded != plan {
		t.Fatalf("reopen validation record = %v; want same plan", err)
	}
	if _, err := LoadRestorePlan(plans, "../"+plan.ID); !errors.Is(err, errRestorePlan) {
		t.Fatalf("LoadRestorePlan with a path = %v; want validation record rejection", err)
	}
	planPath := filepath.Join(plans, plan.ID+".json")
	if err := os.Chmod(planPath, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadRestorePlan(plans, plan.ID); !errors.Is(err, errRestorePlan) {
		t.Fatalf("LoadRestorePlan with shared permissions = %v; want validation record rejection", err)
	}
	if err := os.Chmod(planPath, 0600); err != nil {
		t.Fatal(err)
	}

	stageParent := checkpointStage(t)
	for _, tc := range []struct {
		name, target, password string
		archive                []byte
		want                   error
	}{
		{"other target", digestBytes([]byte("replacement B")), restoreFixturePassword, archive, ErrRestoreTarget},
		{"changed archive", target, restoreFixturePassword, other, errArchiveInvalid},
		{"wrong password", target, "unrelated password", archive, errArchiveInvalid},
		{"truncated", target, restoreFixturePassword, archive[:len(archive)-1], errArchiveInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stage, err := StageRestore(ctx, stageParent, loaded, tc.target, bytes.NewReader(tc.archive), tc.password, inputs, checkTarget)
			if stage != "" || !errors.Is(err, tc.want) {
				t.Fatalf("StageRestore = %q, %v; want %v and no stage", stage, err, tc.want)
			}
			if entries, err := os.ReadDir(stageParent); err != nil || len(entries) != 0 {
				t.Fatalf("failed restore left staged files: %v", err)
			}
		})
	}
	if checks != 1 {
		t.Fatalf("invalid restore checked the target %d times; want no additional checks", checks-1)
	}
	occupied := func(context.Context) error { return ErrRestoreTarget }
	if stage, err := StageRestore(ctx, stageParent, loaded, target, bytes.NewReader(archive), restoreFixturePassword, inputs, occupied); stage != "" || !errors.Is(err, ErrRestoreTarget) {
		t.Fatalf("StageRestore after the target became occupied = %q, %v; want target rejection", stage, err)
	}
	if entries, err := os.ReadDir(stageParent); err != nil || len(entries) != 0 {
		t.Fatalf("target rejection left staged files: %v", err)
	}
	stage, err := StageRestore(ctx, stageParent, loaded, target, bytes.NewReader(archive), restoreFixturePassword, inputs, checkTarget)
	if err != nil || checks != 2 {
		t.Fatalf("valid restore stage = %v, %d checks; want staging and fresh target validation", err, checks)
	}
	defer os.RemoveAll(stage)
	if _, err := os.Lstat(filepath.Join(stage, "installation.key")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("restore staged an archived copy of the mounted key")
	}
	for _, name := range []string{"application.db", "dex.db", "configuration.json", "guest-ca.json"} {
		path := filepath.Join(stage, name)
		data, err := os.ReadFile(path)
		if err != nil || string(data) != "fixture "+name {
			t.Fatalf("staged %s = %v; want verified fixture", name, err)
		}
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("staged %s is not private: %v", name, err)
		}
	}
}
