package release

import (
	"context"
	"errors"
)

// GuestUpgradeBlocker identifies a retained workspace with incompatible
// guest evidence or unresolved maintenance.
type GuestUpgradeBlocker struct {
	WorkspaceID     string `json:"workspaceId"`
	ObservedState   string `json:"observedState"`
	GuestRelease    string `json:"guestRelease"`
	ProtocolVersion string `json:"protocolVersion"`
}

// GuestUpgradeBlockers requires the current release and settled maintenance
// before the controller can advance. Only verified deletion removes a target.
func (s *Store) GuestUpgradeBlockers(ctx context.Context, installationID, deployedRelease, protocol string) ([]GuestUpgradeBlocker, error) {
	if !hasMetadata(installationID, deployedRelease, protocol) {
		return nil, errors.New("guest upgrade check requires installation, deployed release, and protocol")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT w.id,w.observed_state,COALESCE(w.guest_release,''),COALESCE(w.protocol_version,'')
		FROM workspaces w WHERE w.installation_id=$1 AND w.observed_state<>'deleted'
		AND (COALESCE(w.guest_release,'')<>$2 OR COALESCE(w.protocol_version,'')<>$3 OR EXISTS (
			SELECT 1 FROM maintenance_targets t JOIN maintenance_campaigns c ON c.operation_id=t.campaign_id
			WHERE c.installation_id=w.installation_id AND t.workspace_id=w.id AND t.generation=w.generation
			AND c.state IN ('active','blocked') AND t.state<>'completed'))
		ORDER BY w.id`, installationID, deployedRelease, protocol)
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
