package guest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jflatten/ShiftBox/internal/workspace"
)

// ActivitySocketPath accepts process-watch heartbeats from this workspace only.
const ActivitySocketPath = credentialDirectory + "/activity.sock"

const (
	processWatchLease     = 15 * time.Second
	processWatchHeartbeat = 5 * time.Second
)

// processWatches serializes local lease changes with authenticated poll sampling.
// The guest cooperatively reports activity; the VM is not a trust boundary.
type processWatches struct {
	mu     sync.Mutex
	leases map[string]time.Time
	now    func() time.Time
}

func newProcessWatches(now func() time.Time) *processWatches {
	return &processWatches{leases: make(map[string]time.Time), now: now}
}

// WatchProcess keeps a foreground lease alive while the original Linux process
// exists in the caller's PID namespace. Sleeping processes count; zombies do not.
func WatchProcess(ctx context.Context, socket string, pid int) error {
	return watchProcess(ctx, socket, pid, inspectProcess, processWatchHeartbeat)
}

func watchProcess(ctx context.Context, socket string, pid int, inspect func(int) (string, bool, error), interval time.Duration) error {
	if pid <= 0 || !filepath.IsAbs(socket) || interval <= 0 {
		return errors.New("invalid workspace process watch")
	}
	identity, alive, err := inspect(pid)
	if err != nil || !alive || identity == "" {
		return errors.New("the process is absent or cannot be inspected in this container")
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	token := hex.EncodeToString(nonce[:])
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
	defer func() {
		// Cancellation must remove this lease promptly; a killed client instead
		// expires naturally when its last heartbeat becomes stale.
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer cancel()
		_ = sendProcessWatch(cleanup, client, token, http.MethodDelete)
	}()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		current, alive, err := inspect(pid)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return errors.New("cannot inspect the watched process")
		}
		if !alive || current != identity {
			return nil
		}
		if err := sendProcessWatch(ctx, client, token, http.MethodPut); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func inspectProcess(pid int) (string, bool, error) {
	raw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return "", false, err
	}
	return parseProcessStat(string(raw))
}

func parseProcessStat(raw string) (string, bool, error) {
	// Linux comm may contain spaces and parentheses; fields after its final
	// closing parenthesis retain the kernel's documented numbering.
	closing := strings.LastIndex(raw, ") ")
	if closing < 0 {
		return "", false, errors.New("invalid Linux process identity")
	}
	fields := strings.Fields(raw[closing+2:])
	if len(fields) < 20 || len(fields[0]) != 1 {
		return "", false, errors.New("invalid Linux process identity")
	}
	if _, err := strconv.ParseUint(fields[19], 10, 64); err != nil {
		return "", false, errors.New("invalid Linux process start identity")
	}
	state := fields[0]
	return fields[19], state != "Z" && state != "X" && state != "x", nil
}

func sendProcessWatch(ctx context.Context, client *http.Client, token, method string) error {
	request, err := http.NewRequestWithContext(ctx, method, "http://local/watch/"+token, nil)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("workspace activity service is unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		return errors.New("workspace activity service refused the watch")
	}
	return nil
}

func (w *processWatches) renew(token string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	now := w.now()
	w.expire(now)
	w.leases[token] = now.Add(processWatchLease)
}

func (w *processWatches) remove(token string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.leases, token)
}

func (w *processWatches) expire(now time.Time) {
	for token, until := range w.leases {
		if !now.Before(until) {
			delete(w.leases, token)
		}
	}
}

func (w *processWatches) sample(now time.Time) *workspace.ProcessActivity {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.expire(now)
	return &workspace.ProcessActivity{SampledAt: now.UTC(), Active: len(w.leases) > 0}
}

func (w *processWatches) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	token, ok := strings.CutPrefix(request.URL.RequestURI(), "/watch/")
	decoded, err := hex.DecodeString(token)
	if !ok || err != nil || len(decoded) != 16 || request.ContentLength != 0 ||
		(request.Method != http.MethodPut && request.Method != http.MethodDelete) {
		response.WriteHeader(http.StatusBadRequest)
		return
	}
	if request.Method == http.MethodPut {
		w.renew(token)
	} else {
		w.remove(token)
	}
	response.WriteHeader(http.StatusNoContent)
}

func listenProcessWatchSocket(ctx context.Context, path string, watches *processWatches) (func(), error) {
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, errors.New("activity socket path is occupied")
		}
		connection, err := net.DialTimeout("unix", path, time.Second)
		if err == nil {
			connection.Close()
			return nil, errors.New("activity socket is already serving")
		}
		if err := os.Remove(path); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0666); err != nil {
		listener.Close()
		return nil, err
	}
	socketCtx, cancel := context.WithCancel(ctx)
	server := &http.Server{Handler: watches, ReadHeaderTimeout: time.Second, ReadTimeout: 3 * time.Second,
		WriteTimeout: 3 * time.Second, IdleTimeout: 3 * time.Second, BaseContext: func(net.Listener) context.Context { return socketCtx }}
	done := make(chan struct{})
	go func() { defer close(done); _ = server.Serve(listener) }()
	return func() { cancel(); _ = server.Close(); _ = listener.Close(); <-done }, nil
}
