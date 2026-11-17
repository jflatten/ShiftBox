package proxy

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"strings"
	"testing"
)

// TestInstrumentWorkbench adds the input listener to the IDE entry document,
// preserving CSP and avoiding cached, stale lengths or compressed corruption.
func TestInstrumentWorkbench(t *testing.T) {
	for _, compressed := range []bool{false, true} {
		body := []byte(`<!doctype html><html><head><title>K workspace</title><meta http-equiv="Content-Security-Policy" content="script-src 'self'"></head><body>IDE</body></html>`)
		var encoded bytes.Buffer
		if compressed {
			writer := gzip.NewWriter(&encoded)
			if _, err := writer.Write(body); err != nil {
				t.Fatal(err)
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
		} else {
			encoded.Write(body)
		}
		response := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/html; charset=utf-8"}, "Etag": {"old"}},
			Body: io.NopCloser(&encoded), ContentLength: int64(encoded.Len())}
		if compressed {
			response.Header.Set("Content-Encoding", "gzip")
		}
		if err := instrumentWorkbench(response); err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(response.Body)
		if err != nil || !strings.Contains(string(got), `<script defer src="/_shiftbox/activity.js"></script></head>`) ||
			!strings.Contains(string(got), "script-src 'self'") || response.ContentLength != int64(len(got)) || response.Header.Get("Content-Encoding") != "" || response.Header.Get("ETag") != "" {
			t.Errorf("instrumentWorkbench(gzip=%t) = %s, length %d, headers %v (%v), want inserted listener and valid framing", compressed, got, response.ContentLength, response.Header, err)
		}
	}
}

// TestInstrumentWorkbenchLeavesOtherResponsesAlone avoids changing JSON,
// redirects and websocket upgrade responses on the IDE origin.
func TestInstrumentWorkbenchLeavesOtherResponsesAlone(t *testing.T) {
	for _, status := range []int{101, 302, 404} {
		response := &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"text/html"}}, Body: io.NopCloser(strings.NewReader("unchanged"))}
		if err := instrumentWorkbench(response); err != nil {
			t.Fatal(err)
		}
		got, _ := io.ReadAll(response.Body)
		if string(got) != "unchanged" {
			t.Errorf("status %d body=%s, want unchanged", status, got)
		}
	}
}
