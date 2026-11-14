package release

import (
	"context"
	"errors"
)

// GuestUpgradeBlocker identifies a retained workspace whose last accepted
// guest poll does not establish compatibility with the deployed release.
type GuestUpgradeBlocker struct {
	WorkspaceID     string
	ObservedState   string
	GuestRelease    string
	ProtocolVersion string
}

// GuestUpgradeBlockers reports which retained workspaces still need to reach
// the currently deployed release before the controller can advance.
func (s *Store) GuestUpgradeBlockers(ctx context.Context, installationID, deployedRelease, protocol string) ([]GuestUpgradeBlocker, error) {
	if !hasMetadata(installationID, deployedRelease, protocol) {
		return nil, errors.New("guest upgrade check requires installation, deployed release, and protocol")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,observed_state,COALESCE(guest_release,''),COALESCE(protocol_version,'')
		FROM workspaces WHERE installation_id=$1 AND observed_state<>'deleted'
		AND (COALESCE(guest_release,'')<>$2 OR COALESCE(protocol_version,'')<>$3)
		ORDER BY id`, installationID, deployedRelease, protocol)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var blockers []GuestUpgradeBlocker
	for rows.Next() {
		var blocker GuestUpgradeBlocker
		if err := rows.Scan(&blocker.WorkspaceID, &blocker.ObservedState, &blocker.GuestRelease, &blocker.ProtocolVersion); err != nil {
			return nil, err
		}
		blockers = append(blockers, blocker)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return blockers, nil
}
