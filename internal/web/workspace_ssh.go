package web

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/operation"
	"github.com/jflatten/ShiftBox/internal/proxy"
	"github.com/jflatten/ShiftBox/internal/workspace"
	"github.com/jflatten/ShiftBox/internal/workspacessh"
)

type workspaceSSH struct {
	db       *sql.DB
	registry *proxy.Registry
	journal  *operation.Journal
	open     func(context.Context, string, int64, string) (net.Conn, string, error)
	now      func() time.Time
}

type sshAdmission struct {
	scope          proxy.Scope
	installationID string
	generation     int64
	deadline       time.Time
	ready          bool
	startID        string
}

type sshWorkspaceTarget struct {
	AuthorizationTarget
	generation int64
	ready      bool
	start      workspace.SSHStart
}

type sshAdmissionStage int

const (
	sshAuthorize sshAdmissionStage = iota
	sshStart
	sshCheckReady
	sshForward
)

type sshWorkspaceAlias struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

var (
	sshSignIn      = Problem{Status: http.StatusUnauthorized, Code: "ssh.sign_in_required", Title: "Sign in to ShiftBox to use workspace SSH"}
	sshDenied      = Problem{Status: http.StatusForbidden, Code: "ssh.connection_denied", Title: "Workspace SSH access is unavailable or not permitted"}
	errSSHNotReady = errors.New("workspace is not ready for SSH")
)

// NewWorkspaceSSHAPI mounts workstation access and browser pairing at the
// management origin. Downloads and pairing approval require a browser session.
func NewWorkspaceSSHAPI(db *sql.DB, registry *proxy.Registry, journal *operation.Journal,
	open func(context.Context, string, int64, string) (net.Conn, string, error), now func() time.Time, origin string, archive []byte,
) http.Handler {
	ssh := workspaceSSH{db: db, registry: registry, journal: journal, open: open, now: now}
	pairing := &helperPairing{db: db, origin: origin, now: now, pending: make(map[string]pairingApproval)}
	mux := http.NewServeMux()
	for _, route := range WorkspaceSSHRoutes() {
		pattern := route.Method + " /api/v1" + route.Path
		switch route.OperationID {
		case "listSSHWorkspaces":
			mux.Handle(pattern, ssh.authenticated(ssh.list))
		case "connectWorkspaceSSH":
			mux.Handle(pattern, ssh.authenticated(ssh.connect))
		case "downloadWorkspaceSSHHelper":
			mux.Handle(pattern, helperDownloadHandler(db, origin, now, route, archive))
		case "approveWorkspaceSSHHelper":
			mux.HandleFunc(pattern, pairing.approve)
		case "exchangeWorkspaceSSHHelper":
			mux.HandleFunc(pattern, pairing.exchange)
		}
	}
	return WithRequestID(mux)
}

func (s workspaceSSH) authenticated(handler func(http.ResponseWriter, *http.Request, string)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.TLS == nil || len(r.Header.Values("Origin")) != 0 {
			WriteProblem(w, RequestID(r.Context()), sshDenied)
			return
		}
		headers := r.Header.Values("Authorization")
		if len(headers) != 1 {
			WriteProblem(w, RequestID(r.Context()), sshSignIn)
			return
		}
		token, ok := strings.CutPrefix(headers[0], "Bearer ")
		if !ok || token == "" {
			WriteProblem(w, RequestID(r.Context()), sshSignIn)
			return
		}
		handler(w, r, token)
	})
}

