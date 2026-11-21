package provider

import (
	"slices"
	"time"
)

// PowerState is the provider-observed lifecycle state of the workspace VM.
type PowerState string

const (
	// Unknown indicates that the provider could not establish the power state.
	Unknown PowerState = "unknown"
	// Stopped indicates that the VM is powered off and may retain its disk.
	Stopped PowerState = "stopped"
	// Starting indicates that the provider has observed a boot in progress.
	Starting PowerState = "starting"
	// Running indicates that the VM is powered on. Guest readiness is checked separately.
	Running PowerState = "running"
	// Stopping indicates that shutdown has begun and still needs verification.
	Stopping PowerState = "stopping"
	// Deleted indicates verified removal of the VM and its workspace-owned resources.
	Deleted PowerState = "deleted"
)

// WorkspaceRef binds provider work to an installation, workspace generation,
// and ownership marker. An observed instance ID identifies the specific VM.
type WorkspaceRef struct {
	InstallationID, WorkspaceID string
	Generation                  int64
	InstanceID, OwnershipMarker string
}

// Resources describes an optional machine type, CPU count, and memory and disk capacity in bytes.
type Resources struct {
	InstanceType           string
	CPU                    int
	MemoryBytes, DiskBytes int64
}

// InstanceType identifies a provider-offered compute configuration.
type InstanceType struct {
	Architecture string `json:"architecture"`
	Name         string `json:"name"`
	CPUCount     int    `json:"cpuCount"`
	MemoryMiB    int64  `json:"memoryMiB"`
}

// Capabilities describes the adapter's validated actions and host resource bounds.
type Capabilities struct {
	Name, Version, Architecture, ExecutionMode, Evidence string
	// Architectures lists supported image architectures. Single-architecture
	// providers may supply Architecture alone.
	Architectures                    []string
	Create, Start, Stop, Delete      bool
	NetworkModes, RecoveryMechanisms []string
	NetworkStoppedOnly               bool
	Minimum, Maximum                 Resources
	InstanceTypes                    []InstanceType
	OfficialImage                    *OfficialAMI
}

// OfficialAMI identifies the available release catalog for an EC2 deployment.
type OfficialAMI struct {
	ReleaseID string `json:"releaseId"`
	Region    string `json:"region"`
	AMIID     string `json:"amiId"`
}

// LookupInstanceType retrieves the compute configuration offered to new launches.
func (c Capabilities) LookupInstanceType(name string) (InstanceType, bool) {
	for _, instance := range c.InstanceTypes {
		if instance.Name == name && c.SupportsArchitecture(instance.Architecture) {
			return instance, true
		}
	}
	return InstanceType{}, false
}

// SupportsArchitecture reports whether the provider can run an image for the given architecture.
func (c Capabilities) SupportsArchitecture(architecture string) bool {
	if len(c.Architectures) != 0 {
		return slices.Contains(c.Architectures, architecture)
	}
	return architecture != "" && architecture == c.Architecture
}

// OwnedResource describes one inventoried VM resource and its ownership.
// Shared and immutable resources are excluded from workspace deletion.
type OwnedResource struct {
	ID, Type, OwnershipMarker string
	Generation                int64
	Immutable, Shared         bool
	HostBytes, GuestBytes     *int64
}

// Inventory records the provider evidence used to reconcile desired workspace
// state and capacity. ObservedAt is the time of the provider check.
type Inventory struct {
	EC2Enrollment                                   bool
	Ref                                             WorkspaceRef
	InstanceID                                      string
	Power                                           PowerState
	Resources                                       []OwnedResource
	Effective                                       Resources
	DiskCapacityKnown                               bool
	NetworkMode, ArtifactID, Release, DiagnosticRef string
	ObservedAt                                      time.Time
}

// CreateRequest identifies an admitted VM provision operation and pinned image.
type CreateRequest struct {
	Ref                                           WorkspaceRef
	OperationID, ArtifactID, Release, NetworkMode string
	Resources                                     Resources
	EnrollmentMedia                               string
	BootstrapData                                 string
}

// StartRequest identifies an authorized start of the exact retained VM.
type StartRequest struct {
	Ref                      WorkspaceRef
	OperationID, NetworkMode string
	RequiresEnrollment       bool
	// EnrollmentMedia is the controller's journaled image for this start.
	EnrollmentMedia string
}

// StopRequest identifies a stop of the exact VM. ForceRetentionDelete applies
// only to the authorized retention cleanup path after its graceful window.
type StopRequest struct {
	Ref                  WorkspaceRef
	OperationID          string
	ForceRetentionDelete bool
}

// DeleteRequest identifies a durable cleanup operation for the exact VM.
type DeleteRequest struct {
	Ref         WorkspaceRef
	OperationID string
}

// NetworkRequest carries the admitted network mode for a particular VM.
type NetworkRequest struct {
	Ref               WorkspaceRef
	OperationID, Mode string
}

// EnrollmentRequest carries an operation-scoped recovery image for an
// eligible stopped VM.
type EnrollmentRequest struct {
	Ref                    WorkspaceRef
	OperationID, MediaPath string
	Eligible               bool
	Seed                   []byte
}

// DeliveryResult records the provider-confirmed drive and resulting VM inventory.
type DeliveryResult struct {
	Inventory Inventory
	MediaID   string
}
