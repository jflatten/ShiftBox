package web_test

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/proxy"
	"github.com/jflatten/ShiftBox/internal/web"
	"github.com/jflatten/ShiftBox/internal/workspacelog"
)

// Even an administrator with workspace metadata access cannot read another
// owner's project output. An owner can resume and receive new logs after an idle
// HTTP/2 connection has outlived the budget for an individual write.
func TestWorkspaceLogStreamOwnerAndResume(t *testing.T) {
	db := keepAliveDatabase(t)
	session, principal, ownerToken := sessionHandlerFixture(t)
	executeLifecycleFixture(t, db, `INSERT INTO users(id,primary_email,role,membership_state,credential_mode,created_by,created_at,updated_at,version)
		VALUES('another-user','admin@example.test','administrator','active','local','setup','now','now',1)`)
	admin, adminToken, err := auth.NewSession("admin-session", "another-user", "normal", session.Now(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	for _, login := range []auth.Session{principal.Session, admin} {
		executeLifecycleFixture(t, db, `INSERT INTO sessions
			(id,token_digest,user_id,mode,csrf_digest,authenticated_at,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7)`,
			login.ID, login.TokenDigest, login.UserID, login.Mode, login.CSRFDigest,
			login.AuthenticatedAt.Format(time.RFC3339Nano), login.ExpiresAt.Format(time.RFC3339Nano))
	}
	var store workspacelog.Store
	store.Append(lifecycleWorkspaceID, "first", "building image")
	cursor := strconv.FormatInt(store.After(lifecycleWorkspaceID, 0)[0].ID, 10)
	handler := web.WorkspaceLogsHandler(db, session.Origin, session.Now, &proxy.Registry{}, &store)
	mux := http.NewServeMux()
	mux.Handle("GET /workspaces/{workspaceId}/logs", &handler)
	server := httptest.NewUnstartedServer(mux)
	server.EnableHTTP2 = true
	server.StartTLS()
	defer server.Close()

	request := func(token string) *http.Response {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 12*time.Second)
		t.Cleanup(cancel)
		r, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/workspaces/"+lifecycleWorkspaceID+"/logs", nil)
		if err != nil {
			t.Fatal(err)
		}
		r.AddCookie(&http.Cookie{Name: "__Host-shiftbox_session", Value: token})
		r.Header.Set("Last-Event-ID", cursor)
		response, err := server.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		return response
	}
	denied := request(adminToken)
	denied.Body.Close()
	if denied.StatusCode != http.StatusNotFound {
		t.Fatalf("another owner received status %d", denied.StatusCode)
	}

	owner := request(ownerToken)
	defer owner.Body.Close()
	if owner.StatusCode != http.StatusOK || owner.ProtoMajor != 2 || owner.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("owner received %s status %d and content type %q", owner.Proto, owner.StatusCode, owner.Header.Get("Content-Type"))
	}
	// A live stream can have no guest output before its next heartbeat.
	time.Sleep(5500 * time.Millisecond)
	store.Append(lifecycleWorkspaceID, "second", "image ready")
	lines := bufio.NewScanner(owner.Body)
	for lines.Scan() {
		if line := lines.Text(); strings.HasPrefix(line, "data: ") {
			if line != `data: {"text":"image ready"}` {
				t.Fatalf("resumed stream received %q", line)
			}
			return
		}
	}
	t.Fatalf("stream ended before receiving live output: %v", lines.Err())
}
