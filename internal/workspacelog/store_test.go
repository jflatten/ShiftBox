package workspacelog

import (
	"fmt"
	"testing"
)

// A retried upload must not duplicate output, leak across workspaces, or prevent
// a browser from resuming after the last output it received.
func TestStoreReplaysWorkspaceOutputAfterCursor(t *testing.T) {
	var store Store
	store.Append("one", "batch-1", "building image")
	store.Append("two", "batch-1", "another workspace")
	first := store.After("one", 0)
	if len(first) != 1 || first[0].Text != "building image" {
		t.Fatalf("initial history = %+v", first)
	}
	store.Append("one", "batch-1", "building image")
	store.Append("one", "batch-2", "image built")
	next := store.After("one", first[0].ID)
	if len(next) != 1 || next[0].Text != "image built" {
		t.Fatalf("resumed history = %+v", next)
	}
}

// A verbose build must keep recent output available without unbounded growth.
func TestStoreKeepsTheMostRecentBatches(t *testing.T) {
	var store Store
	for n := range historyLimit + 1 {
		store.Append("one", fmt.Sprint(n), fmt.Sprint(n))
	}
	history := store.After("one", 0)
	if len(history) != historyLimit || history[0].Text != "1" ||
		history[len(history)-1].Text != fmt.Sprint(historyLimit) {
		t.Fatalf("unexpected history length or boundaries: %d", len(history))
	}
}
