// Package testsupport supplies deterministic inputs for ShiftBox tests.
package testsupport

import "time"

// FixedClock returns an immutable clock for expiry and reconciliation fixtures.
func FixedClock(at time.Time) func() time.Time {
	return func() time.Time { return at }
}
