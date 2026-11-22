package provider

import "context"

// Provider is the controller's boundary for observed machine state and VM
// effects. An uncertain effect leaves the controller responsible for
// re-inspecting the workspace before retrying or releasing reservations.
type Provider interface {
	Capabilities(context.Context) (Capabilities, error)
	Inspect(context.Context, WorkspaceRef) (Inventory, error)
	Create(context.Context, CreateRequest) (Inventory, error)
	Start(context.Context, StartRequest) (Inventory, error)
	Stop(context.Context, StopRequest) (Inventory, error)
	Delete(context.Context, DeleteRequest) (Inventory, error)
	ApplyNetwork(context.Context, NetworkRequest) (Inventory, error)
	DeliverEnrollment(context.Context, EnrollmentRequest) (DeliveryResult, error)
}
