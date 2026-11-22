package provider

import "errors"

// Semantic errors let the controller decide whether to inspect, wait, or block.
var (
	ErrUnavailable           = errors.New("provider unavailable")
	ErrOutcomeUnknown        = errors.New("provider outcome unknown")
	ErrNotFound              = errors.New("provider resource not found")
	ErrOwnershipMismatch     = errors.New("provider ownership or generation mismatch")
	ErrCapabilityUnsupported = errors.New("provider capability unsupported")
	ErrInvalidState          = errors.New("invalid provider state")
	ErrPartial               = errors.New("provider operation partially completed")
	ErrPermission            = errors.New("provider permission denied")
)
