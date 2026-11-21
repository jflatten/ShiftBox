package web_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/google/uuid"
	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/operation"
	"github.com/jflatten/ShiftBox/internal/proxy"
	"github.com/jflatten/ShiftBox/internal/web"
	"github.com/jflatten/ShiftBox/internal/workspacessh"
	"golang.org/x/crypto/ssh"
)

// TestWorkspaceSSHReadiness exercises a real SSH handshake and verifies that
// unauthorized requests cannot open the tunnel and non-SSH targets fail closed.
func TestWorkspaceSSHReadiness(t *testing.T) {
	db, now := readySSHWorkspace(t)
	owner := issueWorkspaceSSHFixtureCredential(t, db, now, "user", "owner")
	admin := issueWorkspaceSSHFixtureCredential(t, db, now, "ssh-admin", "admin")
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	config := &ssh.ServerConfig{NoClientAuth: true}
	config.AddHostKey(signer)
	hostKey := string(ssh.MarshalAuthorizedKey(signer.PublicKey()))
	mode := "ready"
	opens := 0
	api := web.NewWorkspaceSSHAPI(db, &proxy.Registry{}, nil, func(ctx context.Context, id string, generation int64, _ string) (net.Conn, string, error) {
		opens++
		if id != lifecycleWorkspaceID || generation != 1 {
			t.Errorf("SSH readiness opened %q generation %d, want the authorized guest", id, generation)
		}
		if mode == "offline" {
			return nil, "", proxy.ErrTunnelServiceUnavailable
		}
		listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = listener.Close() })
		go func(nonSSH bool) {
			guest, err := listener.Accept()
			if err != nil {
				return
			}
			defer guest.Close()
			_ = guest.SetDeadline(time.Now().Add(5 * time.Second))
			if nonSSH {
				_, _ = io.WriteString(guest, "HTTP/1.1 200 OK\r\n")
				return
			}
			if server, _, _, err := ssh.NewServerConn(guest, config); err == nil {
				_ = server.Close()
			}
		}(mode == "not SSH")
		client, err := (&net.Dialer{}).DialContext(ctx, "tcp4", listener.Addr().String())
		return client, hostKey, err
	}, func() time.Time { return now }, "https://management.test", nil)
	check := func(token string, want int) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "https://management.test/api/v1/ssh/workspaces/"+lifecycleWorkspaceID+"/readiness", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		response := httptest.NewRecorder()
		api.ServeHTTP(response, req)
		if response.Code != want {
			t.Errorf("SSH readiness (%s) = HTTP %d, want %d", mode, response.Code, want)
		}
	}
	check("expired-or-invalid", http.StatusUnauthorized)
	check(admin, http.StatusForbidden)
	if opens != 0 {
		t.Errorf("denied readiness requests opened %d tunnels, want zero", opens)
	}
	check(owner, http.StatusNoContent)
	mode = "not SSH"
	check(owner, http.StatusServiceUnavailable)
	mode = "offline"
	check(owner, http.StatusServiceUnavailable)
}

