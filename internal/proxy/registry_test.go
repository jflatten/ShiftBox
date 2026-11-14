package proxy

import "testing"

// TestIDEConnectionPresence counts browser IDE requests without including
// guest tunnels or previews, and clears presence when the request closes.
func TestIDEConnectionPresence(t *testing.T) {
	var registry Registry
	register := func(scope Scope) func() {
		t.Helper()
		unregister, err := registry.Register(scope, func() error { return nil }, func() error { return nil })
		if err != nil {
			t.Fatal(err)
		}
		return unregister
	}
	defer register(Scope{WorkspaceID: "target"})()
	defer register(Scope{WorkspaceID: "another", IDE: true})()
	if registry.HasIDEConnection("target") {
		t.Fatal("non-IDE connection counted as browser activity")
	}
	unregister := register(Scope{WorkspaceID: "target", SessionID: "browser", IDE: true})
	if !registry.HasIDEConnection("target") {
		t.Fatal("open IDE connection did not count as activity")
	}
	unregister()
	if registry.HasIDEConnection("target") {
		t.Fatal("closed IDE connection still counted as activity")
	}
}
