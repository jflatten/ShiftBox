package web

import (
	"context"
	"database/sql"
	"encoding/json"
	"slices"
)

// verifyEC2LaunchImage accepts only the selected profile AMI for the instance architecture.
func verifyEC2LaunchImage(ctx context.Context, tx *sql.Tx, policy WorkspaceLaunchPolicy, artifactID, architecture, current, previous string) (bool, error) {
	var protocol, image string
	err := tx.QueryRowContext(ctx, `SELECT protocol_version,ec2_image_json FROM platform_artifacts
		WHERE id=$1 AND provider_kind='ec2' AND state='available' AND architecture=$2
		  AND guest_release<>'' AND guest_release IN ($3,$4)`,
		artifactID, architecture, current, previous).Scan(&protocol, &image)
	if err != nil {
		return false, err
	}
	if !slices.Contains(policy.Protocols, protocol) || !ec2ImageVerified(policy.EC2VerifiedAMIs, image) {
		return false, sql.ErrNoRows
	}
	return ec2ImageVerified([]string{policy.EC2SingleBootAMI}, image), nil
}

func ec2ImageVerified(allowed []string, raw string) bool {
	var image struct {
		ID string `json:"amiId"`
	}
	return json.Unmarshal([]byte(raw), &image) == nil && image.ID != "" && slices.Contains(allowed, image.ID)
}
