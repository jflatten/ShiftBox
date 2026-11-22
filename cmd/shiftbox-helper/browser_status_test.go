package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestHelperBrowserStatus exposes only a current alias for the paired browser
// account and hides it when the controller or the managed config drops it.
func TestHelperBrowserStatus(t *testing.T) {
	const workspace = "00000000-0000-4000-8000-000000000001"
	const account = "paired-account"
	now := time.Now()
	session := helperSession{AccountID: account, Credential: strings.Repeat("A", 43), ExpiresAt: now.Add(time.Hour)}
	store := &memoryHelperStore{}
	var calls, status atomic.Int32
	status.Store(http.StatusNoContent)
	controller := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/api/v1/ssh/workspaces/"+workspace+"/readiness" || r.Header.Get("Authorization") != "Bearer "+session.Credential {
			t.Error("helper did not request the exact workspace with its scoped credential")
		}
		w.WriteHeader(int(status.Load()))
	}))
	t.Cleanup(controller.Close)
	if err := store.Save(controller.URL, session); err != nil {
		t.Fatal(err)
	}
	paths := sshConfigFixture(t)
	if err := reconcileSSHConfig(paths, controller.URL, account, []sshAliasWorkspace{{ID: workspace, Name: "Demo project"}}); err != nil {
		t.Fatal(err)
	}
	handler := helperBrowserStatus{origin: controller.URL, paths: paths, store: store, client: controller.Client(), now: func() time.Time { return now }}
	check := func(origin, host, browserAccount string, want int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "http://"+host+"/v1/ssh-alias?workspace="+workspace+"&account="+browserAccount, nil)
		req.Header.Set("Origin", origin)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		if response.Code != want {
			t.Errorf("alias status for origin %q, host %q, account %q = %d, want %d", origin, host, browserAccount, response.Code, want)
		}
		return response
	}
	denied := check("https://different.test", "127.0.0.1:48652", account, http.StatusForbidden)
	if denied.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Error("foreign origin unexpectedly received CORS access")
	}
	check(controller.URL, "rebinding.test:48652", account, http.StatusForbidden)
	check(controller.URL, "127.0.0.1:48652", "different-account", http.StatusNotFound)
	if got := calls.Load(); got != 0 {
		t.Errorf("unauthorized browser lookups made %d controller requests, want zero", got)
	}
	response := check(controller.URL, "127.0.0.1:48652", account, http.StatusOK)
	var result struct{ Alias string }
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || result.Alias != "shiftbox-demo-project" ||
		response.Header().Get("Access-Control-Allow-Origin") != controller.URL || strings.Contains(response.Body.String(), session.Credential) {
		t.Errorf("browser status = %q (%v), want the alias only with exact-origin CORS", response.Body.String(), err)
	}
	status.Store(http.StatusUnauthorized)
	check(controller.URL, "127.0.0.1:48652", account, http.StatusNotFound)
	status.Store(http.StatusNoContent)
	if err := reconcileSSHConfig(paths, controller.URL, account, nil); err != nil {
		t.Fatal(err)
	}
	check(controller.URL, "127.0.0.1:48652", account, http.StatusNotFound)
	if got := calls.Load(); got != 2 {
		t.Errorf("status requests with a configured alias = %d, want 2", got)
	}
}