// TestWorkspaceSSHDiscoveryUsesIDEAuthority checks that an administrator needs
// a grant for the exact workspace and that a stale alias cannot bypass denial.
func TestWorkspaceSSHDiscoveryUsesIDEAuthority(t *testing.T) {
	db, now := readySSHWorkspace(t)
	owner := issueWorkspaceSSHFixtureCredential(t, db, now, "user", "owner")
	admin := issueWorkspaceSSHFixtureCredential(t, db, now, "ssh-admin", "admin")
	api := web.NewWorkspaceSSHAPI(db, &proxy.Registry{}, nil, nil, func() time.Time { return now }, "https://management.test", nil)

	request := func(path, token string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "https://management.test"+path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Sec-WebSocket-Protocol", workspacessh.Subprotocol)
		response := httptest.NewRecorder()
		api.ServeHTTP(response, req)
		return response
	}
	checkList := func(token string, want int) {
		t.Helper()
		res := request("/api/v1/ssh/workspaces", token)
		var page struct{ Items []struct{ ID, Name string } }
		if err := json.Unmarshal(res.Body.Bytes(), &page); err != nil || res.Code != http.StatusOK || len(page.Items) != want {
			t.Fatalf("workspace discovery = HTTP %d with %d items (%v), want %d", res.Code, len(page.Items), err, want)
		}
		if want == 1 && (page.Items[0].ID != lifecycleWorkspaceID || page.Items[0].Name != "SSH proof") {
			t.Errorf("workspace discovery item = %+v, want named permitted workspace", page.Items[0])
		}
	}
	checkList(owner, 1)
	executeLifecycleFixture(t, db, `INSERT INTO resource_grants(id,subject_user_id,resource_type,resource_id,action,granted_by,granted_at)
		VALUES('wrong','ssh-admin','workspace','another-workspace','workspace.connect_admin','ssh-admin','2026-11-13T23:00:00Z')`)
	checkList(admin, 0)
	if response := request(workspacessh.ConnectPath(lifecycleWorkspaceID), admin); response.Code != http.StatusForbidden {
		t.Errorf("workspace SSH without exact grant = HTTP %d, want 403", response.Code)
	}
	grantWorkspaceSSHFixture(t, db)
	checkList(admin, 1)
	expired := web.NewWorkspaceSSHAPI(db, &proxy.Registry{}, nil, nil, func() time.Time { return now.Add(time.Hour) }, "https://management.test", nil)
	req := httptest.NewRequest(http.MethodGet, "https://management.test/api/v1/ssh/workspaces", nil)
	req.Header.Set("Authorization", "Bearer "+owner)
	response := httptest.NewRecorder()
	expired.ServeHTTP(response, req)
	if response.Code != http.StatusUnauthorized {
		t.Errorf("workspace discovery after expiry = HTTP %d, want 401", response.Code)
	}
}

