package guest

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/google/uuid"
)

var errGuestDispatchConflict = errors.New("guest operation dispatch is stale or conflicts with local work")

type guestDispatch struct {
	ID         string `json:"id"`
	Generation int64  `json:"generation"`
	Version    int64  `json:"version"`
	Command    string `json:"command"`
	Config     string `json:"config,omitempty"`
}

func (d guestDispatch) valid() bool {
	id, err := uuid.Parse(d.ID)
	return err == nil && id != uuid.Nil && id.String() == d.ID &&
		d.Generation > 0 && d.Version > 0 && d.Command == "ensure-development-container" &&
		(d.Config == "" || validDevContainerPath(d.Config))
}

type localPhase string

const (
	localPending       localPhase = "pending"
	localRequired      localPhase = "required-running"
	localWaitReached   localPhase = "wait-for-reached"
	localRemaining     localPhase = "remaining-running"
	localSucceeded     localPhase = "succeeded"
	localFailed        localPhase = "failed"
	problemInterrupted            = "guest.lifecycle_interrupted"
	problemRequired               = "guest.required_setup_failed"
	problemRemaining              = "guest.later_setup_failed"
)

type localOperation struct {
	guestDispatch
	Attempt    int                 `json:"attempt"`
	Phase      localPhase          `json:"phase"`
	Connection containerConnection `json:"connection"`
	Problem    string              `json:"problem,omitempty"`
}

type localJournalState struct {
	Generation int64           `json:"generation"`
	Operation  *localOperation `json:"operation"`
}

// localJournal belongs to the single agent operation loop. Write the durable
// state before replacing the in-memory view or acknowledging progress.
type localJournal struct {
	path  string
	state localJournalState
}

func loadLocalJournal(path string) (*localJournal, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("invalid guest operation journal path")
	}
	journal := &localJournal{path: path}
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return journal, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	if err := decodeAgentJSON(file, &journal.state); err != nil || !journal.state.valid() {
		return nil, errors.New("invalid persisted guest operation")
	}
	return journal, nil
}

func (j *localJournal) advanceGeneration(generation int64) error {
	if generation < 1 || generation < j.state.Generation {
		return errGuestDispatchConflict
	}
	if generation == j.state.Generation {
		return nil
	}
	return j.save(localJournalState{Generation: generation})
}

func (j *localJournal) accept(dispatch guestDispatch) error {
	if !dispatch.valid() || dispatch.Generation < j.state.Generation {
		return errGuestDispatchConflict
	}
	current := j.state.Operation
	if current != nil && dispatch.Generation == j.state.Generation {
		if current.ID == dispatch.ID {
			if dispatch.Version < current.Version || dispatch.Command != current.Command || dispatch.Config != current.Config {
				return errGuestDispatchConflict
			}
			if dispatch.Version == current.Version {
				return nil
			}
			next := *current
			next.Version = dispatch.Version
			return j.saveOperation(next)
		}
		if current.Phase != localSucceeded && current.Phase != localFailed {
			return errGuestDispatchConflict
		}
	}
	return j.saveOperation(localOperation{guestDispatch: dispatch, Attempt: 1, Phase: localPending})
}

func (j *localJournal) saveOperation(op localOperation) error {
	return j.save(localJournalState{Generation: op.Generation, Operation: &op})
}

func (j *localJournal) save(state localJournalState) error {
	if !state.valid() {
		return errors.New("invalid guest operation journal transition")
	}
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if err := writePrivateState(j.path, data); err != nil {
		return err
	}
	j.state = state
	return nil
}

func (s localJournalState) valid() bool {
	if s.Generation < 1 {
		return false
	}
	op := s.Operation
	if op == nil {
		return true
	}
	if !op.guestDispatch.valid() || op.Generation != s.Generation || op.Attempt < 1 {
		return false
	}
	hasConnection := dockerContainerID.MatchString(op.Connection.ID) && validWaitFor(op.Connection.WaitFor)
	noConnection := op.Connection == (containerConnection{})
	switch op.Phase {
	case localPending, localRequired:
		return noConnection && op.Problem == ""
	case localWaitReached, localRemaining, localSucceeded:
		return hasConnection && op.Problem == ""
	case localFailed:
		switch op.Problem {
		case problemRequired:
			return noConnection
		case problemRemaining:
			return hasConnection
		case problemInterrupted:
			return hasConnection || noConnection
		}
	}
	return false
}
