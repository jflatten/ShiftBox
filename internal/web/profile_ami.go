package web

import (
	"context"
	"database/sql"
)

// validateAMI admits only an approved platform image matching the profile's
// instance architecture. The controller repeats this check at workspace launch.
func (input profileInput) validateAMI(ctx context.Context, tx *sql.Tx, policy WorkspaceLaunchPolicy) error {
	if policy.Capabilities.Name != "ec2" {
		if input.PlatformArtifactID != "" {
			return resourceProfileUnsupported()
		}
		return nil
	}
	instance, offered := policy.Capabilities.LookupInstanceType(input.InstanceType)
	if !offered {
		return resourceProfileUnsupported()
	}
	images, _, err := templateCatalogImages(ctx, tx, policy)
	if err != nil {
		return err
	}
	for _, image := range images {
		if image.ID == input.PlatformArtifactID && image.Architecture == instance.Architecture &&
			input.DiskMiB/1024 >= image.MinimumDiskGiB {
			return nil
		}
	}
	return Problem{Status: 409, Code: "infrastructure.resource_profile_image_incompatible",
		Title: "Choose an available AMI compatible with the instance type and EBS disk size"}
}
