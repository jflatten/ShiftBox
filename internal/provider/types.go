package provider

import "time"

type PowerState string

const (
	Unknown  PowerState = "unknown"
	Stopped  PowerState = "stopped"
	Starting PowerState = "starting"
	Running  PowerState = "running"
	Stopping PowerState = "stopping"
	Deleted  PowerState = "deleted"
)

type WorkspaceRef struct {
	InstallationID, WorkspaceID string
	Generation                  int64
	InstanceID, OwnershipMarker string
}

type Resources struct {
	CPU                    int
	MemoryBytes, DiskBytes int64
}

type Capabilities struct {
	Name, Version, Architecture, ExecutionMode, Evidence string
	Create, Start, Stop, Delete                          bool
	NetworkModes, RecoveryMechanisms                     []string
	NetworkStoppedOnly                                   bool
	Minimum, Maximum                                     Resources
}

type OwnedResource struct {
	ID, Type, OwnershipMarker string
	Generation                int64
	Immutable, Shared         bool
	HostBytes, GuestBytes     *int64
}

type Inventory struct {
	Ref                                             WorkspaceRef
	InstanceID                                      string
	Power                                           PowerState
	Resources                                       []OwnedResource
	Effective                                       Resources
	DiskCapacityKnown                               bool
	NetworkMode, ArtifactID, Release, DiagnosticRef string
	ObservedAt                                      time.Time
}

type CreateRequest struct {
	Ref                                           WorkspaceRef
	OperationID, ArtifactID, Release, NetworkMode string
	Resources                                     Resources
	EnrollmentMedia                               string
}

type StartRequest struct {
	Ref                      WorkspaceRef
	OperationID, NetworkMode string
	RequiresEnrollment       bool
	// EnrollmentMedia is the controller's journaled image for this start.
	EnrollmentMedia string
}

type StopRequest struct {
	Ref                  WorkspaceRef
	OperationID          string
	ForceRetentionDelete bool
}

type DeleteRequest struct {
	Ref         WorkspaceRef
	OperationID string
}

type NetworkRequest struct {
	Ref               WorkspaceRef
	OperationID, Mode string
}

type EnrollmentRequest struct {
	Ref                    WorkspaceRef
	OperationID, MediaPath string
	Eligible               bool
}

type DeliveryResult struct {
	Inventory Inventory
	MediaID   string
}
