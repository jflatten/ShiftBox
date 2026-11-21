package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jflatten/ShiftBox/internal/workspacessh"
)

const helperBrowserAddress = "127.0.0.1:48652"

// helperBrowserStatus bridges the authorized helper and its management origin.
// Only the locally managed alias is shared with the browser.
type helperBrowserStatus struct {
	origin string
	paths  sshConfigPaths
	store  helperCredentialStore
	client *http.Client
	now    func() time.Time
}

func (s helperBrowserStatus) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Vary", "Origin")
	if r.Host != helperBrowserAddress || len(r.Header.Values("Origin")) != 1 || r.Header.Get("Origin") != s.origin {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	if r.URL.Path != "/v1/ssh-alias" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	w.Header().Set("Access-Control-Allow-Origin", s.origin)
	if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") == http.MethodGet {
		w.Header().Set("Access-Control-Allow-Methods", http.MethodGet)
		if r.Header.Get("Access-Control-Request-Private-Network") == "true" {
			w.Header().Set("Access-Control-Allow-Private-Network", "true")
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET, OPTIONS")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	workspace := r.URL.Query().Get("workspace")
	id, err := uuid.Parse(workspace)
	if err != nil || id.String() != workspace {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	account := r.URL.Query().Get("account")
	session, err := loadProxySession(s.store, s.origin, account, s.now())
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	alias := s.paths.activeAlias(s.origin, account, workspace)
	if alias == "" || !s.workspaceReady(r.Context(), workspace, session) {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(struct {
		Alias string `json:"alias"`
	}{Alias: alias})
}

func (s helperBrowserStatus) workspaceReady(ctx context.Context, workspace string, session helperSession) bool {
	ctx, cancel := context.WithTimeout(ctx, 7*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, s.origin+workspacessh.ReadinessPath(workspace), nil)
	if err != nil {
		return false
	}
	request.Header.Set("Authorization", "Bearer "+session.Credential)
	response, err := helperHTTPClient(s.client).Do(request)
	if err != nil {
		return false
	}
	defer response.Body.Close()
	return response.StatusCode == http.StatusNoContent && session.validAt(s.now())
}

func (paths sshConfigPaths) activeAlias(origin, account, workspace string) string {
	file, err := os.Open(paths.AliasStateFile)
	if err != nil {
		return ""
	}
	defer file.Close()
	var histories map[string]map[string]string
	if err := readHelperResponse(file, &histories, 1<<20); err != nil {
		return ""
	}
	alias := histories[origin+"\x00"+account][workspace]
	if !validManagedAlias(alias) {
		return ""
	}
	config, _, err := readSSHUserConfig(paths.ConfigFile)
	if err != nil {
		return ""
	}
	// The history also includes inactive aliases. Verify the current managed
	// stanza points to the same account, workspace, and installed helper.
	prefix, managed := strings.CutPrefix(string(config), sshConfigStart+"\n")
	block, _, complete := strings.Cut(prefix, "\n"+sshConfigEnd+"\n")
	stanza := "\nHost " + alias + "\n    HostName " + alias + "\n    " + paths.sshProxyDirective(origin, account, workspace) + "\n"
	if !managed || !complete || !strings.Contains(block, stanza) {
		return ""
	}
	return alias
}
