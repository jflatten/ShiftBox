package proxy

import (
	"bytes"
	"compress/gzip"
	_ "embed"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// HumanActivityPath is served on authenticated IDE origins, never previews.
const HumanActivityPath = "/_shiftbox/activity"

// HumanActivityScriptPath identifies the first-party workbench input listener.
const HumanActivityScriptPath = "/_shiftbox/activity.js"

//go:embed human-activity.js
var humanActivityScript string

// HumanActivityScript returns the input listener without any workspace secrets.
func HumanActivityScript() string { return humanActivityScript }

func instrumentWorkbench(response *http.Response) error {
	if response.StatusCode != http.StatusOK || !strings.HasPrefix(strings.ToLower(response.Header.Get("Content-Type")), "text/html") {
		return nil
	}
	reader := io.Reader(response.Body)
	switch response.Header.Get("Content-Encoding") {
	case "", "identity":
	case "gzip":
		decoded, err := gzip.NewReader(reader)
		if err != nil {
			return err
		}
		defer decoded.Close()
		reader = decoded
	default:
		return errors.New("unsupported workbench encoding")
	}
	const maxWorkbenchBytes = 4 << 20
	body, err := io.ReadAll(io.LimitReader(reader, maxWorkbenchBytes+1))
	closeErr := response.Body.Close()
	if err != nil || closeErr != nil {
		return errors.Join(err, closeErr)
	}
	if len(body) > maxWorkbenchBytes {
		return errors.New("workbench document is too large")
	}
	// Fold only HTML's ASCII letters. Unicode case folding can change byte
	// lengths in workspace titles, making an insertion offset corrupt the page.
	lower := bytes.Clone(body)
	for index, value := range lower {
		if value >= 'A' && value <= 'Z' {
			lower[index] = value + ('a' - 'A')
		}
	}
	if at := bytes.Index(lower, []byte("</head>")); at >= 0 {
		tag := []byte(`<script defer src="` + HumanActivityScriptPath + `"></script>`)
		body = append(append(append([]byte(nil), body[:at]...), tag...), body[at:]...)
	}
	response.Body = io.NopCloser(bytes.NewReader(body))
	response.ContentLength = int64(len(body))
	response.TransferEncoding = nil
	for _, name := range []string{"Content-Encoding", "Transfer-Encoding", "ETag", "Last-Modified", "Content-MD5", "Digest"} {
		response.Header.Del(name)
	}
	response.Header.Set("Content-Length", strconv.Itoa(len(body)))
	response.Header.Set("Cache-Control", "no-store")
	return nil
}
