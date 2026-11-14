package contract_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
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
			'running','running',1,$1,$1,'now','now',1 FROM installations`, id, owner); err != nil {
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
	request := func(id, service, csrf string, want int) *httptest.ResponseRecorder {
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
	csrf := auth.CSRFToken(token)
	request(workspace, "preview-8080", "invalid", 403)
	request(other, "preview-8080", csrf, 403)
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
	sibling := strings.Replace(serviceOrigin, "preview-8080", "preview-9000", 1)
	send("GET", sibling+destination.RequestURI(), "", nil, 403)
	redeemed := send("GET", grant.ExchangeURL, "", nil, 303)
	cookies := redeemed.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Domain != "" || cookies[0].Path != "/" || !cookies[0].Secure ||
		!cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode || redeemed.Header().Get("Location") != "/" ||
		redeemed.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatal("redemption did not produce a host-only cookie and a clean redirect")
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
	if forwarded != 3 {
		t.Errorf("forwarded %d requests, want three authorized requests", forwarded)
	}
}
