package web_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/testsupport"
	"github.com/jflatten/ShiftBox/internal/web"
)

// TestDecodeJSONRejectsMalformedBodies refuses oversized, unknown-field and trailing documents before mutation.
func TestDecodeJSONRejectsMalformedBodies(t *testing.T) {
	for _, body := range []string{`{"name":"one","unreviewed":true}`, `{"name":"one"} {"name":"two"}`, `{"name":"` + strings.Repeat("x", 100) + `"}`, `{"name":`} {
		request := httptest.NewRequest(http.MethodPost, "https://management.test/api/v1/workspaces", strings.NewReader(body))
		var input struct {
			Name string `json:"name"`
		}
		if err := web.DecodeJSON(httptest.NewRecorder(), request, &input, 64); err == nil {
			t.Errorf("DecodeJSON(%q) accepted invalid request", body)
		}
	}
}

// TestRequestLoggingOmitsCredentials keeps URLs, bodies and headers out of operational logs.
func TestRequestLoggingOmitsCredentials(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	mux := http.NewServeMux()
	mux.HandleFunc("GET /workspaces/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(204)
		if err := http.NewResponseController(w).Flush(); err != nil {
			t.Errorf("logging wrapper broke Flush: %v", err)
		}
	})
	handler := web.WithRequestID(web.WithRequestLogging(logger, mux))
	request := httptest.NewRequest(http.MethodGet, "https://management.test/workspaces/private-workspace?token=private-query", strings.NewReader("private-body"))
	request.Header.Set("Authorization", "Bearer private-header")
	request.Header.Set("Cookie", "__Host-shiftbox_session=private-cookie")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	for _, secret := range []string{"private-workspace", "private-query", "private-body", "private-header", "private-cookie"} {
		if strings.Contains(output.String(), secret) {
			t.Errorf("request log exposed %q", secret)
		}
	}
	var entry struct {
		RequestID string `json:"requestId"`
		Status    int    `json:"status"`
		Route     string `json:"route"`
	}
	if err := json.Unmarshal(output.Bytes(), &entry); err != nil {
		t.Fatal(err)
	}
	if entry.Status != 204 || entry.Route != "GET /workspaces/{id}" || entry.RequestID != response.Header().Get("X-Request-ID") {
		t.Errorf("request log = %+v", entry)
	}
}

// TestOversizedTrailingBody retains the size-limit response even after one valid JSON value.
func TestOversizedTrailingBody(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "https://management.test", strings.NewReader(`{"name":"one"}`+strings.Repeat(" ", 100)))
	var input struct {
		Name string `json:"name"`
	}
	err := web.DecodeJSON(httptest.NewRecorder(), r, &input, 32)
	var problem web.Problem
	if !errors.As(err, &problem) || problem.Status != 413 {
		t.Errorf("DecodeJSON(oversized tail) = %v, want 413 problem", err)
	}
}

// TestConditionalHeaders rejects weak or ambiguous versions and invalid idempotency keys.
func TestConditionalHeaders(t *testing.T) {
	for _, value := range []string{"", `W/"2"`, `2`, `"2", "3"`, `"-1"`, `"9223372036854775808"`} {
		request := httptest.NewRequest(http.MethodPost, "https://management.test", nil)
		request.Header.Set("If-Match", value)
		if _, err := web.RequireVersion(request); err == nil {
			t.Errorf("RequireVersion(%q) succeeded", value)
		}
	}
	request := httptest.NewRequest(http.MethodPost, "https://management.test", nil)
	request.Header.Set("If-Match", `"12"`)
	if version, err := web.RequireVersion(request); err != nil || version != 12 {
		t.Errorf("RequireVersion(valid) = %d,%v", version, err)
	}
	for _, key := range []string{"", strings.Repeat("a", 15), strings.Repeat("a", 129)} {
		request.Header.Set("Idempotency-Key", key)
		if _, err := web.IdempotencyKey(request); err == nil {
			t.Errorf("IdempotencyKey(%d bytes) succeeded", len(key))
		}
	}
}

// TestMutationGuardRejectsSiblingOrigins blocks same-site previews and cross-session CSRF tokens.
func TestMutationGuardRejectsSiblingOrigins(t *testing.T) {
	session, token, err := auth.NewSession(testsupport.ID(1), "alice", "normal", time.Now(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	guard := web.MutationGuard("https://management.test", func(*http.Request) (auth.Session, error) { return session, nil }, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	for _, test := range []struct {
		origin, csrf string
		status       int
	}{
		{"https://management.test", auth.CSRFToken(token), 204},
		{"https://preview.management.test", auth.CSRFToken(token), 403},
		{"https://management.test/", auth.CSRFToken(token), 403},
		{"null", auth.CSRFToken(token), 403},
		{"https://management.test", "wrong-session", 403},
		{"", auth.CSRFToken(token), 403},
	} {
		request := httptest.NewRequest(http.MethodPost, "https://management.test/api/v1/workspaces", nil)
		request.Header.Set("Origin", test.origin)
		request.Header.Set("X-CSRF-Token", test.csrf)
		response := httptest.NewRecorder()
		guard.ServeHTTP(response, request)
		if response.Code != test.status {
			t.Errorf("origin=%q response=%d want=%d", test.origin, response.Code, test.status)
		}
	}
	denied := web.MutationGuard("https://management.test", func(*http.Request) (auth.Session, error) {
		return auth.Session{}, errors.New("private backend failure")
	}, guard)
	response := httptest.NewRecorder()
	denied.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "https://management.test", nil))
	if response.Code != 401 || strings.Contains(response.Body.String(), "private backend") {
		t.Error("unauthenticated mutation leaked detail or was accepted")
	}
}

// TestRequestIDsAreServerOwned prevents attacker-chosen diagnostic identifiers crossing trust boundaries.
func TestRequestIDsAreServerOwned(t *testing.T) {
	seen := ""
	handler := web.WithRequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { seen = web.RequestID(r.Context()) }))
	request := httptest.NewRequest(http.MethodGet, "https://management.test", nil)
	request.Header.Set("X-Request-ID", "attacker-value")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if !web.ValidUUID(seen) || seen == "attacker-value" || response.Header().Get("X-Request-ID") != seen {
		t.Errorf("request ID = %q, want server UUID matching response", seen)
	}
	for _, id := range []string{"", "not-a-uuid", "00000000-0000-4000-8000-00000000000g"} {
		if web.ValidUUID(id) {
			t.Errorf("ValidUUID(%q) accepted", id)
		}
	}
}
