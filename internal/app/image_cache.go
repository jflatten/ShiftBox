package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/jflatten/ShiftBox/internal/backup"
	"github.com/jflatten/ShiftBox/internal/guest"
	"github.com/jflatten/ShiftBox/internal/imagecache"
	"github.com/jflatten/ShiftBox/internal/proxy"
)

func guestImageCache(next http.Handler, enrollment *guest.Enrollment, registry *proxy.Registry, fence *backup.WriteFence, now func() time.Time, prepare func(context.Context, guest.Contact) context.Context) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil || len(r.TLS.VerifiedChains) == 0 || len(r.TLS.PeerCertificates) == 0 {
			http.Error(w, "Guest enrollment is required", http.StatusForbidden)
			return
		}
		leaf := r.TLS.PeerCertificates[0]
		var contact guest.Contact
		check := func() error {
			return withCheckpointActivity(r.Context(), fence, func() error {
				current, err := enrollment.Authenticate(r.Context(), leaf, now())
				if err != nil {
					return err
				}
				if current.DesiredState != "running" {
					return guest.ErrGuestUnauthorized
				}
				contact = current
				return nil
			})
		}
		if err := check(); err != nil {
			http.Error(w, "Guest enrollment is required", http.StatusForbidden)
			return
		}

		// A large layer can outlive the short poll deadline. Keep the lease
		// bounded by certificate expiry and cancel it immediately on revocation.
		deadline := now().Add(20 * time.Minute)
		if leaf.NotAfter.Before(deadline) {
			deadline = leaf.NotAfter
		}
		ctx, cancel := context.WithDeadline(r.Context(), deadline)
		defer cancel()
		controller := http.NewResponseController(w)
		if err := controller.SetWriteDeadline(deadline); err != nil {
			http.Error(w, "The image transfer deadline is unavailable", http.StatusServiceUnavailable)
			return
		}
		if registry != nil {
			scope := proxy.Scope{UserID: contact.OwnerID, WorkspaceID: contact.WorkspaceID, Service: "image-cache"}
			unregister, err := registry.Register(scope, check, func() error {
				cancel()
				return nil
			})
			if err != nil {
				http.Error(w, "Guest enrollment is required", http.StatusForbidden)
				return
			}
			defer unregister()
		}
		stop := context.AfterFunc(ctx, func() { _ = controller.SetWriteDeadline(time.Now()) })
		defer stop()
		if prepare != nil {
			ctx = prepare(ctx, contact)
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func imageCacheContext(db *sql.DB, fence *backup.WriteFence) func(context.Context, guest.Contact) context.Context {
	return func(ctx context.Context, contact guest.Contact) context.Context {
		var arch string
		_ = db.QueryRowContext(ctx, `SELECT a.architecture FROM workspaces w JOIN platform_artifacts a ON a.id=w.platform_artifact_id
			WHERE w.id=$1 AND w.generation=$2`, contact.WorkspaceID, contact.Generation).Scan(&arch)
		return imagecache.WithObservation(imagecache.WithPlatform(ctx, arch), func(observation imagecache.Observation) {
			if err := withCheckpointActivity(ctx, fence, func() error {
				return imagecache.RecordWorkspaceFreshness(ctx, db, contact.WorkspaceID, contact.Generation, observation, false)
			}); err != nil && ctx.Err() == nil {
				slog.Warn("could not record workspace image cache observation")
			}
		})
	}
}

func verifyWorkspaceImageFreshness(ctx context.Context, db *sql.DB, cache *imagecache.Cache, fence *backup.WriteFence) error {
	rows, err := db.QueryContext(ctx, `SELECT id,generation,image_cache_json FROM workspaces
		WHERE desired_state='running' AND image_cache_generation=generation AND image_cache_json LIKE $1
		ORDER BY id LIMIT 32`, `%"warningActive":true%`)
	if err != nil {
		return err
	}
	type recorded struct {
		id         string
		generation int64
		freshness  imagecache.WorkspaceFreshness
	}
	var workspaces []recorded
	for rows.Next() {
		var entry recorded
		var raw string
		if err := rows.Scan(&entry.id, &entry.generation, &raw); err != nil {
			rows.Close()
			return err
		}
		if json.Unmarshal([]byte(raw), &entry.freshness) == nil && entry.freshness.WarningActive {
			workspaces = append(workspaces, entry)
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, workspace := range workspaces {
		for _, image := range workspace.freshness.References {
			if !image.WarningActive {
				continue
			}
			check, cancel := context.WithTimeout(ctx, 10*time.Second)
			observation, err := cache.VerifyReference(check, image)
			cancel()
			if err == nil {
				if err := withCheckpointActivity(ctx, fence, func() error {
					return imagecache.RecordWorkspaceFreshness(ctx, db, workspace.id, workspace.generation, observation, true)
				}); err != nil {
					return err
				}
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
		}
	}
	return nil
}