// TestWorkspaceSSHGrantRevocationClosesRelay verifies an admitted admin grant
// is audited and the registry's exact-grant closure disconnects both relay ends.
func TestWorkspaceSSHGrantRevocationClosesRelay(t *testing.T) {
	db, now := readySSHWorkspace(t)
	token := issueWorkspaceSSHFixtureCredential(t, db, now, "ssh-admin", "admin")
	grantWorkspaceSSHFixture(t, db)
	registry := &proxy.Registry{}
	peer := make(chan net.Conn, 1)
	api := web.NewWorkspaceSSHAPI(db, registry, nil, func(_ context.Context, id string, generation int64, _ string) (net.Conn, string, error) {
		if id != lifecycleWorkspaceID || generation != 1 {
			t.Errorf("SSH tunnel opened for %q generation %d, want the authorized generation", id, generation)
		}
		controller, guest := net.Pipe()
		peer <- guest
		return controller, "fixture-key", nil
	}, func() time.Time { return now }, "https://management.test", nil)
	server := httptest.NewTLSServer(api)
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	socket, _, err := websocket.Dial(ctx, server.URL+workspacessh.ConnectPath(lifecycleWorkspaceID), &websocket.DialOptions{
		HTTPClient: server.Client(), HTTPHeader: http.Header{"Authorization": {"Bearer " + token}},
		Subprotocols: []string{workspacessh.Subprotocol},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer socket.CloseNow()
	var control workspacessh.Control
	if err := wsjson.Read(ctx, socket, &control); err != nil || control.Type != workspacessh.Ready ||
		control.WorkspaceID != lifecycleWorkspaceID || control.HostPublicKey != "fixture-key" {
		t.Fatalf("SSH readiness = %+v, %v, want exact workspace and tunnel key", control, err)
	}
	guest := <-peer
	defer guest.Close()
	if err := guest.SetDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	clientBytes := []byte("client SSH bytes")
	if err := socket.Write(ctx, websocket.MessageBinary, clientBytes); err != nil {
		t.Fatal(err)
	}
	received := make([]byte, len(clientBytes))
	if _, err := io.ReadFull(guest, received); err != nil || string(received) != string(clientBytes) {
		t.Errorf("guest received %q, %v, want %q", received, err, clientBytes)
	}
	if _, err := io.WriteString(guest, "guest handshake"); err != nil {
		t.Fatal(err)
	}
	if kind, data, err := socket.Read(ctx); err != nil || kind != websocket.MessageBinary || string(data) != "guest handshake" {
		t.Errorf("SSH client received %q (%v), %v, want binary guest handshake", data, kind, err)
	}
	if err := guest.SetDeadline(time.Time{}); err != nil {
		t.Fatal(err)
	}
	var auditCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_events
		WHERE actor_id='ssh-admin' AND action='workspace.connect_admin' AND resource_id=$1 AND metadata_json LIKE '%ssh-grant%'`,
		lifecycleWorkspaceID).Scan(&auditCount); err != nil || auditCount != 1 {
		t.Fatalf("connection audit = %d, %v, want one exact-grant admission", auditCount, err)
	}
	executeLifecycleFixture(t, db, `UPDATE resource_grants SET revoked_at='2026-11-13T23:59:59Z' WHERE id='ssh-grant'`)
	if err := registry.CloseMatching(func(scope proxy.Scope) bool {
		return scope.GrantID == "ssh-grant" && scope.WorkspaceID == lifecycleWorkspaceID
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := socket.Read(ctx); err == nil || ctx.Err() != nil {
		t.Errorf("SSH client read after revoke = %v, context %v, want immediate disconnect", err, ctx.Err())
	}
	if err := guest.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		if errors.Is(err, io.ErrClosedPipe) {
			return
		}
		t.Fatal(err)
	}
	if _, err := guest.Read(make([]byte, 1)); err == nil {
		t.Error("guest relay stayed open after grant revocation")
	} else if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
		t.Error("guest relay did not close on grant revocation")
	}
}

// TestWorkspaceSSHConcurrentStarts verifies stopped discovery and two authorized
// attempts share the ordinary start operation and ready primary container.
func TestWorkspaceSSHConcurrentStarts(t *testing.T) {
	db, now, journal := stoppedSSHWorkspace(t)
	owner := issueWorkspaceSSHFixtureCredential(t, db, now, "user", "owner")
	admin := issueWorkspaceSSHFixtureCredential(t, db, now, "ssh-admin", "admin")
	peers := make(chan net.Conn, 2)
	probed := make(chan struct{}, 1)
	var available atomic.Bool
	api := web.NewWorkspaceSSHAPI(db, &proxy.Registry{}, journal, func(_ context.Context, id string, generation int64, _ string) (net.Conn, string, error) {
		if id != lifecycleWorkspaceID || generation != 1 {
			t.Errorf("SSH opened workspace %q generation %d, want the admitted generation", id, generation)
		}
		if !available.Load() {
			select {
			case probed <- struct{}{}:
			default:
			}
			return nil, "", proxy.ErrTunnelServiceUnavailable
		}
		relay, peer := net.Pipe()
		peers <- peer
		return relay, "fixture-key", nil
	}, func() time.Time { return now }, "https://management.test", nil)

	checkStoppedSSHDiscovery(t, api, owner, 1)
	checkStoppedSSHDiscovery(t, api, admin, 0)
	server := httptest.NewTLSServer(api)
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if socket, res, err := dialWorkspaceSSHFixture(ctx, server, admin); err == nil || res == nil || res.StatusCode != http.StatusForbidden {
		if socket != nil {
			_ = socket.CloseNow()
		}
		t.Fatalf("ungranted administrator dial = %v, %v, want HTTP 403", res, err)
	}
	assertSSHStartCount(t, db, 0)

	type result struct {
		socket *websocket.Conn
		err    error
	}
	dials := make(chan result, 2)
	for range 2 {
		go func() {
			socket, _, err := dialWorkspaceSSHFixture(ctx, server, owner)
			dials <- result{socket, err}
		}()
	}
	var sockets []*websocket.Conn
	for range 2 {
		dial := <-dials
		if dial.err != nil {
			t.Fatal(dial.err)
		}
		defer dial.socket.CloseNow()
		expectSSHControl(t, ctx, dial.socket, workspacessh.Progress)
		sockets = append(sockets, dial.socket)
	}
	assertSSHStartCount(t, db, 1)
	checkStoppedSSHDiscovery(t, api, owner, 1)
	executeLifecycleFixture(t, db, `UPDATE workspaces SET observed_state='running';
		UPDATE operation_steps SET status='succeeded' WHERE step_key='verify_ide';
		UPDATE operations SET status='running'`)
	select {
	case <-probed:
		available.Store(true)
	case <-ctx.Done():
		t.Fatal("SSH waiter did not check for the guest after workspace readiness")
	}
	for _, socket := range sockets {
		control := expectSSHControl(t, ctx, socket, workspacessh.Ready)
		if control.WorkspaceID != lifecycleWorkspaceID || control.HostPublicKey != "fixture-key" {
			t.Errorf("SSH readiness = %+v, want the exact admitted workspace and key", control)
		}
	}
	for range 2 {
		select {
		case peer := <-peers:
			_ = peer.Close()
		case <-ctx.Done():
			t.Fatal("authorized wait did not open its own guest stream")
		}
	}
}

// TestWorkspaceSSHStartFailureAndAuthorityChange verifies waits never expose
// SSH before readiness or after revocation, and failed starts guide normal retry.
func TestWorkspaceSSHStartFailureAndAuthorityChange(t *testing.T) {
	for _, tc := range []struct {
		name, change, message string
	}{
		{"failed start", `UPDATE operations SET status='failed'; UPDATE operation_steps SET status='failed' WHERE step_key='configure'`, "retry"},
		{"changed grant", `UPDATE workspaces SET observed_state='running'; UPDATE operation_steps SET status='succeeded' WHERE step_key='verify_ide';
			UPDATE resource_grants SET revoked_at='2026-11-13T23:59:59Z' WHERE id='ssh-grant'`, "not permitted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, now, journal := stoppedSSHWorkspace(t)
			token := issueWorkspaceSSHFixtureCredential(t, db, now, "ssh-admin", "admin")
			grantWorkspaceSSHFixture(t, db)
			api := web.NewWorkspaceSSHAPI(db, &proxy.Registry{}, journal, func(_ context.Context, _ string, _ int64, _ string) (net.Conn, string, error) {
				t.Error("SSH tried to open a guest stream after the start failed or authority changed")
				return nil, "", proxy.ErrTunnelServiceUnavailable
			}, func() time.Time { return now }, "https://management.test", nil)
			server := httptest.NewTLSServer(api)
			defer server.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			socket, _, err := dialWorkspaceSSHFixture(ctx, server, token)
			if err != nil {
				t.Fatal(err)
			}
			defer socket.CloseNow()
			expectSSHControl(t, ctx, socket, workspacessh.Progress)
			executeLifecycleFixture(t, db, tc.change)
			control := expectSSHControl(t, ctx, socket, workspacessh.Error)
			if !strings.Contains(control.Message, tc.message) {
				t.Errorf("SSH control = %+v, want guidance containing %q", control, tc.message)
			}
			assertSSHStartCount(t, db, 1)
			var audits int
			if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_events WHERE action='workspace.connect_admin'`).Scan(&audits); err != nil || audits != 0 {
				t.Errorf("audit count = %d, %v, want no SSH forwarding audit", audits, err)
			}
		})
	}
}

