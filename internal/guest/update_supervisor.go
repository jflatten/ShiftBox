package guest

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"os"
	"path/filepath"
)

func confirmRunningGuestTrial(ctx context.Context, stateDir, executable string, poll func(context.Context) error) error {
	path := filepath.Join(stateDir, "update-trial.json")
	trial, err := loadGuestUpdateTrial(path)
	if err != nil {
		return err
	}
	if trial.Phase != "testing" {
		return poll(ctx)
	}
	if executable != guestUpdateBinaryPath(stateDir, trial.Candidate) {
		return errGuestUpdateTrialConflict
	}
	if err := poll(ctx); err != nil {
		return err
	}
	return confirmGuestUpdateTrial(path, guestUpdateBoot{Digest: trial.Candidate, Attempt: trial.Attempt})
}

// SelectGuestAgent is called once by the platform launcher before starting
// the guest service. It records an attempted trial before handing back its
// path, so a restart will select the last confirmed binary.
func SelectGuestAgent(stateDir, platformBinary string) (string, error) {
	info, err := os.Lstat(stateDir)
	if err != nil || !filepath.IsAbs(stateDir) || !filepath.IsAbs(platformBinary) || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return "", errors.New("guest update requires a private state directory")
	}
	trialPath := filepath.Join(stateDir, "update-trial.json")
	boot, err := nextGuestUpdateBinary(trialPath)
	if err != nil {
		return "", err
	}
	if selected, err := guestAgentBinary(stateDir, platformBinary, boot.Digest); err == nil || boot.Attempt == "" {
		return selected, err
	}
	// The attempted candidate failed verification before launch. Record the
	// failure now and use the same fallback path as an interrupted trial.
	boot, err = nextGuestUpdateBinary(trialPath)
	if err != nil {
		return "", err
	}
	return guestAgentBinary(stateDir, platformBinary, boot.Digest)
}

func guestAgentBinary(dir, platform string, digest [sha256.Size]byte) (string, error) {
	if digest == ([sha256.Size]byte{}) {
		return platform, nil
	}
	path := guestUpdateBinaryPath(dir, digest)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() < 1 || info.Size() > maxGuestUpdateBytes {
		return "", errGuestUpdateInvalid
	}
	file, err := os.Open(path)
	if err != nil {
		return "", errGuestUpdateInvalid
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return "", errGuestUpdateInvalid
	}
	hash := sha256.New()
	size, err := io.Copy(hash, io.LimitReader(file, maxGuestUpdateBytes+1))
	if err != nil || size != info.Size() || [sha256.Size]byte(hash.Sum(nil)) != digest {
		return "", errGuestUpdateInvalid
	}
	if err := file.Chmod(0700); err != nil {
		return "", errGuestUpdateInvalid
	}
	return path, nil
}
