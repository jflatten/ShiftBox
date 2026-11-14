package guest

import (
	"bufio"
	"errors"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jflatten/ShiftBox/internal/workspace"
)

type cpuCounters struct{ busy, total uint64 }

type timedCPUCounters struct {
	at time.Time
	cpuCounters
}

type cpuSampler struct{ samples []timedCPUCounters }

func (s *cpuSampler) sample(now time.Time) *workspace.CPUActivity {
	file, err := os.Open("/proc/stat")
	if err != nil {
		return nil
	}
	defer file.Close()
	counters, err := readCPUCounters(file)
	if err != nil {
		return nil
	}
	return s.observe(now, counters)
}

func readCPUCounters(source io.Reader) (cpuCounters, error) {
	reader := bufio.NewScanner(source)
	if !reader.Scan() {
		return cpuCounters{}, errors.New("CPU counters are unavailable")
	}
	fields := strings.Fields(reader.Text())
	if len(fields) < 9 || fields[0] != "cpu" {
		return cpuCounters{}, errors.New("invalid aggregate CPU counters")
	}
	var totals cpuCounters
	// Linux guest and guest_nice duplicate time already counted in user and nice.
	for i, field := range fields[1:9] {
		value, err := strconv.ParseUint(field, 10, 64)
		if err != nil {
			return cpuCounters{}, err
		}
		totals.total += value
		if i != 3 && i != 4 && i != 7 {
			totals.busy += value
		}
	}
	return totals, nil
}

func (s *cpuSampler) observe(now time.Time, counters cpuCounters) *workspace.CPUActivity {
	if n := len(s.samples); n > 0 {
		last := s.samples[n-1]
		if !now.After(last.at) || counters.busy < last.busy || counters.total < last.total || now.Sub(last.at) > 90*time.Second {
			s.samples = s.samples[:0]
		}
	}
	s.samples = append(s.samples, timedCPUCounters{at: now, cpuCounters: counters})
	const window = 2 * time.Minute
	for len(s.samples) > 1 && now.Sub(s.samples[1].at) >= window {
		s.samples = s.samples[1:]
	}
	base := s.samples[0]
	if now.Sub(base.at) < window || counters.total == base.total {
		return nil
	}
	percent := 100 * float64(counters.busy-base.busy) / float64(counters.total-base.total)
	return &workspace.CPUActivity{SampledAt: now.UTC(), Percent: percent}
}
