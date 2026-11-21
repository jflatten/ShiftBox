package contract_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jflatten/ShiftBox/internal/app"
	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/backup"
	"github.com/jflatten/ShiftBox/internal/config"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/operation"
	"github.com/jflatten/ShiftBox/internal/web"
)

const (
	recoveryOrigin = "https://management.test"
	recoveryPass   = "disposable test archive passphrase"
)

// TestBackupRestoreAPI checks that admissions return ordered, durable steps
// and that private inputs stay out of successful and failed operation views.
func TestBackupRestoreAPI(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	db, metadata := recoveryContractDB(t, filepath.Join(dir, "application.db"))
	journal := operation.NewJournal(db, time.Now, uuid.NewString)
	service := backup.NewService(db, journal, metadata, dir)
	backupJobs := make(chan web.BackupJob, 1)
	restoreJobs := make(chan web.RestoreValidationJob, 2)
	create := web.BackupRoute(db, service, recoveryOrigin, metadata.InstallationID, time.Now, func(job web.BackupJob) { backupJobs <- job })
	validate := web.RestoreValidationRoute(db, journal, recoveryOrigin, metadata.InstallationID, time.Now, func(job web.RestoreValidationJob) { restoreJobs <- job })
	detail, _ := web.OperationHandlers(db, recoveryOrigin, time.Now, journal, nil)
	mux := http.NewServeMux()
	mux.Handle("POST /api/v1/backups", &create)
	mux.Handle("POST /api/v1/restores/validate", &validate)
	mux.Handle("GET /api/v1/operations/{operationId}", &detail)

	token := recoveryContractSession(t, db, "normal")
	archive := filepath.Join(dir, "management.age")
	destination := filepath.Join(dir, "replacement")
	const appPass = "contract-only-app-login"
	const dexPass = "contract-only-dex-login"
	protected := []string{recoveryPass, appPass, dexPass, archive, destination}
	var version string
	request := func(method, path, body, key string, want int) []byte {
		t.Helper()
		r := httptest.NewRequest(method, recoveryOrigin+"/api/v1"+path, strings.NewReader(body))
		r.AddCookie(&http.Cookie{Name: "__Host-shiftbox_session", Value: token})
		r.Header.Set("Origin", recoveryOrigin)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-CSRF-Token", auth.CSRFToken(token))
		r.Header.Set("Idempotency-Key", key)
		r.Header.Set("If-Match", version)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		assertRecoveryPrivate(t, w.Body.String(), protected...)
		if w.Code != want || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s %s = HTTP %d with cache %q, want HTTP %d and no-store", method, path, w.Code, w.Header().Get("Cache-Control"), want)
		}
		return w.Body.Bytes()
	}
	readOperation := func(raw []byte, kind, state string, steps ...string) operation.Operation {
		t.Helper()
		var op operation.Operation
		if err := json.Unmarshal(raw, &op); err != nil || !web.ValidUUID(op.ID) || op.Type != kind || op.Status != state {
			t.Fatalf("operation response = type %q state %q, UUID valid %t (%v), want %s %s with UUID", op.Type, op.Status, web.ValidUUID(op.ID), err, kind, state)
		}
		var got []string
		for i, step := range op.Steps {
			if step.Index != i || step.Label == "" {
				t.Errorf("operation step %q has index %d and label %q, want index %d and a label", step.Key, step.Index, step.Label, i)
			}
			got = append(got, step.Key+"/"+step.Status)
		}
		if strings.Join(got, ",") != strings.Join(steps, ",") {
			t.Fatalf("%s operation steps = %v, want %v", kind, got, steps)
		}
		return op
	}
	backupBody, err := json.Marshal(map[string]string{"password": recoveryPass, "destination": archive})
	if err != nil {
		t.Fatal(err)
	}
	invalidBackup := request("POST", "/backups", `{"password":"short","destination":"/abs/export.age"}`, "backup-invalid-case", 400)
	var problem web.Problem
	if err := json.Unmarshal(invalidBackup, &problem); err != nil || problem.Code != "backup.invalid_input" || problem.Status != 400 {
		t.Fatalf("invalid backup problem = %q/%d (%v), want backup.invalid_input/400", problem.Code, problem.Status, err)
	}
	admitted := readOperation(request("POST", "/backups", string(backupBody), "backup-contract-case", 202),
		"backup", "pending", "checkpoint/pending", "encrypt/pending")
	close(backupJobs)
	if err := web.RunBackupJobs(ctx, backupJobs, service, func(context.Context, string) (map[string]string, error) {
		return nil, errors.New(recoveryPass)
	}); err != nil {
		t.Fatal(err)
	}
	readOperation(request("GET", "/operations/"+admitted.ID, "", "", 200), "backup", "failed", "checkpoint/failed", "encrypt/pending")

	validationBody, err := json.Marshal(map[string]any{
		"archivePath": archive, "destination": destination, "password": recoveryPass,
		"replacementPostgres": map[string]string{
			"profile": "postgres", "tlsMode": "verify-full",
			"connectionUri":    "postgres://app:" + appPass + "@db.test/replacement?sslmode=verify-full",
			"dexConnectionUri": "postgres://dex:" + dexPass + "@db.test/replacement?sslmode=verify-full",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	missingPass := request("POST", "/restores/validate", `{"archivePath":"/abs/export.age"}`, "restore-invalid-case", 400)
	if err := json.Unmarshal(missingPass, &problem); err != nil || problem.Code != "restore.invalid_input" || problem.Status != 400 {
		t.Fatalf("invalid restore problem = %q/%d (%v), want restore.invalid_input/400", problem.Code, problem.Status, err)
	}
	failed := readOperation(request("POST", "/restores/validate", string(validationBody), "restore-failed-case", 202),
		"restore", "pending", "validate/pending")
	// Run one controller job at a time so the second request can be admitted
	// after the first has durably failed.
	runValidation := func(result string, failure error) {
		t.Helper()
		jobs := make(chan web.RestoreValidationJob, 1)
		select {
		case job := <-restoreJobs:
			jobs <- job
		default:
			t.Fatal("restore admission did not hand off the validation job")
		}
		close(jobs)
		if err := web.RunRestoreValidations(ctx, db, journal, jobs, func(context.Context, web.RestoreValidationJob) (string, error) {
			return result, failure
		}); err != nil {
			t.Fatal(err)
		}
	}
	runValidation("", errors.New(strings.Join(protected, " ")))
	readOperation(request("GET", "/operations/"+failed.ID, "", "", 200), "restore", "failed", "validate/failed")
	accepted := readOperation(request("POST", "/restores/validate", string(validationBody), "restore-success-case", 202),
		"restore", "pending", "validate/pending")
	validationID := uuid.NewString()
	runValidation(validationID, nil)
	result := readOperation(request("GET", "/operations/"+accepted.ID, "", "", 200), "restore", "succeeded", "validate/succeeded")
	if result.Steps[0].Detail != validationID || result.ID == validationID {
		t.Error("successful validation must expose its validation ID separately from the operation ID")
	}
	for _, id := range []string{admitted.ID, failed.ID, accepted.ID} {
		events, err := journal.Events(ctx, id, 0)
		if err != nil || len(events) == 0 {
			t.Fatalf("operation %s evidence = %d events (%v), want durable events", id, len(events), err)
		}
		data, err := json.Marshal(events)
		if err != nil {
			t.Fatal(err)
		}
		assertRecoveryPrivate(t, string(data), protected...)
	}
	var state string
	var reviews int
	if err := db.QueryRowContext(ctx, "SELECT state, (SELECT count(*) FROM recovery_reviews) FROM installations").Scan(&state, &reviews); err != nil || state != "active" || reviews != 0 {
		t.Fatalf("validation left installation state %q and %d reviews (%v), want active with zero reviews", state, reviews, err)
	}

	// In a disposable replacement, only a completed review can authorize a
	// separate activation operation and sign out the recovery administrator.
	reviewID, err := backup.EnterRecovery(ctx, db, journal, metadata.InstallationID, validationID, "local-operator", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	token = recoveryContractSession(t, db, "recovery_review")
	for _, handler := range web.RecoveryRoutes(db, journal, recoveryOrigin, reviewID, time.Now,
		func(context.Context) ([]backup.RecoveryWorkspace, error) { return nil, nil }, nil) {
		mux.Handle(handler.Route.Method+" /api/v1"+handler.Route.Path, handler)
	}
	reviewPath := "/recovery/" + reviewID
	request("POST", reviewPath+"/activate", "", "activate-contract-case", 403)
	version = `"1"`
	request("PATCH", reviewPath, `{"section":"identities","decisions":{"userIds":["admin"]}}`, "", 200)
	version = `"2"`
	request("PATCH", reviewPath, `{"section":"provider","decisions":{}}`, "", 200)
	version = `"3"`
	request("PATCH", reviewPath, `{"section":"authorization_retention","decisions":{"acceptRetention":true}}`, "", 200)
	if err := db.QueryRowContext(ctx, "SELECT state FROM installations").Scan(&state); err != nil || state != "recovery" {
		t.Fatalf("installation after review = %q (%v), want recovery until explicit activation", state, err)
	}
	readOperation(request("POST", reviewPath+"/activate", "", "activate-contract-case", 202), "restore", "succeeded", "activate/succeeded")
	request("GET", reviewPath, "", "", 401)
	if err := db.QueryRowContext(ctx, "SELECT state FROM installations").Scan(&state); err != nil || state != "active" {
		t.Fatalf("installation after activation = %q (%v), want active", state, err)
	}
}

// TestBackupRestoreCLI checks the process exit and JSON contracts with fixture
// passphrases on stdin, then verifies restore validation and activation are
// distinct invocations with distinct effects.
func TestBackupRestoreCLI(t *testing.T) {
	dir, err := os.MkdirTemp("", "sb-cli-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	dbPath := filepath.Join(dir, "application.db")
	db, metadata := recoveryContractDB(t, dbPath)
	configPath := filepath.Join(dir, "configuration.json")
	cfg := config.Config{InstallationDir: dir, DataProfile: "sqlite", SQLitePath: dbPath,
		ManagementOrigin: recoveryOrigin, ExternalAuthIntervalSeconds: 14400}
	configData, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, configData, 0600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(dir, "shiftbox")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", binary, "../../cmd/shiftbox")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build shiftbox CLI: %v\n%s", err, out)
	}
	archive := filepath.Join(dir, "management.age")
	destination := filepath.Join(dir, "replacement")
	type cliResult struct {
		ValidationID     string `json:"validationId"`
		RecoveryReviewID string `json:"recoveryReviewId"`
		OperationID      string `json:"operationId"`
		Archive          string `json:"archive"`
		ManifestDigest   string `json:"manifestDigest"`
	}
	run := func(args []string, input, command string, wantExit, status int, code string) *cliResult {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, binary, args...)
		cmd.Stdin = strings.NewReader(input)
		var diagnostics bytes.Buffer
		cmd.Stderr = &diagnostics
		out, runErr := cmd.Output()
		gotExit := 0
		if runErr != nil {
			processExit, ok := errors.AsType[*exec.ExitError](runErr)
			if !ok {
				t.Fatalf("%s did not return an exit code: %v", command, runErr)
			}
			gotExit = processExit.ExitCode()
		}
		assertRecoveryPrivate(t, string(out)+diagnostics.String(), recoveryPass, "different fixture passphrase")
		if wantExit != 0 {
			assertRecoveryPrivate(t, string(out)+diagnostics.String(), archive)
		}
		if gotExit != wantExit {
			t.Fatalf("%s exit = %d, want %d", command, gotExit, wantExit)
		}
		var result struct {
			APIVersion string
			Command    string
			Status     string
			Problem    *web.Problem
			Result     *cliResult
		}
		if err := json.Unmarshal(out, &result); err != nil || result.APIVersion != "shiftbox.io/cli/v1" || result.Command != command {
			t.Fatalf("%s JSON envelope = version %q, command %q (%v), want shiftbox.io/cli/v1 and matching command", command, result.APIVersion, result.Command, err)
		}
		if wantExit == 0 {
			if result.Status != "succeeded" || result.Result == nil || result.Problem != nil {
				t.Fatalf("%s status = %q, result present %t, problem present %t, want succeeded with result", command, result.Status, result.Result != nil, result.Problem != nil)
			}
		} else if result.Status != "failed" || result.Result != nil || result.Problem == nil || result.Problem.Code != code || result.Problem.Status != status {
			t.Fatalf("%s status = %q, problem = %+v, result present %t, want failed with %s (%d)", command, result.Status, result.Problem, result.Result != nil, code, status)
		}
		return result.Result
	}
	backupArgs := []string{"backup", "--json", "--password-stdin", "--config", configPath, "--output", archive}
	run(backupArgs, recoveryPass+"\n"+recoveryPass+"\n", "backup", 5, 503, "backup.controller_unavailable")
	socket := filepath.Join(dir, "backup.sock")
	t.Run("backup password confirmation", func(t *testing.T) {
		listener, err := net.Listen("unix", socket)
		if errors.Is(err, syscall.EPERM) {
			t.Skip("local Unix socket binding is unavailable")
		}
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { listener.Close() })
		if err := os.Chmod(socket, 0600); err != nil {
			t.Fatal(err)
		}
		run(backupArgs, recoveryPass+"\ndifferent fixture passphrase\n", "backup", 2, 400, "backup.password_input")
		assertRecoveryAbsent(t, archive)

		// Observe the transport contract at the controller's private socket.
		// The archive-writing service is covered by cmd/shiftbox backup tests.
		operationID := uuid.NewString()
		digest := strings.Repeat("a", 64)
		server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var input struct{ Destination, Password string }
			err := json.NewDecoder(io.LimitReader(r.Body, 8192)).Decode(&input)
			if r.Method != http.MethodPost || r.URL.RequestURI() != "/backup" || err != nil ||
				input.Destination != archive || input.Password != recoveryPass {
				t.Error("CLI backup did not send the confirmed passphrase in the private socket request body")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if err := json.NewEncoder(w).Encode(cliResult{OperationID: operationID, Archive: archive, ManifestDigest: digest}); err != nil {
				t.Error("write backup socket response")
			}
		})}
		done := make(chan error, 1)
		go func() { done <- server.Serve(listener) }()
		t.Cleanup(func() {
			if err := server.Close(); err != nil {
				t.Error(err)
			}
			if err := <-done; !errors.Is(err, http.ErrServerClosed) {
				t.Errorf("backup socket closed with %v, want http.ErrServerClosed", err)
			}
		})
		success := run(backupArgs, recoveryPass+"\n"+recoveryPass+"\n", "backup", 0, 0, "")
		if success.OperationID != operationID || success.Archive != archive || success.ManifestDigest != digest {
			t.Error("CLI backup result did not contain the controller's verified archive evidence")
		}
	})

	activate := []string{"restore", "activate", "--json", "--password-stdin", "--config", configPath, "--archive", archive, "--destination", destination}
	run(activate, recoveryPass+"\n", "restore activate", 2, 400, "restore.invalid_input")
	run(append(activate, "--validation-id", uuid.NewString()), recoveryPass+"\n", "restore activate", 3, 404, "restore.validation_missing")
	assertRecoveryAbsent(t, destination)

	t.Run("same-host restore", func(t *testing.T) {
		if runtime.GOOS != "darwin" {
			t.Skip("same-host restore requires the supported controller Mac")
		}
		metadata.HostID, err = app.BackupHostID(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		key := filepath.Join(dir, "installation.key")
		if err := os.WriteFile(key, bytes.Repeat([]byte{0x35}, 32), 0600); err != nil {
			t.Fatal(err)
		}
		snapshot := filepath.Join(dir, "snapshot.db")
		if err := backup.SQLiteCheckpoint(t.Context(), db, snapshot); err != nil {
			t.Fatal(err)
		}
		auxiliary := filepath.Join(dir, "auxiliary")
		if err := os.WriteFile(auxiliary, []byte("disposable auxiliary fixture"), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := backup.CreateArchive(t.Context(), archive, recoveryPass, metadata, map[string]string{
			"application.db": snapshot, "configuration.json": configPath, "installation.key": key,
			"dex.db": auxiliary, "guest-ca.json": auxiliary,
		}); err != nil {
			t.Fatal(err)
		}
		validate := []string{"restore", "validate", "--json", "--password-stdin", "--config", configPath, "--archive", archive,
			"--destination", destination, "--installation-id", metadata.InstallationID, "--release", metadata.Release}
		run(validate, "different fixture passphrase\n", "restore validate", 7, 422, "restore.invalid_archive")
		assertRecoveryAbsent(t, destination)
		valid := run(validate, recoveryPass+"\n", "restore validate", 0, 0, "")
		if !web.ValidUUID(valid.ValidationID) || valid.RecoveryReviewID != "" {
			t.Fatal("restore validate must return a validation ID and create no recovery review")
		}
		assertRecoveryAbsent(t, destination)
		applied := run(append(activate, "--validation-id", valid.ValidationID), recoveryPass+"\n", "restore activate", 0, 0, "")
		if applied.ValidationID != valid.ValidationID || !web.ValidUUID(applied.RecoveryReviewID) || applied.RecoveryReviewID == applied.ValidationID {
			t.Fatal("restore activate must return the original validation ID and a new recovery review ID")
		}
		replacement, err := database.Open(t.Context(), database.Config{Profile: "sqlite", SQLitePath: filepath.Join(destination, "application.db")})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { replacement.Close() })
		var state, reviewState string
		err = replacement.QueryRowContext(t.Context(), `SELECT i.state,r.state FROM installations i CROSS JOIN recovery_reviews r
			WHERE r.id=$1`, applied.RecoveryReviewID).Scan(&state, &reviewState)
		if err != nil || state != "recovery" || reviewState != "identity_review" {
			t.Fatalf("restored installation/review state = %q/%q (%v), want recovery/identity_review", state, reviewState, err)
		}
	})
}

