// Package workspacessh defines the workstation-to-controller SSH wire format.
package workspacessh

import "net/url"

// Subprotocol separates the authorized SSH upgrade from other WebSockets.
const Subprotocol = "shiftbox.ssh.v1"

const (
	// Progress reports a workspace startup transition before SSH is available.
	Progress = "progress"
	// Ready binds the raw SSH stream to the authenticated workspace and host key.
	Ready = "ready"
	// Error ends a connection attempt with a safe user-facing diagnostic.
	Error = "error"
)

// Control is a text frame preceding the binary SSH transport.
type Control struct {
	Type          string `json:"type"`
	WorkspaceID   string `json:"workspaceId,omitempty"`
	HostPublicKey string `json:"hostPublicKey,omitempty"`
	Message       string `json:"message,omitempty"`
}

// ConnectPath returns the management route for the exact workspace ID.
func ConnectPath(workspaceID string) string {
	return "/api/v1/ssh/workspaces/" + url.PathEscape(workspaceID) + "/connect"
}
