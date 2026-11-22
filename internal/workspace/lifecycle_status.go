package workspace

import (
	"slices"
	"time"

	"github.com/jflatten/ShiftBox/internal/operation"
)

// LifecycleStep identifies the displayed milestone a journal step works toward.
type LifecycleStep struct {
	Key       string
	Milestone string
	Confirms  bool
}

// LifecycleSteps is the shared mapping for workspace reads and worker deadlines.
// Steps within a displayed transition share its milestone until it is confirmed.
func LifecycleSteps() []LifecycleStep {
	return []LifecycleStep{
		{"provision", "provisioned", true},
		{"enroll", "vm-running", true},
		{"start", "vm-running", true},
		{"configure", "container-ready", true},
		{"verify_ide", "ready", true},
		{"repair_enter", "repair-ready", false},
		{"verify_repair", "repair-ready", true},
		{"shutdown", "stopped", false},
		{"stop", "stopped", true},
		{"delete", "deleted", true},
		{"maintenance_boot", "vm-running", true},
		{"maintenance_stage", "updated", false},
		{"maintenance_activate", "updated", false},
		{"maintenance_verify", "updated", true},
		{"maintenance_restore", "stopped", true},
	}
}

func lifecycleTransitionLimit(op operation.Operation, index int) operation.TransitionLimit {
	if op.WorkspaceID == nil {
		return operation.TransitionLimit{}
	}
	steps := LifecycleSteps()
	key := op.Steps[index].Key
	target := slices.IndexFunc(steps, func(step LifecycleStep) bool { return step.Key == key })
	if target < 0 {
		return operation.TransitionLimit{}
	}
	first := index
	for first > 0 {
		previousKey := op.Steps[first-1].Key
		sameTransition := slices.ContainsFunc(steps, func(step LifecycleStep) bool {
			return step.Key == previousKey && step.Milestone == steps[target].Milestone && !step.Confirms
		})
		if !sameTransition {
			break
		}
		first--
	}
	return operation.TransitionLimit{
		FirstStep: first,
		Duration:  lifecycleTransitionDuration(op, op.Steps[first].Key),
	}
}

// Measured values are twice the highest scenario P95 recorded for the
// transition in the feature research. The marked estimates are provisional.
func lifecycleTransitionDuration(op operation.Operation, key string) time.Duration {
	switch key {
	case "provision":
		return 11_148 * time.Millisecond
	case "enroll":
		return 39_360 * time.Millisecond
	case "start":
		return 18_392 * time.Millisecond
	case "configure":
		return 198_042 * time.Millisecond
	case "verify_ide":
		return 1_604 * time.Millisecond
	case "repair_enter", "verify_repair":
		return 95_386 * time.Millisecond
	case "shutdown":
		if op.OriginatingAction == retentionDeleteAction {
			return 3 * time.Minute // Provisional, including the forced-stop grace.
		}
		if op.Type == "delete" {
			return 147_578 * time.Millisecond
		}
		return 139_620 * time.Millisecond
	case "stop":
		return 46_042 * time.Millisecond
	case "delete":
		return 18_936 * time.Millisecond
	case "maintenance_boot":
		return 3 * time.Minute // Provisional, including authenticated re-enrollment.
	case "maintenance_stage", "maintenance_activate", "maintenance_verify":
		return 5 * time.Minute // Provisional, including trial activation and guest contact.
	case "maintenance_restore":
		return 2 * time.Minute // Provisional, including a provider stop and verification.
	default:
		return 0
	}
}
