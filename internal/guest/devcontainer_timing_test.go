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
	writer := devContainerCLITimings{record: func(name string, elapsed time.Duration) {
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