// TestWorkspaceSSHDisconnectedWaitKeepsStart verifies the socket reader notices
// a client that leaves before readiness without cancelling the journaled start.
func TestWorkspaceSSHDisconnectedWaitKeepsStart(t *testing.T) {
	db, now, journal := stoppedSSHWorkspace(t)
	token := issueWorkspaceSSHFixtureCredential(t, db, now, "user", "owner")
	finished := make(chan struct{})
	api := web.NewWorkspaceSSHAPI(db, &proxy.Registry{}, journal, func(_ context.Context, _ string, _ int64, _ string) (net.Conn, string, error) {
		t.Error("SSH tried to open a guest stream before the workspace was ready")
		return nil, "", proxy.ErrTunnelServiceUnavailable
	}, func() time.Time { return now }, "https://management.test", nil)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(finished)
		api.ServeHTTP(w, r)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	socket, _, err := dialWorkspaceSSHFixture(ctx, server, token)
	if err != nil {
		t.Fatal(err)
	}
	defer socket.CloseNow()
	expectSSHControl(t, ctx, socket, workspacessh.Progress)
	if err := socket.CloseNow(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-finished:
	case <-ctx.Done():
		t.Fatal("SSH waiter did not release its handler after the peer disconnected")
	}
	var status string
	if err := db.QueryRowContext(ctx, `SELECT status FROM operations WHERE originating_action='workspace.start'`).Scan(&status); err != nil || status != "pending" {
		t.Errorf("ordinary start after disconnect = %q, %v, want pending", status, err)
	}
}

