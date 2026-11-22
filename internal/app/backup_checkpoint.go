package app

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jflatten/ShiftBox/internal/backup"
	"github.com/jflatten/ShiftBox/internal/config"
	"github.com/jflatten/ShiftBox/internal/web"
)

// CaptureBackupCheckpoint binds a protected configuration and live controller
// to the checkpoint implementation for the active storage profile.
func (s *ControllerState) CaptureBackupCheckpoint(ctx context.Context, dir, configPath string, cfg config.Config,
	fence *backup.WriteFence) (map[string]string, error) {
	if fence == nil || cfg.DataProfile != s.Installation.Settings.DataProfile {
		return nil, errors.New("backup requires the active controller checkpoint")
	}
	inputs := backup.ProtectedInputs{Configuration: configPath, InstallationKey: filepath.Join(cfg.InstallationDir, "installation.key"),
		GuestCA: filepath.Join(cfg.InstallationDir, "guest-ca.json")}
	pause := func(ctx context.Context) (func(), error) {
		// In-flight workspace steps may need new guest requests to finish.
		// If they cannot drain promptly, reopen admission and retry later.
		drain, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		return fence.Pause(drain)
	}
	switch cfg.DataProfile {
	case "sqlite":
		resume, err := pause(ctx)
		if err != nil {
			return nil, err
		}
		defer resume()
		return backup.StageSQLiteCheckpoint(ctx, dir, s.DB, filepath.Join(cfg.InstallationDir, "dex.db"), inputs)
	case "postgres":
		if s.DexPostgres == nil {
			return nil, errors.New("backup requires the configured PostgreSQL identity store")
		}
		dex := s.DexPostgres
		q := url.Values{"sslmode": {"verify-full"}, "sslrootcert": {dex.CAFile}}
		uri := url.URL{Scheme: "postgres", Host: net.JoinHostPort(dex.Host, strconv.Itoa(int(dex.Port))),
			Path: "/" + dex.Database, User: url.UserPassword(dex.User, dex.Password), RawQuery: q.Encode()}
		return backup.StagePostgresCheckpoint(ctx, dir, cfg.PostgresURI, uri.String(), inputs, pause)
	default:
		return nil, errors.New("backup requires a supported data profile")
	}
}

// CheckpointManagement coordinates management and identity requests with
// backup capture. Read-only event streams keep reporting operation progress.
func CheckpointManagement(fence *backup.WriteFence, next http.Handler) http.Handler {
	gated := checkpointRequest(fence, next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		identity := strings.HasPrefix(path, "/auth/") || strings.HasPrefix(path, "/dex/")
		management := strings.HasPrefix(path, "/api/v1/") &&
			(r.Method != http.MethodGet || path == "/api/v1/git/authorization/callback")
		if !identity && !management {
			next.ServeHTTP(w, r)
			return
		}
		gated.ServeHTTP(w, r)
	})
}

func checkpointRequest(fence *backup.WriteFence, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := withCheckpointActivity(r.Context(), fence, func() error {
			next.ServeHTTP(w, r)
			return nil
		}); err != nil {
			web.WriteProblem(w, web.RequestID(r.Context()), web.Problem{Status: http.StatusServiceUnavailable,
				Code: "backup.checkpoint_active", Title: "The backup checkpoint is in progress"})
		}
	})
}

func withCheckpointActivity(ctx context.Context, fence *backup.WriteFence, run func() error) error {
	if fence == nil {
		return run()
	}
	leave, err := fence.Enter(ctx)
	if err != nil {
		return err
	}
	defer leave()
	return run()
}
