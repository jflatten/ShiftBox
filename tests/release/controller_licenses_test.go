package release_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestControllerImageStagesLinkedNotices protects the release image's license
// handoff after the controller binary has been built.
func TestControllerImageStagesLinkedNotices(t *testing.T) {
	root := filepath.Join("..", "..")
	dockerfile, err := os.ReadFile(filepath.Join(root, "platform", "container", "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"scripts/stage-controller-licenses.sh /release/shiftbox /release/licenses",
		"COPY --from=controller /release/licenses/ /usr/share/licenses/shiftbox/",
	} {
		if !strings.Contains(string(dockerfile), expected) {
			t.Errorf("controller image is missing %q", expected)
		}
	}
}
