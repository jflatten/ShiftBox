package testsupport

import (
	"testing"
	"time"
)

// TestFixedClock keeps expiry tests independent of wall time and host timezone.
func TestFixedClock(t *testing.T) {
	want := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	now := FixedClock(want)
	for range 2 {
		if got := now(); !got.Equal(want) {
			t.Errorf("FixedClock() = %v, want %v", got, want)
		}
	}
}

// TestID keeps distinct fixture records from accidentally sharing an identity.
func TestID(t *testing.T) {
	if got, want := ID(1), "00000000-0000-4000-8000-000000000001"; got != want {
		t.Errorf("ID(1) = %q, want %q", got, want)
	}
	if ID(1) == ID(2) {
		t.Error("ID(1) = ID(2), want distinct fixture identities")
	}
}
