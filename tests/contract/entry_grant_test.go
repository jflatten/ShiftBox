package contract_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/config"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/operation"
	"github.com/jflatten/ShiftBox/internal/proxy"
	"github.com/jflatten/ShiftBox/internal/web"
)

// TestAdministratorConnectionGrantRevocation verifies exact workspace scope,
// durable revocation before live closure, and retry after a closure failure.
func TestAdministratorConnectionGrantRevocation(t *testing.T) {
	ctx := t.Context()
	db, err := database.Open(ctx, database.Config{Profile: "sqlite", SQLitePath: filepath.Join(t.TempDir(), "grants.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := database.Migrate(ctx, db, "sqlite"); err != nil {
		t.Fatal(err)
	}
	const origin = "https://management.test"
	const id = "00000000-0000-4000-8000-000000000004"
	const other = "00000000-0000-4000-8000-000000000005"
	const grant = "00000000-0000-4000-8000-000000000006"
	memberToken, adminToken := strings.Repeat("A", 43), strings.Repeat("B", 42)+"A"
	now := time.Now().UTC()
	seedWorkspaceLaunch(t, db, "00000000-0000-4000-8000-000000000003", memberToken, now)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	for _, ws := range []string{id, other} {
		exec(`INSERT INTO workspaces(id,installation_id,owner_user_id,template_id,template_revision,platform_artifact_id,
			repository_url,devcontainer_path,cpu_count,memory_mib,disk_mib,network_policy_snapshot_json,desired_state,observed_state,
			generation,ownership_marker,provider_instance_id,created_at,updated_at,version)
			SELECT $1,id,'member','template',1,'artifact','https://github.com/example/repo','devcontainer.json',1,512,100,'{}',
			'running','running',1,$1,$1,$2,$2,1 FROM installations`, ws, now.Format(time.RFC3339Nano))
	}
	exec(`INSERT INTO resource_grants(id,subject_user_id,resource_type,resource_id,action,granted_by,granted_at)
		VALUES($1,'owner','workspace',$2,'workspace.connect_admin','owner',$3)`, grant, id, now.Format(time.RFC3339Nano))
	exec(`INSERT INTO origin_grants(id,token_digest,user_id,session_id,workspace_id,generation,service_key,origin,permission_basis,resource_grant_id,expires_at)
		VALUES('origin','digest','owner','admin-session',$1,1,'ide','https://ide.test','workspace.connect_admin',$2,$3)`, id, grant, now.Add(time.Minute).Format(time.RFC3339Nano))
	if err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
		return auth.NewStore(db).SaveSession(ctx, tx, auth.Session{ID: "admin-session", UserID: "owner",
			TokenDigest: auth.TokenDigest(adminToken), CSRFDigest: auth.TokenDigest(auth.CSRFToken(adminToken)),
			Mode: "normal", AuthenticatedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour)})
	}); err != nil {
		t.Fatal(err)
	}
	registry := &proxy.Registry{}
	closures := 0
	_, err = registry.Register(proxy.Scope{WorkspaceID: id, GrantID: grant}, func() error { return nil }, func() error {
		closures++
		var revoked, originRevoked bool
		if err := db.QueryRowContext(ctx, `SELECT r.revoked_at IS NOT NULL,o.revoked_at IS NOT NULL FROM resource_grants r
			JOIN origin_grants o ON o.resource_grant_id=r.id WHERE r.id=$1`, grant).Scan(&revoked, &originRevoked); err != nil || !revoked || !originRevoked {
			t.Errorf("connection close saw resource/origin revocation %t/%t (%v), want both committed", revoked, originRevoked, err)
		}
		if closures == 1 {
			return errors.New("fixture close failure")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = registry.Register(proxy.Scope{WorkspaceID: id}, func() error { return nil }, func() error {
		t.Error("revocation closed the workspace owner's unrelated connection")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	list, revoke := web.WorkspaceConnectionGrantHandlers(db, origin, func() time.Time { return now }, registry)
	mux := http.NewServeMux()
	path := "/api/v1/admin/workspaces/"
	mux.Handle("GET "+path+"{workspaceId}/connection-grants", &list)
	mux.Handle("DELETE "+path+"{workspaceId}/connection-grants/{grantId}", &revoke)
	request := func(method, ws, token, csrf string, want int) string {
		t.Helper()
		target := path + ws + "/connection-grants"
		if method == http.MethodDelete {
			target += "/" + grant
		}
		r := httptest.NewRequest(method, origin+target, nil)
		r.AddCookie(&http.Cookie{Name: "__Host-shiftbox_session", Value: token})
		r.Header.Set("Origin", origin)
		r.Header.Set("X-CSRF-Token", csrf)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s returned %d %s, want %d", method, ws, w.Code, w.Body, want)
		}
		return w.Body.String()
	}
	csrf := auth.CSRFToken(adminToken)
	if body := request(http.MethodGet, id, adminToken, csrf, 200); !strings.Contains(body, "owner@example.test") {
		t.Errorf("active connection grant listing = %s, want administrator email", body)
	}
	request(http.MethodGet, id, memberToken, "", 403)
	request(http.MethodDelete, id, memberToken, auth.CSRFToken(memberToken), 403)
	request(http.MethodDelete, other, adminToken, csrf, 404)
	request(http.MethodDelete, id, adminToken, "invalid", 403)
	request(http.MethodDelete, id, adminToken, csrf, 503)
	request(http.MethodDelete, id, adminToken, csrf, 204)
	if body := request(http.MethodGet, id, adminToken, csrf, 200); strings.Contains(body, grant) {
		t.Errorf("revoked grant was returned in active list: %s", body)
	}
	var events int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM audit_events WHERE action='workspace.manage_connection_grants' AND reason_code='connection_grant_revoked'").Scan(&events); err != nil || events != 1 || closures != 2 {
		t.Errorf("revoke produced %d audit events and %d closure attempts (%v), want one event and two attempts", events, closures, err)
	}
}

// TestWorkspaceEntryGrantAdmission checks the exact destination and that a
// member may mint a grant for their own running workspace only. IDE grants
// require readiness evidence from the most recent boot.
func TestWorkspaceEntryGrantAdmission(t *testing.T) {
	ctx := t.Context()
	db, err := database.Open(ctx, database.Config{Profile: "sqlite", SQLitePath: filepath.Join(t.TempDir(), "entry.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(ctx, db, "sqlite"); err != nil {
		t.Fatal(err)
	}
	const origin = "https://management.test"
	const workspace = "00000000-0000-4000-8000-000000000004"
	const other = "00000000-0000-4000-8000-000000000005"
	token := strings.Repeat("A", 43)
	now := time.Now().UTC()
	seedWorkspaceLaunch(t, db, "00000000-0000-4000-8000-000000000003", token, now)
	for id, owner := range map[string]string{workspace: "member", other: "owner"} {
		if _, err := db.Exec(`INSERT INTO workspaces(id,installation_id,owner_user_id,template_id,template_revision,platform_artifact_id,
			repository_url,devcontainer_path,cpu_count,memory_mib,disk_mib,network_policy_snapshot_json,desired_state,observed_state,
			generation,ownership_marker,provider_instance_id,created_at,updated_at,version)
			SELECT $1,id,$2,'template',1,'artifact','https://github.com/example/repo','devcontainer.json',1,512,100,'{}',
			'running','running',1,$1,$1,$3,$3,1 FROM installations`, id, owner, now.Format(time.RFC3339Nano)); err != nil {
			t.Fatal(err)
		}
	}
	hosts, err := config.NewServiceHosts(origin, "{workspace}.ide.test", "{service}--{workspace}.preview.test")
	if err != nil {
		t.Fatal(err)
	}
	handler := web.WorkspaceEntryGrantHandler(db, origin, func() time.Time { return now }, hosts)
	_, detail := web.WorkspaceReadHandlers(db, origin, func() time.Time { return now }, nil)
	canOpenIDE := func(want bool) {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, origin+"/api/v1/workspaces/"+workspace, nil)
		r.SetPathValue("workspaceId", workspace)
		r.AddCookie(&http.Cookie{Name: "__Host-shiftbox_session", Value: token})
		w := httptest.NewRecorder()
		detail.ServeHTTP(w, r)
		var view struct {
			CanOpenIDE *bool `json:"canOpenIde"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil || w.Code != 200 || view.CanOpenIDE == nil || *view.CanOpenIDE != want {
			t.Fatalf("workspace detail = %d %s (%v), want canOpenIde=%t", w.Code, w.Body, err, want)
		}
	}
	requestAs := func(token, id, service, csrf string, want int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, origin+"/api/v1/workspaces/"+id+"/entry-grants", strings.NewReader(`{"service":"`+service+`"}`))
		r.SetPathValue("workspaceId", id)
		r.AddCookie(&http.Cookie{Name: "__Host-shiftbox_session", Value: token})
		r.Header.Set("Origin", origin)
		r.Header.Set("X-CSRF-Token", csrf)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("entry grant for %s/%s = %d %s, want %d", id, service, w.Code, w.Body, want)
		}
		return w
	}
	request := func(id, service, csrf string, want int) *httptest.ResponseRecorder {
		t.Helper()
		return requestAs(token, id, service, csrf, want)
	}
	csrf := auth.CSRFToken(token)
	request(workspace, "preview-8080", "invalid", 403)
	request(other, "preview-8080", csrf, 403)
	request(workspace, "preview-8080", csrf, 403)
	if _, err := db.ExecContext(ctx, "UPDATE workspaces SET preview_ports_json='[8080]' WHERE id=$1", workspace); err != nil {
		t.Fatal(err)
	}
	request(workspace, "ide", csrf, 403)
	canOpenIDE(false)
	bootTime := now.Add(-time.Hour)
	journal := operation.NewJournal(db, func() time.Time { return bootTime }, func() string { return bootTime.Format(time.RFC3339) })
	recordBoot := func(action, kind, key string) operation.Operation {
		t.Helper()
		id := workspace
		op, err := journal.Create(ctx, operation.Request{Actor: "member", Key: key, Type: kind,
			OriginatingAction: action, WorkspaceID: &id, Generation: 1,
			Steps: []operation.Step{{Key: "verify_ide", Label: "Verify IDE"}}})
		if err != nil {
			t.Fatal(err)
		}
		return op
	}
	verifyBoot := func(op operation.Operation) {
		t.Helper()
		claim, err := journal.Claim(ctx, op.ID, "fixture", time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if err := journal.StartStep(ctx, claim, 0); err != nil {
			t.Fatal(err)
		}
		if err := journal.CompleteStep(ctx, claim, 0, "Verified"); err != nil {
			t.Fatal(err)
		}
	}
	verifyBoot(recordBoot("workspace.launch", "provision", "provision-fixture"))
	request(workspace, "ide", csrf, 201)
	canOpenIDE(true)
	bootTime = now
	restart := recordBoot("workspace.start", "start", "workspace-restart")
	request(workspace, "ide", csrf, 403)
	canOpenIDE(false)
	verifyBoot(restart)
	request(workspace, "ide", csrf, 201)
	canOpenIDE(true)
	result := request(workspace, "preview-8080", csrf, 201)
	var grant struct {
		ExchangeURL string    `json:"exchangeUrl"`
		ExpiresAt   time.Time `json:"expiresAt"`
	}
	if err := json.Unmarshal(result.Body.Bytes(), &grant); err != nil ||
		!strings.HasPrefix(grant.ExchangeURL, "https://preview-8080--"+workspace+".preview.test/_shiftbox/entry?grant=") ||
		!grant.ExpiresAt.Equal(now.Add(time.Minute)) {
		t.Fatal("response did not bind a one-minute grant to the exact service origin")
	}
	destination, err := url.Parse(grant.ExchangeURL)
	if err != nil {
		t.Fatal(err)
	}
	// A newer repair workflow closes normal IDE admission and only opens the
	// dedicated repair origin after its own verification step succeeds.
	bootTime = now.Add(time.Second)
	id := workspace
	repair, err := journal.Create(ctx, operation.Request{Actor: "member", Key: "workspace-repair-fixture", Type: "repair",
		OriginatingAction: "workspace.repair", WorkspaceID: &id, Generation: 1,
		Steps: []operation.Step{{Key: "verify_repair", Label: "Verify repair IDE"}}})
	if err != nil {
		t.Fatal(err)
	}
	request(workspace, "ide", csrf, 403)
	request(workspace, "repair", csrf, 403)
	verifyBoot(repair)
	request(workspace, "ide", csrf, 403)
	response := request(workspace, "repair", csrf, 201)
	var repairGrant struct{ ExchangeURL string }
	if err := json.Unmarshal(response.Body.Bytes(), &repairGrant); err != nil ||
		!strings.HasPrefix(repairGrant.ExchangeURL, "https://"+workspace+".repair.test/_shiftbox/entry?grant=") {
		t.Fatalf("repair entry response = %s, %v, want a grant for the repair hostname", response.Body, err)
	}
	// The retry intent closes repair access immediately, including a grant
	// issued before exit. Normal access waits for the new verification.
	power := web.WorkspacePowerHandler(db, origin, func() time.Time { return bootTime.Add(time.Second) }, journal, &proxy.Registry{})
	r := httptest.NewRequest(http.MethodPost, origin+"/api/v1/workspaces/"+workspace+"/actions/retry", nil)
	r.SetPathValue("workspaceId", workspace)
	r.SetPathValue("action", "retry")
	r.AddCookie(&http.Cookie{Name: "__Host-shiftbox_session", Value: token})
	r.Header.Set("Origin", origin)
	r.Header.Set("X-CSRF-Token", csrf)
	r.Header.Set("If-Match", `"1"`)
	r.Header.Set("Idempotency-Key", "workspace-exit-repair")
	retryResponse := httptest.NewRecorder()
	bootTime = bootTime.Add(time.Second)
	power.ServeHTTP(retryResponse, r)
	if retryResponse.Code != http.StatusAccepted {
		t.Fatalf("exit repair and retry = %d %s, want 202", retryResponse.Code, retryResponse.Body)
	}
	request(workspace, "repair", csrf, 403)
	request(workspace, "ide", csrf, 403)
	forwarded := 0
	registry := &proxy.Registry{}
	streamStarted := make(chan struct{})
	streamClosed := make(chan struct{})
	serviceProxy := web.ServiceProxyHandler(db, hosts, registry, func() time.Time { return now },
		func(w http.ResponseWriter, r *http.Request, id string, generation int64, service, requestID string) {
			forwarded++
			if id != workspace || generation != 1 || service != "preview-8080" || requestID == "" || r.Header.Get("Cookie") != "app=fixture" {
				t.Error("service proxy forwarded incorrect scope or private browser credentials")
			}
			if r.URL.Path == "/stream" {
				close(streamStarted)
				<-r.Context().Done()
				return
			}
			w.WriteHeader(http.StatusNoContent)
		})
	router, err := web.NewBrowserRouter(hosts, handler, serviceProxy)
	if err != nil {
		t.Fatal(err)
	}
	send := func(method, uri, requestOrigin string, cookie *http.Cookie, want int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, uri, nil)
		if requestOrigin != "" {
			r.Header.Set("Origin", requestOrigin)
		}
		if cookie != nil {
			r.AddCookie(cookie)
			r.AddCookie(&http.Cookie{Name: "app", Value: "fixture"})
		}
		response := httptest.NewRecorder()
		router.ServeHTTP(response, r)
		if response.Code != want {
			t.Fatalf("%s %s returned %d, want %d", method, r.URL.Path, response.Code, want)
		}
		return response
	}
	serviceOrigin := "https://" + destination.Host
	// An administrator needs the explicit grant even on a running preview.
	// Removing that permission also invalidates an origin cookie already issued.
	admin, adminToken, err := auth.NewSession("admin-preview-session", "owner", "normal", now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
		return auth.NewStore(db).SaveSession(ctx, tx, admin)
	}); err != nil {
		t.Fatal(err)
	}
	adminCSRF := auth.CSRFToken(adminToken)
	requestAs(adminToken, workspace, "preview-8080", adminCSRF, 403)
	if _, err := db.ExecContext(ctx, `INSERT INTO resource_grants(id,subject_user_id,resource_type,resource_id,action,granted_by,granted_at)
		VALUES('preview-admin-grant','owner','workspace',$1,'workspace.connect_admin','owner',$2)`, workspace, now.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	var adminGrant struct{ ExchangeURL string }
	adminResponse := requestAs(adminToken, workspace, "preview-8080", adminCSRF, 201)
	if err := json.Unmarshal(adminResponse.Body.Bytes(), &adminGrant); err != nil {
		t.Fatal(err)
	}
	var audited int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM audit_events WHERE action='workspace.connect_admin'
		AND outcome='allowed' AND actor_id='owner' AND resource_id=$1`, workspace).Scan(&audited); err != nil || audited != 1 {
		t.Fatalf("administrator entry audit count = %d (%v), want one before redemption", audited, err)
	}
	adminCookies := send("GET", adminGrant.ExchangeURL, "", nil, 200).Result().Cookies()
	if len(adminCookies) != 1 {
		t.Fatal("administrator entry did not issue a service cookie")
	}
	send("GET", serviceOrigin+"/", "", adminCookies[0], 204)
	if _, err := db.ExecContext(ctx, `UPDATE resource_grants SET revoked_at=$1 WHERE id='preview-admin-grant'`, now.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	send("GET", serviceOrigin+"/", "", adminCookies[0], 403)
	send("GET", repairGrant.ExchangeURL, "", nil, 403)
	sibling := strings.Replace(serviceOrigin, "preview-8080", "preview-9000", 1)
	send("GET", sibling+destination.RequestURI(), "", nil, 403)
	redeemed := send("GET", grant.ExchangeURL, "", nil, 200)
	cookies := redeemed.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Domain != "" || cookies[0].Path != "/" || !cookies[0].Secure ||
		!cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode || redeemed.Header().Get("Location") != "" ||
		redeemed.Header().Get("Referrer-Policy") != "no-referrer" || !strings.Contains(redeemed.Body.String(), `location.replace("/")`) ||
		strings.Contains(redeemed.Body.String(), destination.RawQuery) {
		t.Fatal("redemption did not produce a host-only cookie and clean same-origin navigation")
	}
	send("GET", grant.ExchangeURL, "", nil, 403)
	send("GET", serviceOrigin+"/", "", nil, 403)
	send("GET", sibling+"/", "", cookies[0], 403)
	send("POST", serviceOrigin+"/", sibling, cookies[0], 403)
	send("GET", serviceOrigin+"/", "", cookies[0], 204)
	send("POST", serviceOrigin+"/", serviceOrigin, cookies[0], 204)
	stream := httptest.NewRequest(http.MethodGet, serviceOrigin+"/stream", nil)
	stream.AddCookie(cookies[0])
	stream.AddCookie(&http.Cookie{Name: "app", Value: "fixture"})
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		defer close(streamClosed)
		router.ServeHTTP(httptest.NewRecorder(), stream.WithContext(streamCtx))
	}()
	select {
	case <-streamStarted:
	case <-time.After(time.Second):
		t.Fatal("authorized streaming request did not reach forwarding")
	}
	if err := auth.NewStore(db).RevokeAndClose(ctx, "launch-session", now, "session.logout", registry); err != nil {
		t.Fatal(err)
	}
	select {
	case <-streamClosed:
	case <-time.After(time.Second):
		t.Fatal("session revocation did not cancel the active proxy request")
	}
	send("GET", serviceOrigin+"/", "", cookies[0], 403)
	if forwarded != 4 {
		t.Errorf("forwarded %d requests, want four authorized requests", forwarded)
	}
	// Removing the port cancels existing browser traffic and revokes previously
	// issued cookies. Re-adding the same port cannot restore those cookies.
	replacement, replacementToken, err := auth.NewSession("preview-removal-session", "member", "normal", now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
		return auth.NewStore(db).SaveSession(ctx, tx, replacement)
	}); err != nil {
		t.Fatal(err)
	}
	var replacementGrant struct{ ExchangeURL string }
	newGrant := requestAs(replacementToken, workspace, "preview-8080", auth.CSRFToken(replacementToken), 201)
	if err := json.Unmarshal(newGrant.Body.Bytes(), &replacementGrant); err != nil {
		t.Fatal(err)
	}
	previewCookie := send("GET", replacementGrant.ExchangeURL, "", nil, 200).Result().Cookies()[0]
	var activeVersion int64
	if err := db.QueryRowContext(ctx, "SELECT version FROM workspaces WHERE id=$1", workspace).Scan(&activeVersion); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "UPDATE operations SET status='cancelled' WHERE workspace_id=$1 AND status IN ('pending','running','waiting')", workspace); err != nil {
		t.Fatal(err)
	}
	streamStarted, streamClosed = make(chan struct{}), make(chan struct{})
	preview := httptest.NewRequest(http.MethodGet, serviceOrigin+"/stream", nil).WithContext(streamCtx)
	preview.AddCookie(previewCookie)
	preview.AddCookie(&http.Cookie{Name: "app", Value: "fixture"})
	go func() {
		defer close(streamClosed)
		router.ServeHTTP(httptest.NewRecorder(), preview)
	}()
	select {
	case <-streamStarted:
	case <-time.After(time.Second):
		t.Fatal("preview stream did not open")
	}
	policy := web.WorkspacePolicyHandler(db, origin, func() time.Time { return now }, web.WorkspaceLaunchPolicy{}, registry)
	patchPorts := func(body, version string) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPatch, origin+"/api/v1/workspaces/"+workspace, strings.NewReader(body))
		req.SetPathValue("workspaceId", workspace)
		req.AddCookie(&http.Cookie{Name: "__Host-shiftbox_session", Value: replacementToken})
		req.Header.Set("Origin", origin)
		req.Header.Set("If-Match", version)
		req.Header.Set("X-CSRF-Token", auth.CSRFToken(replacementToken))
		response := httptest.NewRecorder()
		policy.ServeHTTP(response, req)
		if response.Code != 200 {
			t.Fatalf("preview update = %d, want 200", response.Code)
		}
	}
	patchPorts(`{"previewPorts":[]}`, strconv.Quote(strconv.FormatInt(activeVersion, 10)))
	select {
	case <-streamClosed:
	case <-time.After(time.Second):
		t.Fatal("preview stream remained open after the port was removed")
	}
	patchPorts(`{"previewPorts":[8080]}`, strconv.Quote(strconv.FormatInt(activeVersion+1, 10)))
	send("GET", serviceOrigin+"/", "", previewCookie, 403)
}
