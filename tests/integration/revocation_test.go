package integration_test

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jflatten/ShiftBox/internal/app"
	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/config"
	"github.com/jflatten/ShiftBox/internal/cryptostore"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/guest"
	"github.com/jflatten/ShiftBox/internal/operation"
	"github.com/jflatten/ShiftBox/internal/provider"
	"github.com/jflatten/ShiftBox/internal/proxy"
	"github.com/jflatten/ShiftBox/internal/web"
)

const (
	revocationManagementOrigin = "https://management.test"
	revocationIDEPattern       = "{workspace}.ide.test"
	revocationPreviewPattern   = "{service}--{workspace}.preview.test"
)

// TestDisableClosesBrowserAndTunnelConnections verifies that authorized
// offboarding closes established SSE, IDE, preview, and WebSocket streams
// and the live guest tunnel, and durably requests a workspace stop.
func TestDisableClosesBrowserAndTunnelConnections(t *testing.T) {
	fixture := newRevocationFixture(t)
	guestListeners, guestTLS, boot := fixture.startGuestListeners(t)
	if err := fixture.journal.StartStep(t.Context(), boot, 0); err != nil {
		t.Fatal(err)
	}
	if err := fixture.journal.CompleteStep(t.Context(), boot, 0, "IDE verified"); err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewServer(http.HandlerFunc(holdRevocationUpstream))
	t.Cleanup(upstream.Close)
	port := upstream.Listener.Addr().(*net.TCPAddr).Port
	preview := "preview-" + strconv.Itoa(port)
	fixture.exec(t, "UPDATE workspaces SET preview_ports_json=$1 WHERE id=$2", fmt.Sprintf("[%d]", port), fixture.ref.WorkspaceID)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() {
		done <- guest.ServeTunnel(ctx, guestListeners.TunnelAddress, guestTLS, "1", 1, []proxy.TunnelService{
			{Key: "ide", Target: upstream.Listener.Addr().String(), Protocols: []string{"http", "websocket"}},
			{Key: preview, Target: upstream.Listener.Addr().String(), Protocols: []string{"http"}},
		})
	}()
	probe := proxy.StreamHeader{ServiceKey: "ide", RequestID: "tunnel-readiness", Protocol: "http"}
	for {
		stream, err := fixture.tunnels.Open(ctx, fixture.ref.WorkspaceID, 1, probe)
		if err == nil {
			stream.Close()
			break
		}
		select {
		case err := <-done:
			t.Fatalf("guest tunnel ended before readiness: %v", err)
		case <-ctx.Done():
			t.Fatal("guest tunnel did not register before the deadline")
		case <-time.After(10 * time.Millisecond):
		}
	}

	hosts, err := config.NewServiceHosts(revocationManagementOrigin, revocationIDEPattern, revocationPreviewPattern)
	if err != nil {
		t.Fatal(err)
	}
	now := func() time.Time { return fixture.now }
	_, events := web.OperationHandlers(fixture.db, revocationManagementOrigin, now, fixture.journal, fixture.tunnels.Registry)
	management := http.NewServeMux()
	management.Handle("GET /api/v1/operations/{operationId}/events", &events)
	management.Handle("PATCH /api/v1/admin/users/{userId}", fixture.membershipHandler())
	managementServer := httptest.NewTLSServer(management)
	t.Cleanup(managementServer.Close)
	active := make(chan (<-chan struct{}), 3)
	forward := func(w http.ResponseWriter, r *http.Request, workspace string, generation int64, service, requestID string) {
		active <- r.Context().Done()
		fixture.tunnels.Forward(w, r, workspace, generation, service, requestID)
	}
	serviceServer := httptest.NewTLSServer(web.ServiceProxyHandler(fixture.db, hosts, fixture.tunnels.Registry, now, forward))
	t.Cleanup(serviceServer.Close)

	ideHost, err := hosts.ServiceAuthority(fixture.ref.WorkspaceID, "ide")
	if err != nil {
		t.Fatal(err)
	}
	previewHost, err := hosts.ServiceAuthority(fixture.ref.WorkspaceID, preview)
	if err != nil {
		t.Fatal(err)
	}
	ideCookie := fixture.serviceCookie(t, ctx, serviceServer, ideHost, "ide")
	previewCookie := fixture.serviceCookie(t, ctx, serviceServer, previewHost, preview)
	type openStream struct {
		name string
		body io.Reader
		done <-chan struct{}
	}
	var streams []openStream
	for _, stream := range []struct {
		name, host string
		cookie     *http.Cookie
		upgrade    bool
	}{
		{name: "IDE", host: ideHost, cookie: ideCookie},
		{name: "preview", host: previewHost, cookie: previewCookie},
		{name: "WebSocket", host: ideHost, cookie: ideCookie, upgrade: true},
	} {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, serviceServer.URL+"/stream", nil)
		if err != nil {
			t.Fatal(err)
		}
		request.Host = stream.host
		request.AddCookie(stream.cookie)
		want := http.StatusOK
		if stream.upgrade {
			want = http.StatusSwitchingProtocols
			request.Header.Set("Origin", "https://"+stream.host)
			request.Header.Set("Connection", "Upgrade")
			request.Header.Set("Upgrade", "websocket")
		}
		response := doRevocationRequest(t, serviceServer.Client(), request, want)
		if !stream.upgrade {
			var marker [1]byte
			if _, err := io.ReadFull(response.Body, marker[:]); marker[0] != '>' || err != nil {
				t.Fatalf("%s stream marker = %q, %v, want >", stream.name, marker, err)
			}
		}
		streams = append(streams, openStream{name: stream.name, body: response.Body, done: <-active})
	}
	pending := fixture.createOperation(t, "workspace.start", "start", "start")
	sseRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, managementServer.URL+"/api/v1/operations/"+pending.ID+"/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	sseRequest.AddCookie(&http.Cookie{Name: "__Host-shiftbox_session", Value: fixture.ownerToken})
	sse := doRevocationRequest(t, managementServer.Client(), sseRequest, http.StatusOK)
	sseBody := bufio.NewReader(sse.Body)
	if line, err := sseBody.ReadString('\n'); line != ": connected\n" || err != nil {
		t.Fatalf("management SSE first line = %q, %v, want connected marker", line, err)
	}
	streams = append(streams, openStream{name: "management SSE", body: sseBody})

	response := doRevocationRequest(t, managementServer.Client(), fixture.disableRequest(t, ctx, managementServer.URL), http.StatusOK)
	for _, stream := range streams {
		if stream.done == nil {
			continue
		}
		select {
		case <-stream.done:
		default:
			t.Errorf("%s proxy request was still authorized when disable returned success", stream.name)
		}
	}
	var result struct {
		StopOperations []struct {
			OperationID string `json:"operationId"`
			WorkspaceID string `json:"workspaceId"`
		} `json:"stopOperations"`
	}
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil || len(result.StopOperations) != 1 {
		t.Fatalf("disable workspace owner stop operations = %v, %v, want one stop", result.StopOperations, err)
	}
	stop := result.StopOperations[0]
	op, err := fixture.journal.Get(t.Context(), stop.OperationID)
	if err != nil || op.Type != "stop" || op.OriginatingAction != "user.disable" || op.Status != "pending" || stop.WorkspaceID != fixture.ref.WorkspaceID {
		t.Errorf("disable workspace owner queued operation = %s/%s/%s for %s, %v, want pending user.disable stop for %s",
			op.Type, op.OriginatingAction, op.Status, stop.WorkspaceID, err, fixture.ref.WorkspaceID)
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Error("guest tunnel remained established after successful disable")
	}
	for _, stream := range streams {
		_, err := io.Copy(io.Discard, stream.body)
		if errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
			t.Errorf("%s remained readable after successful disable: %v", stream.name, err)
		}
	}
}

