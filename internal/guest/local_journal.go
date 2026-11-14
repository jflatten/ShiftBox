package guest

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/google/uuid"
	"github.com/jflatten/ShiftBox/internal/release"
)

var errGuestDispatchConflict = errors.New("guest operation dispatch is stale or conflicts with local work")

const rebuildDevelopmentCommand = "rebuild-development-container"
const retryAfterRepairCommand = "retry-development-after-repair"

type guestDispatch struct {
	ID             string `json:"id"`
	Generation     int64  `json:"generation"`
	Version        int64  `json:"version"`
	Command        string `json:"command"`
	Config         string `json:"config,omitempty"`
	Repository     string `json:"repository,omitempty"`
	Ref            string `json:"ref,omitempty"`
	ProductRelease string `json:"productRelease,omitempty"`
}

func (d guestDispatch) valid() bool {
	id, err := uuid.Parse(d.ID)
	if err != nil || id == uuid.Nil || id.String() != d.ID || d.Generation < 1 || d.Version < 1 {
		return false
	}
	if d.Command == stageGuestUpdateCommand {
		return d.Config == "" && d.Repository == "" && d.Ref == "" && release.ValidMigrationReleaseID(d.ProductRelease)
	}
	if d.ProductRelease != "" {
		return false
	}
	switch d.Command {
	case "ensure-development-container", rebuildDevelopmentCommand, retryAfterRepairCommand:
		return d.Config == "" || validDevContainerPath(d.Config)
	case "shutdown-for-stop", enterRepairCommand, exitRepairCommand:
		return d.Config == "" && d.Repository == "" && d.Ref == ""
	default:
		return false
	}
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
	problemRepository             = "guest.repository_preparation_failed"
	problemRequired               = "guest.required_setup_failed"
	problemIDE                    = "guest.ide_start_failed"
	problemRemaining              = "guest.later_setup_failed"
)

type localOperation struct {
	guestDispatch
	Attempt      int                 `json:"attempt"`
	Phase        localPhase          `json:"phase"`
	Connection   containerConnection `json:"connection"`
	Problem      string              `json:"problem,omitempty"`
	UpdateDigest string              `json:"updateDigest,omitempty"`
}

type localJournalState struct {
	Generation int64           `json:"generation"`
	Operation  *localOperation `json:"operation"`
}

// localJournal belongs to the single agent operation loop. Write the durable
// state before replacing the in-memory view or acknowledging progress.
type localJournal struct {
	path           string
	state          localJournalState
	downloadUpdate func(context.Context, string) ([sha256.Size]byte, error)
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
			bound := current.guestDispatch
			bound.Version = dispatch.Version
			if dispatch.Version < current.Version || dispatch != bound {
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
	if op.Command == stageGuestUpdateCommand {
		if !noConnection {
			return false
		}
		switch op.Phase {
		case localPending:
			return op.Problem == "" && op.UpdateDigest == ""
		case localSucceeded:
			return op.Problem == "" && validStagedUpdateDigest(op.UpdateDigest)
		case localFailed:
			return op.Problem == problemGuestUpdateStage && op.UpdateDigest == ""
		default:
			return false
		}
	}
	if op.UpdateDigest != "" {
		return false
	}
	if op.Command == enterRepairCommand || op.Command == exitRepairCommand {
		switch op.Phase {
		case localPending, localRequired:
			return noConnection && op.Problem == ""
		case localSucceeded:
			return op.Problem == "" && op.Connection.WaitFor == "" &&
				(op.Command == enterRepairCommand && dockerContainerID.MatchString(op.Connection.ID) || op.Command == exitRepairCommand && noConnection)
		case localFailed:
			return noConnection && op.Problem == problemRepairFailed
		default:
			return false
		}
	}
	if op.Command == "shutdown-for-stop" {
		if !noConnection {
			return false
		}
		switch op.Phase {
		case localPending, localRequired, localSucceeded:
			return op.Problem == ""
		case localFailed:
			return op.Problem == problemShutdownTimeout || op.Problem == problemShutdownFailed
		default:
			return false
		}
	}
	switch op.Phase {
	case localPending, localRequired:
		return noConnection && op.Problem == ""
	case localWaitReached, localRemaining, localSucceeded:
		return hasConnection && op.Problem == ""
	case localFailed:
		switch op.Problem {
		case problemRequired, problemRepository, problemIDE:
			return noConnection
		case problemRemaining:
			return hasConnection
		case problemInterrupted:
			return hasConnection || noConnection
		}
	}
	return false
}
