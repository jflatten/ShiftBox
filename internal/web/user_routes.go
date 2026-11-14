package web

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"time"

	"github.com/jflatten/ShiftBox/internal/auth"
)

// ListUsersHandler returns membership records to authorized administrators.
// It does not read Dex identities or credential records.
func ListUsersHandler(db *sql.DB, origin string, now func() time.Time) AuthorizedHandler {
	return AuthorizedHandler{
		DB: db, Origin: origin, Now: now,
		Route: ManagementRoute{Method: http.MethodGet, Path: "/admin/users", OperationID: "listUsers", Action: "user.list"},
		Resolve: func(context.Context, *sql.Tx, *http.Request, auth.Principal) (AuthorizationTarget, error) {
			return AuthorizationTarget{Action: "user.list"}, nil
		},
		Apply: listUsers,
	}
}

func listUsers(ctx context.Context, tx *sql.Tx, _ *http.Request, _ auth.Principal, _ AuthorizationTarget, _ auth.Decision) (http.Handler, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id,primary_email,display_name,role,membership_state,
		credential_mode,must_change_password,version FROM users ORDER BY primary_email,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	users := []auth.User{}
	for rows.Next() {
		var user auth.User
		if err := rows.Scan(&user.ID, &user.PrimaryEmail, &user.DisplayName, &user.Role,
			&user.MembershipState, &user.CredentialMode, &user.MustChangePassword, &user.Version); err != nil {
			return nil, err
		}
		users = append(users, user)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(users)
	}), nil
}