func recoveryContractDB(t *testing.T, path string) (*sql.DB, backup.ArchiveMetadata) {
	t.Helper()
	ctx := t.Context()
	const release = "development"
	db, id, err := database.OpenBootstrap(ctx, path, database.BootstrapSeed{
		ID: uuid.NewString(), Organization: "Contract fixture", Origin: recoveryOrigin, Now: time.Now(),
		IDEPattern: "{workspace}.ide.test", PreviewPattern: "{service}--{workspace}.preview.test", Release: release, MinGuestRelease: release,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.ExecContext(ctx, "UPDATE installations SET state='active'"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO users(id,primary_email,role,membership_state,credential_mode,created_by,created_at,updated_at,version)
		VALUES('admin','admin@example.test','administrator','active','local','setup','now','now',1)`); err != nil {
		t.Fatal(err)
	}
	schema, err := database.CurrentSchemaVersion("sqlite")
	if err != nil {
		t.Fatal(err)
	}
	return db, backup.ArchiveMetadata{InstallationID: id, Release: release, Profile: "sqlite", Schema: schema}
}

func recoveryContractSession(t *testing.T, db *sql.DB, mode string) string {
	t.Helper()
	session, token, err := auth.NewSession(uuid.NewString(), "admin", mode, time.Now().UTC(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Transaction(t.Context(), db, func(tx *sql.Tx) error {
		return auth.NewStore(db).SaveSession(t.Context(), tx, session)
	}); err != nil {
		t.Fatal(err)
	}
	return token
}

func assertRecoveryPrivate(t *testing.T, output string, protected ...string) {
	t.Helper()
	for _, value := range protected {
		if strings.Contains(output, value) {
			t.Fatal("response, diagnostics, or operation evidence contained a protected input")
		}
	}
}

func assertRecoveryAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("command created an archive or replacement before successful activation")
	}
}
