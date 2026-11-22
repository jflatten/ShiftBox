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
// before the controller can advance. Verified repair or removal clears a
// failed target without changing its historical record.
func (s *Store) GuestUpgradeBlockers(ctx context.Context, installationID, deployedRelease, protocol string) ([]GuestUpgradeBlocker, error) {
	if !hasMetadata(installationID, deployedRelease, protocol) {
		return nil, errors.New("guest upgrade check requires installation, deployed release, and protocol")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT w.id,w.observed_state,COALESCE(w.guest_release,''),COALESCE(w.protocol_version,'')
		FROM workspaces w WHERE w.installation_id=$1 AND w.observed_state<>'deleted'
		AND (COALESCE(w.guest_release,'')<>$2 OR COALESCE(w.protocol_version,'')<>$3 OR EXISTS (
			SELECT 1 FROM maintenance_targets t JOIN maintenance_campaigns c ON c.operation_id=t.campaign_id
			WHERE c.installation_id=w.installation_id AND t.workspace_id=w.id AND t.generation=w.generation
			AND c.state IN ('active','blocked') AND t.state<>'completed'
			AND NOT (t.state='failed' AND EXISTS (
				SELECT 1 FROM maintenance_targets replacement
				JOIN maintenance_campaigns newer ON newer.operation_id=replacement.campaign_id
				JOIN operations verified ON verified.id=replacement.operation_id
				WHERE replacement.workspace_id=w.id AND replacement.generation=w.generation
				AND verified.workspace_id=w.id AND verified.generation=w.generation
				AND replacement.state='completed' AND newer.installation_id=w.installation_id
				AND newer.created_at>c.created_at AND verified.status='succeeded'
				AND verified.type='update' AND verified.originating_action='maintenance.update_guest'))))
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
