package guest

import (
	"strings"
	"testing"
	"time"
)

// TestCPUActivityWindow checks aggregate vCPU accounting across a full
// window, excluding guest counters already included in user time.
func TestCPUActivityWindow(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	var sampler cpuSampler
	sample := func(line string, elapsed time.Duration) (float64, bool) {
		t.Helper()
		counters, err := readCPUCounters(strings.NewReader(line))
		if err != nil {
			t.Fatal(err)
		}
		value := sampler.observe(now.Add(elapsed), counters)
		if value == nil {
			return 0, false
		}
		return value.Percent, true
	}
	if _, ok := sample("cpu  100 10 20 800 20 10 10 30 15 5\n", 0); ok {
		t.Fatal("an incomplete sample was accepted")
	}
	if _, ok := sample("cpu  120 10 20 970 30 10 10 30 21 5\n", time.Minute); ok {
		t.Fatal("a one-minute sample was accepted")
	}
	if percent, ok := sample("cpu  140 10 20 1140 40 10 10 30 27 5\n", 2*time.Minute); !ok || percent != 10 {
		t.Errorf("CPU activity = %v (%t), want 10%% over two minutes", percent, ok)
	}
}
