//go:build utm_release

package release_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jflatten/ShiftBox/internal/provider"
	"github.com/jflatten/ShiftBox/internal/provider/utm"
)

// TestUTMGuestClockRecovery checks that a wrong guest clock blocks fresh TLS
// contact and that correcting it recovers the same enrollment and owned disk.
func TestUTMGuestClockRecovery(t *testing.T) {
	if os.Getenv("SHIFTBOX_TEST_UTM_GUEST_CLOCK") != "1" {
		t.Skip("set SHIFTBOX_TEST_UTM_GUEST_CLOCK=1 for native clock correction")
	}
	runUTMGuestFixture(t, nil, nativeGuestClock)
}

func verifyNativeGuestClock(t *testing.T, ctx context.Context, db *sql.DB, machines *utm.Provider,
	before provider.Inventory, clock *nativeClockFixture,
) {
	t.Helper()
	prior, serial := nativeGuestContact(t, ctx, db, before.Ref)
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	case <-timer.C:
	}
	if got, cert := nativeGuestContact(t, ctx, db, before.Ref); !got.Equal(prior) || cert != serial {
		t.Fatalf("wrong guest clock advanced contact from %v to %v or changed identity", prior, got)
	}
	clock.correct(t, ctx)

	deadline, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		got, cert := nativeGuestContact(t, deadline, db, before.Ref)
		if cert != serial {
			t.Fatal("correcting the guest clock changed its enrolled identity")
		}
		if got.After(prior) {
			break
		}
		select {
		case <-deadline.Done():
			t.Fatal("guest did not resume authenticated polling after clock correction")
		case <-ticker.C:
		}
	}
	after, err := machines.Inspect(ctx, before.Ref)
	if err != nil || after.Power != provider.Running || after.Ref != before.Ref {
		t.Fatalf("VM after clock correction = %s/%+v (%v), want the same running guest", after.Power, after.Ref, err)
	}
	verifyOwnedDisk(t, before, after)
	t.Log("correcting the guest clock restored authentication with the original certificate, VM and disk")
}

type nativeClockFixture struct {
	seed       string
	skewClock  chan struct{}
	resetClock chan struct{}
	skewed     chan time.Time
	corrected  chan time.Time
}

// startNativeClockFixture serves a one-use NoCloud seed and observes guest
// clock changes over an isolated test endpoint. It does not serve credentials.
func startNativeClockFixture(t *testing.T, host string) *nativeClockFixture {
	t.Helper()
	listener, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
	if err != nil {
		t.Fatal(err)
	}
	id := uuid.NewString()
	path := "/" + id + "/"
	clock := &nativeClockFixture{
		seed: "http://" + listener.Addr().String() + path, skewClock: make(chan struct{}), resetClock: make(chan struct{}),
		skewed: make(chan time.Time, 1), corrected: make(chan time.Time, 1),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+path+"meta-data", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, "instance-id: %s\n", id)
	})
	mux.HandleFunc("GET "+path+"user-data", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, "#!/bin/sh\nset -eu\nbase='%s'\n%s", clock.seed, nativeClockScript)
	})
	timestamp := func(pathSuffix string, gate <-chan struct{}, offset time.Duration, observation chan<- time.Time) {
		mux.HandleFunc("GET "+path+pathSuffix, func(w http.ResponseWriter, _ *http.Request) {
			select {
			case <-gate:
				fmt.Fprintln(w, time.Now().Add(offset).Unix())
			default:
				w.WriteHeader(http.StatusServiceUnavailable)
			}
		})
		mux.HandleFunc("GET "+path+pathSuffix+"/{seconds}", func(w http.ResponseWriter, r *http.Request) {
			seconds, err := strconv.ParseInt(r.PathValue("seconds"), 10, 64)
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			select {
			case observation <- time.Unix(seconds, 0):
			default:
			}
			w.WriteHeader(http.StatusNoContent)
		})
	}
	timestamp("skew", clock.skewClock, -time.Hour, clock.skewed)
	timestamp("reset", clock.resetClock, 0, clock.corrected)
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 3 * time.Second}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	t.Cleanup(func() {
		_ = server.Close()
		if err := <-done; !errors.Is(err, http.ErrServerClosed) {
			t.Errorf("clock fixture seed server: %v", err)
		}
	})
	return clock
}

func (clock *nativeClockFixture) skew(t *testing.T, ctx context.Context) {
	t.Helper()
	close(clock.skewClock)
	observeGuestClock(t, ctx, clock.skewed, -time.Hour)
}

func (clock *nativeClockFixture) correct(t *testing.T, ctx context.Context) {
	t.Helper()
	close(clock.resetClock)
	observeGuestClock(t, ctx, clock.corrected, 0)
}

func observeGuestClock(t *testing.T, ctx context.Context, report <-chan time.Time, offset time.Duration) {
	t.Helper()
	wait, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	select {
	case stamp := <-report:
		if difference := stamp.Sub(time.Now().Add(offset)); difference.Abs() > 15*time.Second {
			t.Fatalf("guest clock differed from expected offset %v by %v", offset, difference)
		}
		t.Logf("guest reported its clock at the expected offset %v", offset)
	case <-wait.Done():
		t.Fatalf("guest did not report its clock at offset %v: %v", offset, wait.Err())
	}
}

const nativeClockScript = `
wait_for_clock() {
    attempts=0
    until seconds=$(curl -fsS --max-time 3 "$base$1" 2>/dev/null); do
        attempts=$((attempts + 1))
        [ "$attempts" -lt 180 ] || return 1
        sleep 1
    done
    case "$seconds" in ''|*[!0-9]*) return 1;; esac
}
wait_for_clock skew
timedatectl set-ntp false
date -u -s "@$seconds" >/dev/null
curl -fsS --retry 3 --max-time 3 "${base}skew/$(date -u +%s)"
wait_for_clock reset
date -u -s "@$seconds" >/dev/null
curl -fsS --retry 3 --max-time 3 "${base}reset/$(date -u +%s)"
`

type nativeSeedProvider struct {
	provider.Provider
	seed string
}

// Start attaches the fixture seed to the disposable VM only. The provider
// starts the same imported release disk with its normal enrollment media.
func (p nativeSeedProvider) Start(ctx context.Context, request provider.StartRequest) (provider.Inventory, error) {
	const script = `on run argv
    tell application "/Applications/UTM.app"
        if UTM version is not "5.0.8" then error "unexpected UTM version"
        set vm to virtual machine id (item 1 of argv)
        if status of vm is stopped then
            set cfg to configuration of vm
            set cfgArgs to qemu additional arguments of cfg
            set discovery to "type=1,serial=ds=nocloud-net;s=" & (item 2 of argv)
            set seen to false
            repeat with entry in cfgArgs
                if argument string of entry is discovery then set seen to true
            end repeat
            if not seen then update configuration vm with {qemu additional arguments:cfgArgs & {{argument string:"-smbios"}, {argument string:discovery}}}
        end if
        return "ready"
    end tell
end run`
	output, err := exec.CommandContext(ctx, "/usr/bin/osascript", "-e", script, "--", request.Ref.InstanceID, p.seed).CombinedOutput()
	if err != nil || strings.TrimSpace(string(output)) != "ready" {
		return provider.Inventory{}, fmt.Errorf("set NoCloud seed on the disposable UTM fixture: %v (%s)", err, output)
	}
	return p.Provider.Start(ctx, request)
}
