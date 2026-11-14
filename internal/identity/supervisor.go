package identity

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

// DexVersion is the identity binary validated for this controller release.
const DexVersion = "v2.46.1"

var (
	// ErrDexExited reports child-process failure without exposing its output.
	ErrDexExited = errors.New("managed identity service exited")
	// ErrDexUnhealthy prevents sign-in while identity readiness is unproven.
	ErrDexUnhealthy = errors.New("managed identity service is not ready")
	// ErrDexVersion blocks an unvalidated identity binary before it opens storage.
	ErrDexVersion = errors.New("managed identity service version is unsupported")
)

// DexProcess owns one native identity child and its health lifecycle. Configure
// its fields before Run; Healthy is safe for concurrent controller status reads.
type DexProcess struct {
	BinaryPath      string
	ConfigPath      string
	HealthURL       string
	Client          *http.Client
	PollInterval    time.Duration
	StartupTimeout  time.Duration
	ShutdownTimeout time.Duration
	healthy         atomic.Bool
	running         atomic.Bool
}

// Healthy is true only after the running process has passed readiness checks.
func (p *DexProcess) Healthy() bool { return p.healthy.Load() }

// Run supervises a pinned process until cancellation, exit, or health failure.
// It signals graceful shutdown, bounds the wait, and joins its sole wait goroutine.
func (p *DexProcess) Run(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !p.running.CompareAndSwap(false, true) {
		return errors.New("identity supervisor is already running")
	}
	defer p.running.Store(false)
	u, err := url.Parse(p.HealthURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || !loopbackAddress(u.Host) || !filepath.IsAbs(p.BinaryPath) || !filepath.IsAbs(p.ConfigPath) || p.Client == nil {
		return ErrDexUnhealthy
	}
	interval := p.PollInterval
	if interval <= 0 {
		interval = time.Second
	}
	startup := p.StartupTimeout
	if startup <= 0 {
		startup = 10 * time.Second
	}
	shutdown := p.ShutdownTimeout
	if shutdown <= 0 {
		shutdown = 5 * time.Second
	}
	versionCtx, versionCancel := context.WithTimeout(ctx, startup)
	versionCommand := exec.CommandContext(versionCtx, p.BinaryPath, "version")
	versionCommand.Env = []string{} // Host Dex feature flags must not change the managed product.
	version, err := versionCommand.Output()
	versionCancel()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return ErrDexExited
	}
	if strings.SplitN(string(version), "\n", 2)[0] != "Dex Version: "+DexVersion {
		return ErrDexVersion
	}
	childCtx, stop := context.WithCancel(ctx)
	command := exec.CommandContext(childCtx, p.BinaryPath, "serve", p.ConfigPath)
	command.Env = []string{}
	command.Stdout = io.Discard
	command.Stderr = io.Discard // Child diagnostics can contain identity or database secrets.
	command.Cancel = func() error { return command.Process.Signal(syscall.SIGTERM) }
	command.WaitDelay = shutdown
	if err := command.Start(); err != nil {
		stop()
		return ErrDexExited
	}
	exited := make(chan error, 1)
	go func() { exited <- command.Wait() }() // Child cancellation and WaitDelay guarantee this goroutine exits.
	waited := false
	defer func() {
		p.healthy.Store(false)
		stop()
		if !waited {
			<-exited
		}
	}()
	deadline := time.Now().Add(startup)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		probeCtx, cancel := context.WithTimeout(ctx, min(startup, 5*time.Second))
		err := p.probe(probeCtx)
		cancel()
		if err := ctx.Err(); err != nil {
			return err
		}
		if err == nil {
			p.healthy.Store(true)
		} else if p.healthy.Load() || !time.Now().Before(deadline) {
			return ErrDexUnhealthy
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-exited:
			waited = true
			return ErrDexExited
		case <-ticker.C:
		}
	}
}

func (p *DexProcess) probe(ctx context.Context) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, p.HealthURL, nil)
	if err != nil {
		return ErrDexUnhealthy
	}
	response, err := p.Client.Do(request)
	if err != nil {
		return ErrDexUnhealthy
	}
	defer response.Body.Close()
	if _, err := io.Copy(io.Discard, io.LimitReader(response.Body, 4096)); err != nil {
		return ErrDexUnhealthy
	}
	if response.StatusCode != http.StatusOK {
		return ErrDexUnhealthy
	}
	return nil
}
