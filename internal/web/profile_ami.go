package web

import (
	"context"
	"database/sql"
	"errors"

	"github.com/jflatten/ShiftBox/internal/release"
)

// resolveAMI checks profile compatibility and returns an available image to
// anchor the template's immutable revision. Family launches resolve again.
func (input profileInput) resolveAMI(ctx context.Context, tx *sql.Tx, policy WorkspaceLaunchPolicy) (string, error) {
	if policy.Capabilities.Name != "ec2" {
		if input.PlatformArtifactID != "" || input.AMIFamily != "" {
			return "", resourceProfileUnsupported()
		}
		return "", nil
	}
	instance, offered := policy.Capabilities.LookupInstanceType(input.InstanceType)
	if !offered {
		return "", resourceProfileUnsupported()
	}
	if input.AMIFamily != "" {
		if input.PlatformArtifactID != "" || !release.ValidEC2FamilyName(input.AMIFamily) {
			return "", Problem{Status: 400, Code: "infrastructure.invalid_ami_family", Title: "Choose one AMI family"}
		}
		id, err := selectCurrentEC2FamilyImage(ctx, tx, policy, input.AMIFamily, instance.Architecture, input.DiskMiB)
		if errors.Is(err, sql.ErrNoRows) {
			return "", Problem{Status: 409, Code: "infrastructure.resource_profile_image_incompatible",
				Title: "Choose an AMI family with an image compatible with the instance type and EBS disk size"}
		}
		return id, err
	}
	images, _, err := templateCatalogImages(ctx, tx, policy)
	if err != nil {
		return "", err
	}
	for _, image := range images {
		if image.AMIFamily == "" && image.ID == input.PlatformArtifactID && image.Architecture == instance.Architecture &&
			input.DiskMiB/1024 >= image.MinimumDiskGiB {
			return image.ID, nil
		}
	}
	return "", Problem{Status: 409, Code: "infrastructure.resource_profile_image_incompatible",
		Title: "Choose an available AMI compatible with the instance type and EBS disk size"}
}
