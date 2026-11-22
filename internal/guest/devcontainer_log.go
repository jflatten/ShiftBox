package guest

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jflatten/ShiftBox/internal/workspacelog"
)

var terminalEscapes = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(\x07|\x1b\\)`)

// devContainerLogs frames the CLI's JSON stderr events independently of its
// stdout result. Raw Docker output and textual diagnostics reach the console.
type devContainerLogs struct {
	record func(string)
	line   []byte
	skip   bool
}

func (l *devContainerLogs) Write(data []byte) (int, error) {
	const maxRecord = 64 << 10
	written := len(data)
	for len(data) > 0 {
		part, rest, complete := bytes.Cut(data, []byte{'\n'})
		if len(part) > maxRecord-len(l.line) {
			l.skip = true
		}
		if !l.skip {
			l.line = append(l.line, part...)
		}
		if complete {
			l.flush()
		}
		data = rest
	}
	return written, nil
}

func (l *devContainerLogs) flush() {
	defer func() {
		l.line = l.line[:0]
		l.skip = false
	}()
	if l.skip {
		l.record("[Oversized Dev Container output omitted]\n")
		return
	}
	var event struct {
		Type  string `json:"type"`
		Level int    `json:"level"`
		Text  string `json:"text"`
	}
	text := string(l.line)
	if json.Unmarshal(l.line, &event) == nil {
		// The CLI emits trace events in JSON mode even at the default log level.
		// Trace output can include shell environment values. Start and stop
		// events can include full command arguments.
		const debugLevel = 2
		if event.Level < debugLevel || (event.Type != "text" && event.Type != "raw") {
			return
		}
		text = event.Text
	}
	text = terminalEscapes.ReplaceAllString(text, "")
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	text = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, text)
	if strings.TrimSpace(text) == "" {
		return
	}
	const suffix = "… [output truncated]\n"
	if len(text) >= workspacelog.BatchLimit {
		end := workspacelog.BatchLimit - len(suffix)
		for !utf8.RuneStart(text[end]) {
			end--
		}
		l.record(text[:end] + suffix)
		return
	}
	l.record(strings.TrimSuffix(text, "\n") + "\n")
}
