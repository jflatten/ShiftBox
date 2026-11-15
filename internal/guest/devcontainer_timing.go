package guest

import (
	"bytes"
	"encoding/json"
	"strings"
	"time"
)

func (d devContainer) measurePhase(phase string, started time.Time, err error) {
	if d.timing == nil {
		return
	}
	outcome := "succeeded"
	if err != nil {
		outcome = "failed"
	}
	d.timing(phase, time.Since(started), outcome)
}

// devContainerCLITimings reads the pinned CLI's JSON command stop events.
// Command arguments and project output never become timing fields.
type devContainerCLITimings struct {
	record  func(string, time.Duration)
	line    []byte
	discard bool
}

func (t *devContainerCLITimings) Write(data []byte) (int, error) {
	const maxLine = 8 << 10
	total := len(data)
	for len(data) > 0 {
		part, rest, complete := bytes.Cut(data, []byte{'\n'})
		if !t.discard {
			if len(part) <= maxLine-len(t.line) {
				t.line = append(t.line, part...)
			} else {
				t.discard = true
			}
		}
		if complete {
			if !t.discard {
				t.recordLine(t.line)
			}
			t.line = t.line[:0]
			t.discard = false
		}
		data = rest
	}
	return total, nil
}

func (t *devContainerCLITimings) recordLine(line []byte) {
	var event struct {
		Type           string `json:"type"`
		Text           string `json:"text"`
		Timestamp      int64  `json:"timestamp"`
		StartTimestamp int64  `json:"startTimestamp"`
	}
	if json.Unmarshal(line, &event) != nil || event.Type != "stop" ||
		event.StartTimestamp <= 0 || event.Timestamp < event.StartTimestamp ||
		event.Timestamp-event.StartTimestamp > (24*time.Hour).Milliseconds() {
		return
	}
	elapsed := time.Duration(event.Timestamp-event.StartTimestamp) * time.Millisecond
	// The CLI attaches to docker run for the container lifetime. This
	// separate event ends when the new container is ready.
	if event.Text == "Starting container" {
		t.record("docker_container_ready", elapsed)
		return
	}
	command, ok := strings.CutPrefix(event.Text, "Run: /usr/bin/docker ")
	if !ok {
		return
	}
	verb, arguments, _ := strings.Cut(command, " ")
	var phase string
	switch verb {
	case "pull":
		phase = "docker_image_pull"
	case "build":
		phase = "docker_image_build"
	case "buildx":
		subcommand, _, _ := strings.Cut(arguments, " ")
		if subcommand == "build" {
			phase = "docker_image_build"
		}
	case "run":
		phase = "docker_container_run"
	case "create":
		phase = "docker_container_create"
	case "start":
		phase = "docker_container_start"
	}
	if phase != "" {
		t.record(phase, elapsed)
	}
}
