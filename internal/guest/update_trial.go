package guest

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"os"

	"github.com/google/uuid"
)

var errGuestUpdateTrialConflict = errors.New("guest update trial conflicts with the recorded activation")

type guestUpdateTrial struct {
	// A zero active digest means the original platform binary.
	Active    [sha256.Size]byte `json:"active"`
	Candidate [sha256.Size]byte `json:"candidate"`
	Phase     string            `json:"phase"`
	Attempt   string            `json:"attempt,omitempty"`
}

type guestUpdateBoot struct {
	Digest  [sha256.Size]byte
	Attempt string
}

// The guest agent requests activation at a safe boundary. The supervisor
// records the attempt before starting the candidate. A subsequent supervisor
// start after an unconfirmed attempt selects the last confirmed binary.
func requestGuestUpdateTrial(path string, candidate [sha256.Size]byte, retry bool) error {
	trial, err := loadGuestUpdateTrial(path)
	if err != nil {
		return err
	}
	if candidate == ([sha256.Size]byte{}) {
		return errGuestUpdateTrialConflict
	}
	if trial.Phase == "requested" && trial.Candidate == candidate {
		return nil
	}
	if trial.Phase == "requested" || trial.Phase == "testing" ||
		(trial.Phase == "failed" && trial.Candidate == candidate && !retry) {
		return errGuestUpdateTrialConflict
	}
	if trial.Active == candidate {
		return nil
	}
	trial.Candidate, trial.Phase, trial.Attempt = candidate, "requested", ""
	return trial.save(path)
}

func nextGuestUpdateBinary(path string) (guestUpdateBoot, error) {
	trial, err := loadGuestUpdateTrial(path)
	if err != nil {
		return guestUpdateBoot{}, err
	}
	switch trial.Phase {
	case "requested":
		attempt, err := uuid.NewRandom()
		if err != nil {
			return guestUpdateBoot{}, err
		}
		trial.Phase, trial.Attempt = "testing", attempt.String()
		if err := trial.save(path); err != nil {
			return guestUpdateBoot{}, err
		}
		return guestUpdateBoot{Digest: trial.Candidate, Attempt: trial.Attempt}, nil
	case "testing":
		trial.Phase = "failed"
		if err := trial.save(path); err != nil {
			return guestUpdateBoot{}, err
		}
	}
	return guestUpdateBoot{Digest: trial.Active}, nil
}

// confirmGuestUpdateTrial is called only after the trial process has proved
// authenticated operation to its controller.
func confirmGuestUpdateTrial(path string, boot guestUpdateBoot) error {
	trial, err := loadGuestUpdateTrial(path)
	if err != nil {
		return err
	}
	if boot.Attempt == "" || trial.Attempt != boot.Attempt || trial.Candidate != boot.Digest {
		return errGuestUpdateTrialConflict
	}
	if trial.Phase == "confirmed" {
		return nil
	}
	if trial.Phase != "testing" {
		return errGuestUpdateTrialConflict
	}
	trial.Active, trial.Phase = boot.Digest, "confirmed"
	return trial.save(path)
}

func loadGuestUpdateTrial(path string) (guestUpdateTrial, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return guestUpdateTrial{}, nil
	}
	if err != nil {
		return guestUpdateTrial{}, err
	}
	defer file.Close()
	var trial guestUpdateTrial
	if err := decodeAgentJSON(file, &trial); err != nil || !trial.valid() {
		return guestUpdateTrial{}, errors.New("invalid persisted guest update trial")
	}
	return trial, nil
}

func (t guestUpdateTrial) valid() bool {
	if t.Candidate == ([sha256.Size]byte{}) {
		return false
	}
	switch t.Phase {
	case "requested":
		return t.Attempt == "" && t.Active != t.Candidate
	case "testing", "failed":
		return t.Attempt != "" && t.Active != t.Candidate
	case "confirmed":
		return t.Attempt != "" && t.Active == t.Candidate
	default:
		return false
	}
}

func (t guestUpdateTrial) save(path string) error {
	data, err := json.Marshal(t)
	if err != nil {
		return err
	}
	return writePrivateState(path, data)
}