func (s workspaceSSH) list(w http.ResponseWriter, r *http.Request, token string) {
	ctx := r.Context()
	items := []sshWorkspaceAlias{}
	err := database.Transaction(ctx, s.db, func(tx *sql.Tx) error {
		now := s.now()
		store := auth.NewStore(nil)
		principal, _, err := store.HelperPrincipal(ctx, tx, token, now)
		if err != nil {
			return err
		}
		aliases, err := sshAliasCandidates(ctx, tx, principal)
		if err != nil {
			return err
		}
		for _, alias := range aliases {
			target, err := workspaceSSHAuthority(ctx, tx, store, principal, alias.ID, now, s.journal != nil)
			if err != nil {
				return err
			}
			if auth.Authorize(principal, target.Action, target.Resource, target.Grants, now).Allowed {
				items = append(items, alias)
			}
		}
		return nil
	})
	if err != nil {
		writeSSHProblem(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(struct {
		Items []sshWorkspaceAlias `json:"items"`
	}{Items: items})
}

func sshAliasCandidates(ctx context.Context, tx *sql.Tx, principal auth.Principal) ([]sshWorkspaceAlias, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id,name,repository_url FROM workspaces
		WHERE installation_id=$1 AND observed_state<>'deleted' AND (owner_user_id=$2 OR $3='administrator')
		ORDER BY name,id`, principal.InstallationID, principal.User.ID, principal.User.Role)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var aliases []sshWorkspaceAlias
	for rows.Next() {
		var alias sshWorkspaceAlias
		var repository string
		if err := rows.Scan(&alias.ID, &alias.Name, &repository); err != nil {
			return nil, err
		}
		if alias.Name == "" {
			alias.Name = strings.TrimSuffix(path.Base(repository), ".git")
		}
		aliases = append(aliases, alias)
	}
	return aliases, rows.Err()
}

func workspaceSSHAuthority(ctx context.Context, tx *sql.Tx, store *auth.Store, principal auth.Principal,
	id string, now time.Time, allowStart bool,
) (sshWorkspaceTarget, error) {
	resource, generation, err := serviceWorkspace(ctx, tx, id, "ide")
	if err != nil {
		return sshWorkspaceTarget{}, err
	}
	access := sshWorkspaceTarget{generation: generation, ready: resource.Eligible}
	if allowStart && !access.ready && auth.Authorize(principal, "workspace.start", resource, nil, now).Allowed {
		access.start, err = workspace.ReadSSHStart(ctx, tx, id, generation, now)
		if err != nil {
			return sshWorkspaceTarget{}, err
		}
		resource.Eligible = access.start.CanStart || access.start.OperationID != ""
	}
	access.AuthorizationTarget, err = serviceEntryTarget(ctx, tx, store, principal, resource)
	return access, err
}

func (s workspaceSSH) admit(ctx context.Context, id, token string, expected *sshAdmission, stage sshAdmissionStage) (sshAdmission, error) {
	var admission sshAdmission
	err := database.Transaction(ctx, s.db, func(tx *sql.Tx) error {
		if stage == sshStart {
			if err := workspace.LockPowerAdmission(ctx, tx, expected.installationID); err != nil {
				return powerAdmissionProblem(err)
			}
		}
		now := s.now()
		store := auth.NewStore(nil)
		principal, deadline, err := store.HelperPrincipal(ctx, tx, token, now)
		if err != nil {
			return err
		}
		target, err := workspaceSSHAuthority(ctx, tx, store, principal, id, now, s.journal != nil)
		if errors.Is(err, sql.ErrNoRows) {
			return sshDenied
		}
		if err != nil {
			return err
		}
		decision := auth.Authorize(principal, target.Action, target.Resource, target.Grants, now)
		if !decision.Allowed {
			return sshDenied
		}
		scope := proxy.Scope{UserID: principal.User.ID, SessionID: principal.Session.ID, WorkspaceID: id, Service: proxy.SSHService}
		if decision.AuditRequired {
			for _, grant := range target.Grants {
				if grant.Action == target.Action {
					scope.GrantID = grant.ID
					break
				}
			}
		}
		admission = sshAdmission{scope: scope, installationID: principal.InstallationID, generation: target.generation,
			deadline: deadline, ready: target.ready, startID: target.start.OperationID}
		if expected != nil && (admission.scope != expected.scope || admission.installationID != expected.installationID ||
			admission.generation != expected.generation) {
			return sshDenied
		}
		if stage >= sshCheckReady && !admission.ready {
			return errSSHNotReady
		}
		if stage == sshStart && !admission.ready && admission.startID == "" {
			op, err := workspace.AdmitPower(ctx, tx, s.journal, workspace.PowerIntent{
				WorkspaceID: id, ActorID: principal.User.ID, RequestKey: "ssh-start-" + RequestID(ctx),
				Action: "start", Version: target.start.Version,
			}, now)
			if err != nil {
				return powerAdmissionProblem(err)
			}
			admission.startID = op.ID
		}
		if stage == sshForward && decision.AuditRequired {
			return recordConnectionAudit(ctx, tx, principal, target.AuthorizationTarget, now)
		}
		return nil
	})
	return admission, err
}

func (s workspaceSSH) connect(w http.ResponseWriter, r *http.Request, token string) {
	id := r.PathValue("workspaceId")
	if !ValidUUID(id) {
		writeSSHProblem(w, r, sshDenied)
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	admission, err := s.admit(ctx, id, token, nil, sshAuthorize)
	if err != nil {
		writeSSHProblem(w, r, err)
		return
	}
	if r.Header.Get("Sec-WebSocket-Protocol") != workspacessh.Subprotocol ||
		!strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		WriteProblem(w, RequestID(ctx), Problem{Status: http.StatusBadRequest, Code: "ssh.upgrade_required", Title: "Use the ShiftBox helper to connect"})
		return
	}
	unregister, err := s.registry.Register(admission.scope, func() error {
		current, err := s.admit(ctx, id, token, &admission, sshAuthorize)
		if err != nil {
			return err
		}
		admission = current
		return nil
	}, func() error {
		cancel()
		return nil
	})
	if err != nil {
		writeSSHProblem(w, r, err)
		return
	}
	defer unregister()
	active, expire := context.WithTimeout(ctx, admission.deadline.Sub(s.now()))
	defer expire()
	socket, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{workspacessh.Subprotocol}})
	if err != nil {
		return
	}
	stopSocket := context.AfterFunc(active, func() { _ = socket.CloseNow() })
	defer stopSocket()

	client := websocket.NetConn(active, socket, websocket.MessageBinary)
	socket.SetReadLimit(1 << 20)
	input, writer := io.Pipe()
	ready := make(chan struct{})
	peerDone := make(chan struct{})
	go func() {
		defer close(peerDone)
		defer cancel()
		defer writer.Close()
		readWorkspaceSSH(active, socket, writer, ready)
	}()
	defer func() {
		cancel()
		_ = input.Close()
		_ = socket.CloseNow()
		<-peerDone
	}()

	if !admission.ready {
		admission, err = s.admit(active, id, token, &admission, sshStart)
		if err != nil {
			writeSSHControlError(active, socket, err)
			return
		}
	}
	guest, hostKey, err := s.awaitSSHGuest(active, socket, token, admission)
	if err != nil {
		writeSSHControlError(active, socket, err)
		return
	}
	defer guest.Close()
	// The exact session and grant remain registered while we recheck and audit
	// the admission. No workstation bytes reach the guest before this commit.
	current, err := s.admit(active, id, token, &admission, sshForward)
	if err != nil {
		writeSSHControlError(active, socket, err)
		return
	}
	forwardCtx, expireForward := context.WithTimeout(active, current.deadline.Sub(s.now()))
	defer expireForward()
	forwardWorkspaceSSH(forwardCtx, socket, client, guest, input, ready, current.scope.WorkspaceID, hostKey)
}

func (s workspaceSSH) awaitSSHGuest(ctx context.Context, socket *websocket.Conn, token string, admission sshAdmission) (net.Conn, string, error) {
	open := func(ctx context.Context) (net.Conn, string, error) {
		id := admission.scope.WorkspaceID
		if _, err := s.admit(ctx, id, token, &admission, sshCheckReady); err != nil {
			return nil, "", err
		}
		return s.open(ctx, id, admission.generation, RequestID(ctx))
	}
	if admission.startID == "" {
		return open(ctx)
	}
	ctx, cancel := context.WithTimeout(ctx, 4*time.Minute)
	defer cancel()
	poll := time.NewTicker(500 * time.Millisecond)
	defer poll.Stop()
	lastMessage := ""
	for {
		op, err := s.journal.Get(ctx, admission.startID)
		if err != nil {
			return nil, "", err
		}
		switch op.Status {
		case "failed":
			return nil, "", Problem{Title: "Workspace start failed. Open Workspaces to review and retry."}
		case "cancelled":
			return nil, "", Problem{Title: "Workspace start was cancelled. Open Workspaces to review its state."}
		}
		message := sshStartProgress(op)
		if message != lastMessage {
			if err := wsjson.Write(ctx, socket, workspacessh.Control{Type: workspacessh.Progress, Message: message}); err != nil {
				return nil, "", err
			}
			lastMessage = message
		}
		guest, hostKey, err := open(ctx)
		if !errors.Is(err, errSSHNotReady) && !errors.Is(err, proxy.ErrTunnelServiceUnavailable) {
			return guest, hostKey, err
		}
		select {
		case <-ctx.Done():
			return nil, "", ctx.Err()
		case <-poll.C:
		}
	}
}

func sshStartProgress(op operation.Operation) string {
	if op.CurrentStep != nil {
		for _, step := range op.Steps {
			if step.Key == *op.CurrentStep {
				return step.Label
			}
		}
	}
	return "Waiting for workspace startup"
}

// readWorkspaceSSH watches for disconnection even before there is a guest.
// It is the only WebSocket reader and rejects SSH input until readiness.
func readWorkspaceSSH(ctx context.Context, socket *websocket.Conn, writer *io.PipeWriter, ready <-chan struct{}) {
	for {
		kind, message, err := socket.Reader(ctx)
		if err != nil || kind != websocket.MessageBinary {
			return
		}
		select {
		case <-ready:
		default:
			return
		}
		if _, err := io.Copy(writer, message); err != nil {
			return
		}
	}
}

func forwardWorkspaceSSH(ctx context.Context, socket *websocket.Conn, client, guest net.Conn,
	input *io.PipeReader, ready chan<- struct{}, id, hostKey string,
) {
	stopRelay := context.AfterFunc(ctx, func() {
		_ = guest.Close()
		_ = socket.CloseNow()
	})
	defer stopRelay()
	close(ready)
	if err := wsjson.Write(ctx, socket, workspacessh.Control{
		Type: workspacessh.Ready, WorkspaceID: id, HostPublicKey: hostKey,
	}); err != nil {
		return
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = io.Copy(guest, input)
		_ = guest.Close()
	}()
	_, _ = io.Copy(client, guest)
	_ = input.Close()
	_ = guest.Close()
	<-done
	_ = socket.Close(websocket.StatusNormalClosure, "")
}

func writeSSHControlError(ctx context.Context, socket *websocket.Conn, err error) {
	if ctx.Err() != nil {
		return
	}
	message := "Workspace SSH is unavailable"
	var problem Problem
	switch {
	case errors.Is(err, auth.ErrHelperCredential):
		message = sshSignIn.Title
	case errors.Is(err, context.DeadlineExceeded):
		message = "Workspace SSH did not become ready. Open Workspaces to review startup."
	case errors.As(err, &problem) && problem.Title != "":
		message = problem.Title
	}
	_ = wsjson.Write(ctx, socket, workspacessh.Control{Type: workspacessh.Error, Message: message})
}

func writeSSHProblem(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, auth.ErrHelperCredential) {
		err = sshSignIn
	}
	WriteProblem(w, RequestID(r.Context()), err)
}
