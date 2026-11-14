package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/google/uuid"
)

var errRestorePlan = errors.New("restore validation record is unavailable or invalid")

// RestorePlan binds preactivation checks to the verified archive and target.
// The target digest is computed from normalized target identity by the caller.
type RestorePlan struct {
	ID           string            `json:"id"`
	TargetDigest string            `json:"targetDigest"`
	Validation   RestoreValidation `json:"validation"`
}

// CreateRestorePlan records a validation ID only after read-only checks pass.
func CreateRestorePlan(ctx context.Context, directory, targetDigest string, archive io.Reader, password string,
	expected RestoreRequirements, checkTarget func(context.Context) error) (RestorePlan, error) {
	if !privateCheckpointDirectory(directory) || !archiveDigest(targetDigest) {
		return RestorePlan{}, errRestorePlan
	}
	validation, err := ValidateRestore(ctx, archive, password, expected, checkTarget)
	if err != nil {
		return RestorePlan{}, err
	}
	if err := ctx.Err(); err != nil {
		return RestorePlan{}, err
	}
	plan := RestorePlan{ID: uuid.NewString(), TargetDigest: targetDigest, Validation: validation}
	if err := saveRestorePlan(directory, plan); err != nil {
		return RestorePlan{}, errRestorePlan
	}
	return plan, nil
}

// LoadRestorePlan reads a private local validation record by its exact ID.
func LoadRestorePlan(directory, id string) (RestorePlan, error) {
	parsed, err := uuid.Parse(id)
	if err != nil || parsed.String() != id || !privateCheckpointDirectory(directory) {
		return RestorePlan{}, errRestorePlan
	}
	file, err := openCheckpoint(filepath.Join(directory, id+".json"))
	if err != nil {
		return RestorePlan{}, errRestorePlan
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || info.Mode().Perm()&0077 != 0 {
		return RestorePlan{}, errRestorePlan
	}
	const maxSize = 8192
	raw, err := io.ReadAll(io.LimitReader(file, maxSize+1))
	if err != nil || len(raw) > maxSize {
		return RestorePlan{}, errRestorePlan
	}
	var plan RestorePlan
	if err := json.Unmarshal(raw, &plan); err != nil || plan.ID != id || !plan.valid() {
		return RestorePlan{}, errRestorePlan
	}
	canonical, err := json.Marshal(plan)
	if err != nil || !bytes.Equal(canonical, raw) {
		return RestorePlan{}, errRestorePlan
	}
	return plan, nil
}

// StageRestore revalidates the archive, key and target, leaving verified
// entries in a private directory owned by the caller. Nothing is published
// to the target. The caller must remove the stage after activation or failure.
func StageRestore(ctx context.Context, parent string, plan RestorePlan, targetDigest string, archive io.Reader,
	password string, expected RestoreRequirements, checkTarget func(context.Context) error) (string, error) {
	if !plan.valid() || !privateCheckpointDirectory(parent) {
		return "", errRestorePlan
	}
	if targetDigest != plan.TargetDigest {
		return "", ErrRestoreTarget
	}
	if expected.Metadata != plan.Validation.Evidence.Metadata {
		return "", ErrRestoreCompatibility
	}
	if expected.ArchiveDigest != "" && expected.ArchiveDigest != plan.Validation.Evidence.ArchiveDigest {
		return "", errArchiveInvalid
	}
	expected.ArchiveDigest = plan.Validation.Evidence.ArchiveDigest
	stage, err := os.MkdirTemp(parent, ".restore-")
	if err != nil {
		return "", errRestorePlan
	}
	verified := false
	defer func() {
		if !verified {
			os.RemoveAll(stage)
		}
	}()
	evidence, doc, err := readArchive(ctx, archive, password, func(item archiveFile, data io.Reader) error {
		if item.Name == "installation.key" && expected.MountedKey != nil {
			_, err := io.Copy(io.Discard, data)
			return err
		}
		return stageRestoreEntry(stage, item.Name, data)
	})
	if err != nil || evidence != plan.Validation.Evidence {
		return "", errArchiveInvalid
	}
	validation, err := assessRestore(ctx, evidence, doc, expected, checkTarget)
	if err != nil {
		return "", err
	}
	if validation.KeyDisposition != plan.Validation.KeyDisposition {
		return "", ErrRestoreKey
	}
	verified = true
	return stage, nil
}

func stageRestoreEntry(directory, name string, data io.Reader) error {
	file, err := os.OpenFile(filepath.Join(directory, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return errRestorePlan
	}
	_, writeErr := io.Copy(file, data)
	return errors.Join(writeErr, file.Sync(), file.Close())
}

func (p RestorePlan) valid() bool {
	v := p.Validation
	e := v.Evidence
	id, err := uuid.Parse(p.ID)
	return err == nil && id.String() == p.ID && archiveDigest(p.TargetDigest) &&
		e.Metadata.valid() && e.Metadata.HostID != "" && archiveDigest(e.ArchiveDigest) && archiveDigest(e.ManifestDigest) &&
		e.Size > 0 && (v.KeyDisposition == "retain_matching_key" || v.KeyDisposition == "restore_archived_key")
}

func saveRestorePlan(directory string, plan RestorePlan) error {
	if !plan.valid() {
		return errRestorePlan
	}
	data, err := json.Marshal(plan)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(directory, ".validation-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	_, writeErr := file.Write(data)
	if err := errors.Join(writeErr, file.Sync(), file.Close()); err != nil {
		return err
	}
	if err := os.Link(file.Name(), filepath.Join(directory, plan.ID+".json")); err != nil {
		return err
	}
	parent, err := os.Open(directory)
	if err != nil {
		return err
	}
	return errors.Join(parent.Sync(), parent.Close())
}
