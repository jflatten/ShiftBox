package web

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/provider"
	"github.com/jflatten/ShiftBox/internal/workspace"
)

// NetworkPolicyHandler serves the administrator network policy creation route.
func NetworkPolicyHandler(db *sql.DB, origin string, now func() time.Time, newID func() string, caps provider.Capabilities) AuthorizedHandler {
	return AuthorizedHandler{
		DB: db, Origin: origin, Now: now,
		Route: ManagementRoute{Method: http.MethodPost, Path: "/admin/network-policies", OperationID: "createNetworkPolicy", Action: "infrastructure.manage"},
		Resolve: func(context.Context, *sql.Tx, *http.Request, auth.Principal) (AuthorizationTarget, error) {
			return AuthorizationTarget{Action: "infrastructure.manage"}, nil
		},
		Apply: func(ctx context.Context, tx *sql.Tx, r *http.Request, _ auth.Principal, _ AuthorizationTarget, _ auth.Decision) (http.Handler, error) {
			var input workspace.NetworkPolicy
			if err := DecodeJSON(nil, r, &input, 32<<10); err != nil {
				return nil, err
			}
			input.Name = strings.TrimSpace(input.Name)
			err := workspace.CreateNetworkPolicyInTx(ctx, tx, newID(), input, caps)
			switch {
			case errors.Is(err, workspace.ErrNetworkPolicyInvalid):
				return nil, Problem{Status: 400, Code: "infrastructure.invalid_network_policy", Title: "Check the network policy name, mode and rules"}
			case errors.Is(err, provider.ErrCapabilityUnsupported):
				return nil, Problem{Status: 409, Code: "infrastructure.network_policy_unsupported", Title: "This provider cannot enforce the selected network policy"}
			case errors.Is(err, workspace.ErrNetworkPolicyConflict):
				return nil, Problem{Status: 409, Code: "infrastructure.network_policy_conflict", Title: "Choose a different network policy name"}
			case err != nil:
				return nil, err
			}
			return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusCreated)
				_ = json.NewEncoder(w).Encode(input)
			}), nil
		},
	}
}
