package contract_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/uuid"
	"github.com/jflatten/ShiftBox/internal/app"
	"github.com/jflatten/ShiftBox/internal/cryptostore"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/guest"
	"github.com/jflatten/ShiftBox/internal/operation"
	"github.com/jflatten/ShiftBox/internal/provider"
	"github.com/jflatten/ShiftBox/internal/web"
	"github.com/jflatten/ShiftBox/internal/workspace"
)

// TestGuestEnrollmentHTTP checks enrollment over server-authenticated TLS,
// limits inputs, and rejects incompatible agents before issuing credentials.
func TestGuestEnrollmentHTTP(t *testing.T) {
	now := time.Now()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, key)
	if err != nil {
		t.Fatal(err)
	}
	const token = "test-grant"
	calls := 0
	handler := web.GuestEnrollmentHandler(web.GuestCompatibility{Protocol: "1", CurrentRelease: "current", PreviousRelease: "previous"},
		func(_ context.Context, grant string, der []byte, stamp time.Time) ([]byte, error) {
			calls++
			if grant != token || !bytes.Equal(der, csr) || !stamp.Equal(now) {
				t.Error("wrong enrollment inputs")
			}
			return []byte{1, 2, 3}, nil
		}, func() time.Time { return now })
	server := httptest.NewUnstartedServer(handler)
	server.TLS = guest.EnrollmentTLS(tls.Certificate{})
	server.TLS.Certificates = nil // httptest supplies a certificate trusted by its client.
	server.StartTLS()
	defer server.Close()

	csrPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csr}))
	body := map[string]string{"grant": token, "csrPem": csrPEM, "guestRelease": "unsupported", "protocolVersion": "1"}
	post := func(path string, want int, code string) []byte {
		t.Helper()
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := server.Client().Post(server.URL+path, "application/json", bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		var problem web.Problem
		if resp.StatusCode != want || (code != "" && (json.Unmarshal(raw, &problem) != nil || problem.Code != code)) {
			t.Fatalf("POST %s = %d %s, want %d %s", path, resp.StatusCode, raw, want, code)
		}
		return raw
	}
	post("/internal/v1/enroll", 409, "guest.release_unsupported")
	body["guestRelease"] = "previous"
	body["protocolVersion"] = "2"
	post("/internal/v1/enroll", 409, "guest.release_unsupported")
	body["protocolVersion"] = "1"
	body["workspaceId"] = "forged"
	post("/internal/v1/enroll", 400, "request.invalid_body")
	delete(body, "workspaceId")
	post("/internal/v1/guest/poll", 404, "")
	if calls != 0 {
		t.Fatalf("enrollment called for invalid requests %d times", calls)
	}
	raw := post("/internal/v1/enroll", 200, "")
	var result struct {
		CertificatePEM string `json:"certificatePem"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	cert, _ := pem.Decode([]byte(result.CertificatePEM))
	if calls != 1 || cert == nil || cert.Type != "CERTIFICATE" || !bytes.Equal(cert.Bytes, []byte{1, 2, 3}) {
		t.Fatal("response did not contain the issued certificate")
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("POST", "/internal/v1/enroll", nil))
	if rec.Code != 403 || calls != 1 {
		t.Fatalf("plaintext request = %d, calls %d, want 403 and no issuance", rec.Code, calls)
	}
}

// TestGuestPollContract uses a real enrollment and poll over dedicated TLS
// listeners to verify pinned compatibility, durable retries, generation
// fencing, and independent controller-side IDE readiness.
func TestGuestPollContract(t *testing.T) {
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)
	var clockOffset atomic.Int64
	clock := func() time.Time { return now.Add(time.Duration(clockOffset.Load())) }
	dir := t.TempDir()
	db, err := database.Open(ctx, database.Config{Profile: "sqlite", SQLitePath: filepath.Join(dir, "guest.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := database.Migrate(ctx, db, "sqlite"); err != nil {
		t.Fatal(err)
	}
	const installation = "00000000-0000-4000-8000-000000000001"
	const workspaceID = "00000000-0000-4000-8000-000000000004"
	const template = "00000000-0000-4000-8000-000000000003"
	seedWorkspaceLaunch(t, db, template, strings.Repeat("A", 43), now)
	journal := operation.NewJournal(db, clock, uuid.NewString)
	var op operation.Operation
	if err := database.Transaction(ctx, db, func(tx *sql.Tx) error {
		op, err = workspace.AdmitLaunch(ctx, tx, journal, workspace.LaunchIntent{
			InstallationID: installation, WorkspaceID: workspaceID, OwnerID: "owner",
			TemplateID: template, TemplateRevision: 1, ArtifactID: "artifact",
			RequestKey: "guest-protocol-fixture", RepositoryURL: "https://github.com/example/repo",
			DevcontainerPath: ".devcontainer/devcontainer.json", Allocation: workspace.Allocation{CPU: 2, MemoryMiB: 1024, DiskMiB: 40},
			NetworkPolicyJSON: `{"mode":"shared"}`, OwnershipMarker: "fixture-owner",
		}, now)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	claim, err := journal.Claim(ctx, op.ID, "fixture-worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	// This fixture begins at a verified provider boundary. The existing UTM
	// harness exercises provider effects independently of the protocol.
	if _, err := db.ExecContext(ctx, `UPDATE workspaces SET provider_instance_id='fixture-vm',observed_state='stopped' WHERE id=$1`, workspaceID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO guest_registrations(workspace_id,generation,state) VALUES($1,1,'eligible')`, workspaceID); err != nil {
		t.Fatal(err)
	}
	cipher, err := cryptostore.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	authority, err := app.EnsureInstallationCA(dir, installation, cipher, now)
	if err != nil {
		t.Fatal(err)
	}
	enrollment := guest.NewEnrollment(db, authority)
	ref := provider.WorkspaceRef{InstallationID: installation, WorkspaceID: workspaceID, Generation: 1, OwnershipMarker: "fixture-owner", InstanceID: "fixture-vm"}
	token, err := enrollment.Mint(ctx, op.ID, provider.Inventory{Ref: ref, InstanceID: ref.InstanceID, Power: provider.Stopped, ObservedAt: now}, now)
	if err != nil {
		t.Fatal(err)
	}
	compatibility := web.GuestCompatibility{Protocol: "1", CurrentRelease: "current", PreviousRelease: "previous"}
	serverCert, err := authority.ServerLeaf([]string{"127.0.0.1"}, now)
	if err != nil {
		t.Fatal(err)
	}
	serve := func(h http.Handler, config *tls.Config) *httptest.Server {
		server := httptest.NewUnstartedServer(h)
		server.TLS = config
		server.StartTLS()
		t.Cleanup(server.Close)
		return server
	}
	client := func(cert tls.Certificate) *http.Client {
		transport := &http.Transport{TLSClientConfig: guest.ClientTLS(cert, authority, "127.0.0.1")}
		t.Cleanup(transport.CloseIdleConnections)
		return &http.Client{Transport: transport, Timeout: 5 * time.Second}
	}
	enrollServer := serve(web.GuestEnrollmentHandler(compatibility, enrollment.Enroll, clock), guest.EnrollmentTLS(serverCert))
	enroll := func(key *ecdsa.PrivateKey, want int, code string) []byte {
		t.Helper()
		csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, key)
		if err != nil {
			t.Fatal(err)
		}
		raw := postGuestContract(t, client(tls.Certificate{}), enrollServer.URL+"/internal/v1/enroll",
			map[string]any{"grant": token, "protocolVersion": "1", "guestRelease": "current",
				"csrPem": string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csr}))}, want, code)
		if want != http.StatusOK {
			return nil
		}
		var response struct{ CertificatePEM string }
		if err := json.Unmarshal(raw, &response); err != nil {
			t.Fatal(err)
		}
		block, _ := pem.Decode([]byte(response.CertificatePEM))
		if block == nil {
			t.Fatal("enrollment did not return a PEM certificate")
		}
		return block.Bytes
	}
	key := guestContractKey(t)
	leaf := enroll(key, http.StatusOK, "")
	if retry := enroll(key, http.StatusOK, ""); !bytes.Equal(retry, leaf) {
		t.Fatal("same-key retry issued a different certificate")
	}
	enroll(guestContractKey(t), http.StatusForbidden, "guest.enrollment_denied")

	pollServer := serve(web.GuestPollHandler(compatibility, enrollment.Exchange, clock, ""), guest.ServerTLS(serverCert, authority))
	pollURL := pollServer.URL + "/internal/v1/guest/poll"
	unauthenticated, err := client(tls.Certificate{}).Post(pollURL, "application/json", strings.NewReader("{}"))
	if err == nil {
		unauthenticated.Body.Close()
		t.Fatalf("poll without a client certificate reached HTTP status %d, want TLS refusal", unauthenticated.StatusCode)
	}
	registered := client(tls.Certificate{Certificate: [][]byte{leaf}, PrivateKey: key})
	poll := func(release string, want int, code string) []byte {
		t.Helper()
		return postGuestContract(t, registered, pollURL, map[string]any{"protocolVersion": "1", "guestRelease": release}, want, code)
	}
	poll("previous", http.StatusOK, "")
	poll("older", http.StatusConflict, "guest.release_unsupported")
	if _, err := db.ExecContext(ctx, `UPDATE workspaces SET observed_state='running' WHERE id=$1`, workspaceID); err != nil {
		t.Fatal(err)
	}
	for _, index := range []int{0, 1, 2} {
		if err := journal.StartStep(ctx, claim, index); err != nil {
			t.Fatal(err)
		}
		if index == 1 {
			// Release the first authenticated guest from the enrollment handoff promptly.
			var handoff struct {
				NextPollAfterSeconds int
				Operation            *guest.PollOperation
			}
			raw := poll("current", http.StatusOK, "")
			if err := json.Unmarshal(raw, &handoff); err != nil || handoff.NextPollAfterSeconds != 1 || handoff.Operation != nil {
				t.Errorf("enrollment handoff poll = %s (%v), want a one-second retry without a dispatch", raw, err)
			}
			// A stalled admission must fall back to the idle interval.
			clockOffset.Store(int64(91 * time.Second))
			raw = poll("current", http.StatusOK, "")
			clockOffset.Store(0)
			if err := json.Unmarshal(raw, &handoff); err != nil || handoff.NextPollAfterSeconds != 30 || handoff.Operation != nil {
				t.Errorf("stalled enrollment handoff = %s (%v), want idle cadence", raw, err)
			}
		}
		if index < 2 {
			if err := journal.CompleteStep(ctx, claim, index, "fixture provider evidence"); err != nil {
				t.Fatal(err)
			}
		}
	}
	type pollView struct {
		WorkspaceGeneration  int64
		NextPollAfterSeconds int
		Operation            *guest.PollOperation
	}
	var first pollView
	response := poll("current", http.StatusOK, "")
	if err := json.Unmarshal(response, &first); err != nil || first.WorkspaceGeneration != 1 || first.NextPollAfterSeconds != 30 ||
		first.Operation == nil || first.Operation.ID != op.ID || first.Operation.Command != "ensure-development-container" {
		t.Fatalf("registered poll returned %s (%v), want generation 1 and the workspace launch dispatch", response, err)
	}
	var retried pollView
	if err := json.Unmarshal(poll("current", http.StatusOK, ""), &retried); err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(first, retried); diff != "" {
		t.Fatalf("replayed poll changed the dispatch (-want +got):\n%s", diff)
	}
	report := map[string]any{
		"operationId": op.ID, "workspaceGeneration": 1, "status": "running", "step": "devcontainer.wait_for", "attempt": 1,
		"evidence": map[string]any{"containerId": strings.Repeat("a", 64), "waitForReached": true},
	}
	input := map[string]any{
		"protocolVersion": "1", "guestRelease": "current", "lastOperationId": op.ID,
		"lastOperationVersion": first.Operation.Version - 1, "operationReport": report,
	}
	postGuestContract(t, registered, pollURL, input, http.StatusConflict, "operation.conflict")
	input["lastOperationVersion"] = first.Operation.Version
	report["workspaceGeneration"] = 2
	postGuestContract(t, registered, pollURL, input, http.StatusConflict, "operation.conflict")
	report["workspaceGeneration"] = 1
	// Reject an invalid evidence value and oversized payload before they can
	// advance the operation. The same dispatch must still accept its real evidence.
	evidence := report["evidence"].(map[string]any)
	evidence["containerId"] = strings.Repeat("a", 65)
	postGuestContract(t, registered, pollURL, input, http.StatusConflict, "operation.conflict")
	evidence["containerId"] = strings.Repeat("a", 17<<10)
	postGuestContract(t, registered, pollURL, input, http.StatusRequestEntityTooLarge, "request.too_large")
	evidence["containerId"] = strings.Repeat("a", 64)
	for range 2 {
		postGuestContract(t, registered, pollURL, input, http.StatusOK, "")
	}
	if err := journal.CompleteStep(ctx, claim, 2, "connected at waitFor"); err != nil {
		t.Fatal(err)
	}
	if err := journal.StartStep(ctx, claim, 3); err != nil {
		t.Fatal(err)
	}
	probeUnavailable := errors.New("fixture IDE unavailable")
	probeErr := error(probeUnavailable)
	executor := workspace.NewLaunchExecutor(workspace.NewStore(db), nil, nil, "", "", "", clock,
		func(_ context.Context, id string, generation int64) error {
			if id != workspaceID || generation != 1 {
				t.Errorf("IDE probe got %s/%d, want the registered workspace generation", id, generation)
			}
			return probeErr
		})
	ide := operation.Step{Key: "verify_ide"}
	if _, err := executor.Apply(ctx, op, ide); !errors.Is(err, probeUnavailable) {
		t.Fatalf("IDE verification after waitFor = %v, want failed independent proxy probe", err)
	}
	probeErr = nil
	if evidence, err := executor.Apply(ctx, op, ide); err != nil || evidence == "" {
		t.Fatalf("IDE verification after proxy success = %q, %v, want verified evidence", evidence, err)
	}
	report["status"], report["step"] = "succeeded", "devcontainer.lifecycle"
	for range 2 {
		var done pollView
		raw := postGuestContract(t, registered, pollURL, input, http.StatusOK, "")
		if err := json.Unmarshal(raw, &done); err != nil || done.Operation != nil || done.NextPollAfterSeconds != 30 {
			t.Fatalf("completed guest operation poll returned %+v (%v), want idle cadence and no redispatch", done, err)
		}
	}
	report["status"], report["step"] = "running", "devcontainer.wait_for"
	postGuestContract(t, registered, pollURL, input, http.StatusConflict, "operation.conflict")

	if _, err := db.ExecContext(ctx, `UPDATE workspaces SET generation=2 WHERE id=$1`, workspaceID); err != nil {
		t.Fatal(err)
	}
	poll("current", http.StatusForbidden, "guest.identity_denied")
}

func guestContractKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func postGuestContract(t *testing.T, client *http.Client, url string, body map[string]any, want int, code string) []byte {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	var problem web.Problem
	if resp.StatusCode != want || (code != "" && (json.Unmarshal(raw, &problem) != nil || problem.Code != code)) {
		t.Fatalf("POST %s returned %d/%s, want %d/%s", req.URL.Path, resp.StatusCode, problem.Code, want, code)
	}
	return raw
}
