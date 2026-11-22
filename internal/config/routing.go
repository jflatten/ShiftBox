package config

import (
	"errors"
	"net"
	"net/url"
	"regexp"
	"strings"
)

var workspaceLabel = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var serviceLabel = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,23}[a-z0-9])?$`)

// ServiceHosts maps browser hostnames to workspace/service identities, never to
// guest-chosen network addresses. Its validated suffixes cannot change afterward.
type ServiceHosts struct {
	management    string
	authority     string
	port          string
	ideSuffix     string
	previewSuffix string
	repairSuffix  string
}

// NewServiceHosts validates configured origins and the approved fixed templates.
func NewServiceHosts(managementOrigin, idePattern, previewPattern string) (*ServiceHosts, error) {
	if err := validateManagementOrigin(managementOrigin); err != nil {
		return nil, err
	}
	if err := ValidateServiceHostPatterns(idePattern, previewPattern); err != nil {
		return nil, err
	}
	origin, _ := url.Parse(managementOrigin)
	hosts := &ServiceHosts{management: origin.Hostname(), authority: origin.Host, port: origin.Port(),
		ideSuffix: strings.TrimPrefix(idePattern, "{workspace}"), previewSuffix: strings.TrimPrefix(previewPattern, "{service}--{workspace}")}
	suffix := hosts.ideSuffix
	if domain, ok := strings.CutPrefix(suffix, ".ide."); ok {
		suffix = "." + domain
	}
	hosts.repairSuffix = ".repair" + suffix
	if len(hosts.repairSuffix)+36 > 253 {
		return nil, errors.New("repair hostname exceeds the DNS length limit")
	}
	// Host-scoped cookies do not isolate ports, so a different port cannot make
	// a management hostname safe to reuse for guest content.
	if _, _, ok := hosts.Resolve(hosts.management); ok {
		return nil, errors.New("management hostname overlaps a service hostname")
	}
	return hosts, nil
}

// ManagementAuthority returns the exact Host header for the management origin.
func (h *ServiceHosts) ManagementAuthority() string {
	if h == nil {
		return ""
	}
	return h.authority
}

// ServiceAuthority builds the service Host header on the browser HTTPS port.
func (h *ServiceHosts) ServiceAuthority(workspace, service string) (string, error) {
	name, err := h.Hostname(workspace, service)
	if err != nil || h.port == "" {
		return name, err
	}
	return net.JoinHostPort(name, h.port), nil
}

// ResolveAuthority admits a canonical service Host header on the configured port.
func (h *ServiceHosts) ResolveAuthority(authority string) (workspace, service string, ok bool) {
	if h == nil {
		return "", "", false
	}
	if h.port == "" {
		return h.Resolve(authority)
	}
	host, port, err := net.SplitHostPort(authority)
	if err != nil || port != h.port || net.JoinHostPort(host, port) != authority {
		return "", "", false
	}
	return h.Resolve(host)
}

// Hostname constructs one IDE or preview hostname. Service key "ide" selects
// the IDE namespace; other keys fit alongside the UUID in one 63-byte label.
func (h *ServiceHosts) Hostname(workspace, service string) (string, error) {
	if h == nil || !workspaceLabel.MatchString(workspace) || !serviceLabel.MatchString(service) {
		return "", errors.New("invalid workspace or service identity")
	}
	if service == "ide" {
		return workspace + h.ideSuffix, nil
	}
	if service == "repair" {
		return workspace + h.repairSuffix, nil
	}
	return service + "--" + workspace + h.previewSuffix, nil
}

// Resolve accepts a canonical hostname without a port. The caller must validate
// request authority before routing; this function grants no access permission.
func (h *ServiceHosts) Resolve(hostname string) (workspace, service string, ok bool) {
	if h == nil {
		return "", "", false
	}
	if label, found := strings.CutSuffix(hostname, h.ideSuffix); found && workspaceLabel.MatchString(label) {
		return label, "ide", true
	}
	if label, found := strings.CutSuffix(hostname, h.repairSuffix); found && workspaceLabel.MatchString(label) {
		return label, "repair", true
	}
	label, found := strings.CutSuffix(hostname, h.previewSuffix)
	// Read the fixed-width UUID from the end so hyphens inside service keys
	// (including consecutive hyphens) cannot change the workspace mapping.
	if !found || len(label) < 39 {
		return "", "", false
	}
	boundary := len(label) - 38
	workspace, service = label[boundary+2:], label[:boundary]
	if label[boundary:boundary+2] != "--" || !workspaceLabel.MatchString(workspace) || !serviceLabel.MatchString(service) || service == "ide" || service == "repair" {
		return "", "", false
	}
	return workspace, service, true
}