// TestDisableClosesEstablishedGuestPoll verifies that the production polling
// listener closes an authenticated keep-alive connection on offboarding.
func TestDisableClosesEstablishedGuestPoll(t *testing.T) {
	fixture := newRevocationFixture(t)
	listeners, guestTLS, _ := fixture.startGuestListeners(t)
	pollURL, err := url.Parse(listeners.PollURL)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	t.Cleanup(cancel)
	dialer := tls.Dialer{Config: guestTLS}
	conn, err := dialer.DialContext(ctx, "tcp", pollURL.Host)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	deadline, _ := ctx.Deadline()
	if err := conn.SetDeadline(deadline); err != nil {
		t.Fatal(err)
	}
	pollRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, pollURL.String(),
		strings.NewReader(`{"protocolVersion":"1","guestRelease":"release"}`))
	if err != nil {
		t.Fatal(err)
	}
	pollRequest.Header.Set("Content-Type", "application/json")
	if err := pollRequest.Write(conn); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(conn)
	response, err := http.ReadResponse(reader, pollRequest)
	if err != nil {
		t.Fatal(err)
	}
	_, readErr := io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if readErr != nil || response.StatusCode != http.StatusOK || response.Close {
		t.Fatalf("initial guest poll = HTTP %d, close=%t, %v, want 200 on a reusable connection", response.StatusCode, response.Close, readErr)
	}

	result := httptest.NewRecorder()
	request := fixture.disableRequest(t, ctx, revocationManagementOrigin)
	request.SetPathValue("userId", fixture.owner)
	fixture.membershipHandler().ServeHTTP(result, request)
	if result.Code != http.StatusOK {
		t.Fatalf("disable workspace owner = HTTP %d, want 200", result.Code)
	}
	if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	_, err = reader.Peek(1)
	var netErr net.Error
	if err == nil || (errors.As(err, &netErr) && netErr.Timeout()) {
		t.Errorf("read established poll connection after successful disable = %v, want transport closure", err)
	}
}

