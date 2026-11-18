package web

import (
	"context"
	"database/sql"
	"encoding/json"
	"slices"

	"github.com/jflatten/ShiftBox/internal/release"
)

// selectEC2FamilyImage considers only validated family members and orders them
// by AWS creation time so publishing an older image cannot displace a patch.
func selectEC2FamilyImage(ctx context.Context, tx *sql.Tx, policy WorkspaceLaunchPolicy,
	family, architecture string, diskMiB int64, current, previous string,
) (string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id,protocol_version,ec2_image_json FROM platform_artifacts
		WHERE ec2_family=$1 AND ec2_family<>'' AND provider_kind='ec2' AND state='available' AND ec2_retired=0
		  AND architecture=$2 AND guest_release<>'' AND guest_release IN ($3,$4)
		ORDER BY ec2_created_at DESC,id`, family, architecture, current, previous)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	for rows.Next() {
		var id, protocol, raw string
		if err := rows.Scan(&id, &protocol, &raw); err != nil {
			return "", err
		}
		var image release.EC2Image
		if slices.Contains(policy.Protocols, protocol) && json.Unmarshal([]byte(raw), &image) == nil &&
			image.AMIID != "" && image.MinimumRootDiskGiB > 0 && diskMiB/1024 >= int64(image.MinimumRootDiskGiB) {
			return id, nil
		}
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	return "", sql.ErrNoRows
}

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

func selectCurrentEC2FamilyImage(ctx context.Context, tx *sql.Tx, policy WorkspaceLaunchPolicy, family, architecture string, diskMiB int64) (string, error) {
	var current, previous string
	if err := tx.QueryRowContext(ctx, `SELECT active_release,min_guest_release FROM installations WHERE singleton=1`).
		Scan(&current, &previous); err != nil {
		return "", err
	}
	return selectEC2FamilyImage(ctx, tx, policy, family, architecture, diskMiB, current, previous)
}

func ec2ImageVerified(allowed []string, raw string) bool {
	var image struct {
		ID string `json:"amiId"`
	}
	return json.Unmarshal([]byte(raw), &image) == nil && image.ID != "" && slices.Contains(allowed, image.ID)
}
