package operation

import (
	"errors"
	"time"
)

// ErrConflict identifies a stale generation, worker claim, or workflow transition.
var ErrConflict = errors.New("operation state or worker claim changed")

// Step is persisted workflow evidence rendered by the management Stepper.
type Step struct {
	Key     string       `json:"key"`
	Label   string       `json:"label"`
	Status  string       `json:"status"`
	Index   int          `json:"index"`
	Detail  string       `json:"detail,omitempty"`
	Timeout *StepTimeout `json:"timeout,omitempty"`

	startedAt          *time.Time
	transitionDeadline *time.Time
}

// StepTimeout records an expired transition without changing the step's status.
type StepTimeout struct {
	OccurredAt time.Time `json:"occurredAt"`
	Reason     string    `json:"reason"`
}

// Operation is authoritative workflow state; worker credentials stay server-side.
type Operation struct {
	// OriginatingAction is controller-owned admission evidence, never browser input.
	OriginatingAction string     `json:"-"`
	ID                string     `json:"id"`
	WorkspaceID       *string    `json:"workspaceId,omitempty"`
	Type              string     `json:"type"`
	Generation        int64      `json:"-"`
	Actor             string     `json:"-"`
	RequestKey        string     `json:"-"`
	Status            string     `json:"status"`
	CurrentStep       *string    `json:"currentStep"`
	Steps             []Step     `json:"steps"`
	CreatedAt         time.Time  `json:"createdAt"`
	StartedAt         *time.Time `json:"startedAt"`
	FinishedAt        *time.Time `json:"finishedAt"`
	Version           int64      `json:"version"`
	leaseOwner        string
	leaseExpiresAt    time.Time
}

// Request describes intent that must commit before a provider or guest effect.
type Request struct {
	OriginatingAction string
	Actor             string
	Key               string
	Type              string
	WorkspaceID       *string
	Generation        int64
	Steps             []Step
}

// Claim fences one worker attempt, including reuse of the same worker name.
type Claim struct {
	OperationID string
	Generation  int64
	Token       string
}

// Event combines the committed snapshot and bounded non-secret completion evidence.
type Event struct {
	ID         int64     `json:"eventId"`
	Operation  Operation `json:"operation"`
	Evidence   string    `json:"evidence,omitempty"`
	OccurredAt time.Time `json:"occurredAt"`
}
