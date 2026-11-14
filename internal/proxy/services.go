package proxy

import "errors"

// Service is a named HTTP/WebSocket endpoint offered through the guest tunnel.
// Targets are deliberately absent: the controller opens a named tunnel stream,
// never a browser-selected host or port on the controller's network.
type Service struct {
	Key       string
	Protocols []string
}

// ServiceAllowlist is an immutable snapshot bound to an authenticated registration.
type ServiceAllowlist struct {
	workspaceID string
	generation  int64
	services    map[string]map[string]bool
}

// NewServiceAllowlist validates service declarations. Workspace identity and
// generation must come from verified controller records, not guest message fields.
func NewServiceAllowlist(workspaceID string, generation int64, services []Service) (*ServiceAllowlist, error) {
	if workspaceID == "" || generation <= 0 {
		return nil, errors.New("missing authenticated service registration")
	}
	allowed := make(map[string]map[string]bool, len(services))
	for _, service := range services {
		if service.Key == "" || len(service.Protocols) == 0 || allowed[service.Key] != nil {
			return nil, errors.New("invalid or duplicate service declaration")
		}
		protocols := make(map[string]bool, len(service.Protocols))
		for _, protocol := range service.Protocols {
			if protocol != "http" && protocol != "websocket" {
				return nil, errors.New("unsupported service protocol")
			}
			protocols[protocol] = true
		}
		allowed[service.Key] = protocols
	}
	return &ServiceAllowlist{workspaceID: workspaceID, generation: generation, services: allowed}, nil
}

// Allows checks the exact workspace generation, service, and protocol before a
// stream is opened. Callers must also check current user authority and guest trust.
func (a *ServiceAllowlist) Allows(workspaceID string, generation int64, serviceKey, protocol string) bool {
	return a != nil && a.workspaceID == workspaceID && a.generation == generation && a.services[serviceKey][protocol]
}
