package web

import (
	"context"
	"errors"
	"net/http"
	"time"

	"golang.org/x/crypto/ssh"
)

var sshUnavailable = Problem{Status: http.StatusServiceUnavailable, Code: "ssh.not_ready", Title: "Workspace SSH is unavailable"}

// readiness checks the real guest SSH server without starting a workspace or
// opening an SSH session channel.
func (s workspaceSSH) readiness(w http.ResponseWriter, r *http.Request, token string) {
	id := r.PathValue("workspaceId")
	if !ValidUUID(id) {
		writeSSHProblem(w, r, sshDenied)
		return
	}
	deadline := time.Now().Add(5 * time.Second)
	ctx, cancel := context.WithDeadline(r.Context(), deadline)
	defer cancel()
	admission, err := s.admit(ctx, id, token, nil, sshCheckReady)
	if err != nil {
		if errors.Is(err, errSSHNotReady) {
			err = sshUnavailable
		}
		writeSSHProblem(w, r, err)
		return
	}
	if s.open == nil || !s.probeSSH(ctx, id, admission.generation, deadline) {
		writeSSHProblem(w, r, sshUnavailable)
		return
	}
	if _, err := s.admit(ctx, id, token, &admission, sshCheckReady); err != nil {
		if errors.Is(err, errSSHNotReady) {
			err = sshUnavailable
		}
		writeSSHProblem(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s workspaceSSH) probeSSH(ctx context.Context, id string, generation int64, deadline time.Time) bool {
	conn, authorizedKey, err := s.open(ctx, id, generation, RequestID(ctx))
	if err != nil {
		return false
	}
	defer conn.Close()
	interrupt := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer interrupt()
	publicKey, _, _, _, err := ssh.ParseAuthorizedKey([]byte(authorizedKey))
	if err != nil || conn.SetDeadline(deadline) != nil {
		return false
	}
	client, _, _, err := ssh.NewClientConn(conn, id, &ssh.ClientConfig{
		User:            "shiftbox-readiness",
		HostKeyCallback: ssh.FixedHostKey(publicKey),
	})
	if err != nil {
		return false
	}
	// Closing after authentication avoids PTY, shell, and command startup.
	_ = client.Close()
	return true
}