func holdRevocationUpstream(w http.ResponseWriter, r *http.Request) {
	controller := http.NewResponseController(w)
	if r.Header.Get("Upgrade") == "websocket" {
		conn, rw, err := controller.Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = fmt.Fprint(rw, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n")
		if err := rw.Flush(); err == nil {
			_, _ = io.Copy(io.Discard, rw)
		}
		return
	}
	_, _ = io.WriteString(w, ">")
	if err := controller.Flush(); err == nil {
		<-r.Context().Done()
	}
}

type revocationFixture struct {
	db                            *sql.DB
	now                           time.Time
	owner, ownerToken, adminToken string
	ownerSession                  auth.Session
	ref                           provider.WorkspaceRef
	journal                       *operation.Journal
	tunnels                       *proxy.Tunnels
}

func newRevocationFixture(t *testing.T) *revocationFixture {
	t.Helper()
	db, err := database.Open(t.Context(), database.Config{Profile: "sqlite", SQLitePath: filepath.Join(t.TempDir(), "revocation.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := database.Migrate(t.Context(), db, "sqlite"); err != nil {
		t.Fatal(err)
	}
	fixture := &revocationFixture{db: db, now: time.Now().UTC(), owner: uuid.NewString(), tunnels: &proxy.Tunnels{Registry: &proxy.Registry{}},
		ref: provider.WorkspaceRef{InstallationID: uuid.NewString(), WorkspaceID: uuid.NewString(), Generation: 1, OwnershipMarker: "fixture-owned", InstanceID: "fixture-vm"}}
	stamp := fixture.now.Format(time.RFC3339Nano)
	fixture.exec(t, `INSERT INTO installations(id,state,organization_name,data_profile,management_origin,ide_host_pattern,preview_host_pattern,
		active_release,min_guest_release,created_at,updated_at,version)
		VALUES($1,'active','Fixture','sqlite',$2,$3,$4,'release','release',$5,$5,1)`,
		fixture.ref.InstallationID, revocationManagementOrigin, revocationIDEPattern, revocationPreviewPattern, stamp)
	adminID := uuid.NewString()
	fixture.exec(t, `INSERT INTO users(id,primary_email,role,membership_state,credential_mode,created_by,created_at,updated_at,version) VALUES
		($1,'member@example.test','member','active','local','setup',$3,$3,1),
		($2,'admin@example.test','administrator','active','local','setup',$3,$3,1)`, fixture.owner, adminID, stamp)
	fixture.ownerSession, fixture.ownerToken = fixture.saveSession(t, fixture.owner)
	_, fixture.adminToken = fixture.saveSession(t, adminID)
	fixture.exec(t, `INSERT INTO platform_artifacts(id,release_id,architecture,platform_version,guest_release,protocol_version,owned_resources_json,
		digests_json,provenance_json,manifest_json,manifest_sha256,bundle_name,imported_by,imported_at,state)
		VALUES('artifact','release','amd64','fixture','release','1','[]','{}','{}','{}',
		'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','fixture.utm',$1,$2,'available')`, fixture.owner, stamp)
	fixture.exec(t, `INSERT INTO workspaces(id,installation_id,owner_user_id,template_id,template_revision,platform_artifact_id,repository_url,
		devcontainer_path,cpu_count,memory_mib,disk_mib,network_policy_snapshot_json,desired_state,observed_state,generation,ownership_marker,provider_instance_id,created_at,updated_at,version)
		VALUES($1,$2,$3,'template',1,'artifact','https://github.com/example/workspace','.devcontainer/devcontainer.json',2,1024,40,'{"mode":"nat"}',
		'running','running',1,$4,$5,$6,$6,1)`, fixture.ref.WorkspaceID, fixture.ref.InstallationID, fixture.owner, fixture.ref.OwnershipMarker, fixture.ref.InstanceID, stamp)
	fixture.journal = operation.NewJournal(db, time.Now, uuid.NewString)
	return fixture
}

func (f *revocationFixture) exec(t *testing.T, query string, args ...any) {
	t.Helper()
	if _, err := f.db.ExecContext(t.Context(), query, args...); err != nil {
		t.Fatal(err)
	}
}

func (f *revocationFixture) saveSession(t *testing.T, user string) (auth.Session, string) {
	t.Helper()
	session, token, err := auth.NewSession(uuid.NewString(), user, "normal", f.now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Transaction(t.Context(), f.db, func(tx *sql.Tx) error {
		return auth.NewStore(f.db).SaveSession(t.Context(), tx, session)
	}); err != nil {
		t.Fatal(err)
	}
	return session, token
}

func (f *revocationFixture) createOperation(t *testing.T, action, kind, step string) operation.Operation {
	t.Helper()
	op, err := f.journal.Create(t.Context(), operation.Request{
		Actor: f.owner, Key: uuid.NewString(), Type: kind, OriginatingAction: action,
		WorkspaceID: &f.ref.WorkspaceID, Generation: f.ref.Generation, Steps: []operation.Step{{Key: step, Label: step}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return op
}

func (f *revocationFixture) membershipHandler() http.Handler {
	handler := web.UpdateMembershipHandler(f.db, revocationManagementOrigin, func() time.Time { return f.now }, f.tunnels.Registry, f.journal)
	return &handler
}

func (f *revocationFixture) disableRequest(t *testing.T, ctx context.Context, base string) *http.Request {
	t.Helper()
	request, err := http.NewRequestWithContext(ctx, http.MethodPatch, base+"/api/v1/admin/users/"+f.owner, strings.NewReader(`{"membershipState":"disabled"}`))
	if err != nil {
		t.Fatal(err)
	}
	request.AddCookie(&http.Cookie{Name: "__Host-shiftbox_session", Value: f.adminToken})
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", revocationManagementOrigin)
	request.Header.Set("If-Match", `"1"`)
	request.Header.Set("X-CSRF-Token", auth.CSRFToken(f.adminToken))
	return request
}

func (f *revocationFixture) serviceCookie(t *testing.T, ctx context.Context, server *httptest.Server, host, service string) *http.Cookie {
	t.Helper()
	var token string
	if err := database.Transaction(ctx, f.db, func(tx *sql.Tx) error {
		var err error
		token, _, err = proxy.IssueOriginGrant(ctx, tx, proxy.OriginAuthority{
			Scope:  proxy.OriginScope{WorkspaceID: f.ref.WorkspaceID, Generation: f.ref.Generation, Service: service, Origin: "https://" + host},
			UserID: f.owner, SessionID: f.ownerSession.ID, Permission: "workspace.connect_owner",
		}, f.now)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+web.ServiceEntryPath+"?grant="+url.QueryEscape(token), nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Host = host
	response := doRevocationRequest(t, server.Client(), request, http.StatusOK)
	response.Body.Close()
	for _, cookie := range response.Cookies() {
		if cookie.Name == "__Host-shiftbox_origin" {
			return cookie
		}
	}
	t.Fatalf("entry grant for %s did not issue an origin cookie", service)
	return nil
}

func (f *revocationFixture) startGuestListeners(t *testing.T) (*app.GuestListeners, *tls.Config, operation.Claim) {
	t.Helper()
	encryption, err := cryptostore.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	authority, err := app.EnsureInstallationCA(t.TempDir(), f.ref.InstallationID, encryption, f.now)
	if err != nil {
		t.Fatal(err)
	}
	enrollment := guest.NewEnrollment(f.db, authority)
	boot := f.createOperation(t, "workspace.launch", "provision", "verify_ide")
	claim, err := f.journal.Claim(t.Context(), boot.ID, "fixture-worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	f.exec(t, "INSERT INTO guest_registrations(workspace_id,generation,state) VALUES($1,1,'eligible')", f.ref.WorkspaceID)
	grant, err := enrollment.Mint(t.Context(), boot.ID, provider.Inventory{
		Ref: f.ref, Power: provider.Stopped, InstanceID: f.ref.InstanceID, ObservedAt: f.now,
	}, f.now)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := enrollment.Enroll(t.Context(), grant, csr, f.now)
	if err != nil {
		t.Fatal(err)
	}
	const loopback = "127.0.0.1"
	config := app.GuestListenerConfig{PublicHost: loopback, EnrollmentAddress: loopback + ":0", PollAddress: loopback + ":0",
		TunnelAddress: loopback + ":0", Tunnels: f.tunnels}
	listeners, err := app.ListenGuests(config, enrollment, authority, web.GuestCompatibility{Protocol: "1", CurrentRelease: "release"}, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- listeners.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		listeners.Close()
		<-done
	})
	return listeners, guest.ClientTLS(tls.Certificate{Certificate: [][]byte{leaf}, PrivateKey: key}, authority, loopback), claim
}

func doRevocationRequest(t *testing.T, client *http.Client, request *http.Request, want int) *http.Response {
	t.Helper()
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("%s %s = %v, want HTTP %d", request.Method, request.URL.Path, err, want)
	}
	t.Cleanup(func() { response.Body.Close() })
	if response.StatusCode != want {
		t.Fatalf("%s %s = HTTP %d, want %d", request.Method, request.URL.Path, response.StatusCode, want)
	}
	return response
}
