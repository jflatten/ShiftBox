package guest

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jflatten/ShiftBox/internal/workspacelog"
)

// CLI output can be split across writes and can include build lines with ANSI,
// structured command arguments, trace-level environment values, and a final plain error without a newline.
func TestDevContainerLogsCaptureBuildOutput(t *testing.T) {
	var output []string
	logs := devContainerLogs{record: func(text string) { output = append(output, text) }}
	for _, chunk := range []string{
		`{"type":"start","text":"Run: docker --env TOKEN=private"}` + "\n",
		`{"type":"text","level":1,"text":"userEnvProbe parsed: {\"ACCESS_TOKEN\":\"private\"}"}` + "\n",
		`{"type":"raw","level":1,"text":"ACCESS_TOKEN=private"}` + "\n",
		`{"type":"raw","level":3,"text":"#3 BU`,
		`ILD\u001b[32m complete\u001b[0m\r\n"}` + "\n",
		`{"type":"progress","name":"internal"}` + "\n",
		"failed to communicate with Docker",
	} {
		if _, err := io.WriteString(&logs, chunk); err != nil {
			t.Fatal(err)
		}
	}
	logs.flush()
	want := []string{"#3 BUILD complete\n", "failed to communicate with Docker\n"}
	if !reflect.DeepEqual(output, want) {
		t.Fatalf("output = %q, want %q", output, want)
	}
}

// A slow or unreachable collector must not prevent a command from writing,
// and an older queued batch must still be sent on the normal flush interval.
func TestWorkspaceLogQueueUploadsBatches(t *testing.T) {
	reports := make(chan workspaceLogReport, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var report workspaceLogReport
		if err := json.NewDecoder(r.Body).Decode(&report); err != nil {
			t.Error(err)
		}
		select {
		case reports <- report:
		default:
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	defer func() { cancel(); <-done }()
	queue := make(chan workspaceLogOutput, 2)
	for range 5 {
		queueWorkspaceLog(queue, ensureDispatch(), "Build step\n")
	}
	go func() {
		defer close(done)
		runWorkspaceLogUploads(ctx, server.Client(), server.URL, queue)
	}()
	select {
	case report := <-reports:
		if report.OperationID != ensureDispatch().ID || report.DispatchVersion != ensureDispatch().Version ||
			report.Text != strings.Repeat("Build step\n", 2) || report.BatchID == "" || report.Format != workspacelog.Format || len(report.Text) > workspacelog.BatchLimit {
			t.Fatalf("unexpected report: %+v", report)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("workspace output was not uploaded")
	}
}
