package guest

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
)

const activateGuestUpdateCommand = "activate-guest-update"
const problemGuestUpdateRollback = "guest.update_rolled_back"

// Returning a failure from the agent service asks systemd to restart it.
var errGuestUpdateRestart = errors.New("restarting the guest agent for a supervised update trial")

func (j *localJournal) activateUpdate(dispatch guestDispatch) error {
	staged := j.state.Operation
	if !dispatch.valid() || dispatch.Command != activateGuestUpdateCommand || staged == nil {
		return errGuestDispatchConflict
	}
	if staged.Command == activateGuestUpdateCommand {
		return j.accept(dispatch)
	}
	bound := staged.guestDispatch
	bound.Command, bound.Version = activateGuestUpdateCommand, dispatch.Version
	if dispatch != bound || dispatch.Version < staged.Version || staged.Command != stageGuestUpdateCommand ||
		staged.Phase != localSucceeded {
		return errGuestDispatchConflict
	}
	raw, err := hex.DecodeString(staged.UpdateDigest)
	if err != nil || len(raw) != sha256.Size {
		return errGuestDispatchConflict
	}
	digest := [sha256.Size]byte(raw)
	path := filepath.Join(filepath.Dir(j.path), "update-trial.json")
	trial, err := loadGuestUpdateTrial(path)
	if err != nil {
		return err
	}
	next := *staged
	next.guestDispatch = dispatch
	switch {
	case trial.Active == digest:
		return j.saveOperation(next)
	case trial.Candidate == digest && trial.Phase == "testing":
		// The first authenticated poll is still in progress. Leave the
		// staging receipt available until it confirms the running trial.
		return nil
	case trial.Candidate == digest && trial.Phase == "failed":
		next.Phase, next.Problem = localFailed, problemGuestUpdateRollback
		return j.saveOperation(next)
	}
	if err := requestGuestUpdateTrial(path, digest, false); err != nil {
		return err
	}
	return errGuestUpdateRestart
}
