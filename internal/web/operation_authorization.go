package web

import (
	"errors"
	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/operation"
)

// OperationViewTarget uses the loaded workspace's current visibility rules or
// the immutable administrative action persisted when installation intent began.
// The caller loads both records in the authorization transaction, including on
// SSE authority rechecks. Missing originating evidence fails closed.
func OperationViewTarget(op operation.Operation, resource auth.Resource) (AuthorizationTarget, error) {
	if op.WorkspaceID != nil {
		if resource.Type != "workspace" || resource.ID != *op.WorkspaceID || resource.ID == "" || resource.OwnerID == "" {
			return AuthorizationTarget{}, errors.New("operation workspace context does not match")
		}
		return AuthorizationTarget{Action: "workspace.view", Resource: resource}, nil
	}
	if resource.Type != "installation" || resource.ID == "" {
		return AuthorizationTarget{}, errors.New("operation installation context is missing")
	}
	switch op.OriginatingAction {
	case "setup.mutate", "infrastructure.manage", "maintenance.manage", "backup.create", "restore.validate", "restore.activate", "recovery.activate", "migration.preflight":
		return AuthorizationTarget{Action: op.OriginatingAction, Resource: resource}, nil
	default:
		return AuthorizationTarget{}, errors.New("operation originating authority is missing or invalid")
	}
}
