package workspace

import "time"

// RetentionDeadline derives a deadline from a controller-owned clock.
// An absent duration leaves automatic retention disabled.
func RetentionDeadline(baseline time.Time, seconds *int64) *string {
	if baseline.IsZero() || seconds == nil || *seconds <= 0 || *seconds > int64((1<<63-1)/time.Second) {
		return nil
	}
	deadline := baseline.UTC().Add(time.Duration(*seconds) * time.Second).Format(time.RFC3339Nano)
	return &deadline
}
