package guest

import (
	"slices"
	"strings"
	"testing"
	"time"
)

// TestDevContainerCLITimings measures only known Docker stop events and
// discards project commands, streamed logs, malformed and oversized lines.
func TestDevContainerCLITimings(t *testing.T) {
	type sample struct {
		name    string
		elapsed time.Duration
	}
	var got []sample
	writer := devContainerCLITimings{dockerPath: "/usr/bin/docker", record: func(name string, elapsed time.Duration) {
		got = append(got, sample{name, elapsed})
	}}
	for _, chunk := range []string{
		`{"type":"start","timestamp":1,"text":"Run: /usr/bin/docker pull private.example/test"}` + "\n",
		`{"type":"stop","timestamp":19,"startTimestamp":1,"text":"Run: /usr/bin/docker pull private.ex`,
		`ample/test"}` + "\n",
		strings.Repeat("x", 9000) + `{"type":"stop","timestamp":21,"startTimestamp":1,"text":"Run: /usr/bin/docker run private"}` + "\n",
		`{"type":"raw","timestamp":21,"startTimestamp":1,"text":"Run: /usr/bin/docker run private"}` + "\n",
		`{"type":"stop","timestamp":25,"startTimestamp":20,"text":"Run: /usr/bin/docker buildx build --secret private"}` + "\n",
		`{"type":"stop","timestamp":28,"startTimestamp":25,"text":"Run: /usr/bin/docker run --mount private"}` + "\n",
		`{"type":"stop","timestamp":29,"startTimestamp":26,"text":"Run: /usr/bin/docker inspect private"}` + "\n",
		`{"type":"stop","timestamp":48,"startTimestamp":40,"text":"Starting container"}` + "\n",
	} {
		if n, err := writer.Write([]byte(chunk)); n != len(chunk) || err != nil {
			t.Fatalf("timing writer consumed %d bytes, %v, want %d bytes and no error", n, err, len(chunk))
		}
	}
	want := []sample{{"docker_image_pull", 18 * time.Millisecond}, {"docker_image_build", 5 * time.Millisecond}, {"docker_container_run", 3 * time.Millisecond}, {"docker_container_ready", 8 * time.Millisecond}}
	if !slices.Equal(got, want) {
		t.Errorf("parsed Docker timings = %v, want %v", got, want)
	}
}

// TestDevContainerCLITimingsWithLauncher keeps Docker timings when up uses
// the private bundle-mount launcher and excludes a different command path.
func TestDevContainerCLITimingsWithLauncher(t *testing.T) {
	var got []string
	writer := devContainerCLITimings{
		dockerPath: "/var/lib/shiftbox/.devcontainer-docker-1",
		record:     func(phase string, _ time.Duration) { got = append(got, phase) },
	}
	for _, event := range []string{
		`{"type":"stop","timestamp":25,"startTimestamp":20,"text":"Run: /usr/bin/docker buildx build ."}`,
		`{"type":"stop","timestamp":25,"startTimestamp":20,"text":"Run: /var/lib/shiftbox/.devcontainer-docker-1 buildx build ."}`,
	} {
		writer.recordLine([]byte(event))
	}
	if !slices.Equal(got, []string{"docker_image_build"}) {
		t.Errorf("Docker timing with launcher = %v, want one docker_image_build event", got)
	}
}