func stoppedSSHWorkspace(t *testing.T) (*sql.DB, time.Time, *operation.Journal) {
	t.Helper()
	db, now := readySSHWorkspace(t)
	executeLifecycleFixture(t, db, `DELETE FROM operation_steps; DELETE FROM operations;
		UPDATE workspaces SET desired_state='stopped',observed_state='stopped',provider_instance_id='retained-proof';
		INSERT INTO capacity_budgets(installation_id,cpu_limit,memory_mib_limit,disk_mib_limit,updated_by,updated_at,version)
		SELECT id,2,1024,40,'user','now',1 FROM installations;
		INSERT INTO reservations(workspace_id,cpu_count,memory_mib,disk_mib,cpu_memory_state,disk_state,reserved_at,cpu_memory_released_at)
		SELECT id,cpu_count,memory_mib,disk_mib,'released','reserved','now','now' FROM workspaces`)
	return db, now, operation.NewJournal(db, func() time.Time { return now }, uuid.NewString)
}

func checkStoppedSSHDiscovery(t *testing.T, api http.Handler, token string, want int) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "https://management.test/api/v1/ssh/workspaces", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, req)
	var page struct{ Items []struct{ ID string } }
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil || rec.Code != http.StatusOK || len(page.Items) != want {
		t.Fatalf("stopped SSH discovery = HTTP %d, %d items, %v, want %d items", rec.Code, len(page.Items), err, want)
	}
}

func dialWorkspaceSSHFixture(ctx context.Context, server *httptest.Server, token string) (*websocket.Conn, *http.Response, error) {
	return websocket.Dial(ctx, server.URL+workspacessh.ConnectPath(lifecycleWorkspaceID), &websocket.DialOptions{
		HTTPClient: server.Client(), HTTPHeader: http.Header{"Authorization": {"Bearer " + token}},
		Subprotocols: []string{workspacessh.Subprotocol},
	})
}

func expectSSHControl(t *testing.T, ctx context.Context, socket *websocket.Conn, kind string) workspacessh.Control {
	t.Helper()
	for {
		var control workspacessh.Control
		if err := wsjson.Read(ctx, socket, &control); err != nil {
			t.Fatalf("SSH control waiting for %q = %v", kind, err)
		}
		if control.Type == kind {
			return control
		}
		if control.Type != workspacessh.Progress {
			t.Fatalf("SSH control = %+v, want %s", control, kind)
		}
	}
}

func assertSSHStartCount(t *testing.T, db *sql.DB, want int) {
	t.Helper()
	var count int
	if err := db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM operations WHERE originating_action='workspace.start'`).Scan(&count); err != nil || count != want {
		t.Fatalf("workspace start count = %d, %v, want %d", count, err, want)
	}
}

func readySSHWorkspace(t *testing.T) (*sql.DB, time.Time) {
	t.Helper()
	db := keepAliveDatabase(t)
	executeLifecycleFixture(t, db, `UPDATE workspaces SET desired_state='running',observed_state='running',name='SSH proof';
		INSERT INTO operations(id,workspace_id,type,generation,requested_by,request_key,status,created_at,version,originating_action)
		VALUES('ssh-start',$1,'start',1,'user','ssh-start','succeeded','2026-11-13T23:00:00Z',1,'workspace.start');
		INSERT INTO operation_steps(operation_id,step_index,step_key,label,status)
		VALUES('ssh-start',0,'verify_ide','Verify IDE','succeeded');
		INSERT INTO users(id,primary_email,role,membership_state,credential_mode,created_by,created_at,updated_at,version)
		VALUES('ssh-admin','ssh-admin@example.test','administrator','active','local','setup','now','now',1)`, lifecycleWorkspaceID)
	return db, time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC)
}

func issueWorkspaceSSHFixtureCredential(t *testing.T, db *sql.DB, now time.Time, user, id string) string {
	t.Helper()
	ctx := t.Context()
	store := auth.NewStore(db)
	session, _, err := auth.NewSession(id, user, "normal", now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	var token string
	if err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
		if err := store.SaveSession(ctx, tx, session); err != nil {
			return err
		}
		var err error
		token, _, err = store.IssueHelperCredential(ctx, tx, session.ID, now)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return token
}

func grantWorkspaceSSHFixture(t *testing.T, db *sql.DB) {
	t.Helper()
	executeLifecycleFixture(t, db, `INSERT INTO resource_grants(id,subject_user_id,resource_type,resource_id,action,granted_by,granted_at)
		VALUES('ssh-grant','ssh-admin','workspace',$1,'workspace.connect_admin','ssh-admin','2026-11-13T23:00:00Z')`, lifecycleWorkspaceID)
}
