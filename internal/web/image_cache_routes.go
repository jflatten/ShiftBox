package web

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/jflatten/ShiftBox/internal/audit"
	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/imagecache"
	"github.com/jflatten/ShiftBox/internal/operation"
)

const imageCachePath = "/admin/image-cache"

// ImageCacheHandlers reads current cache status and stages authorized changes.
func ImageCacheHandlers(db *sql.DB, origin string, now func() time.Time, journal *operation.Journal,
	defaultPath string, runtime func() imagecache.RuntimeStatus,
) (AuthorizedHandler, AuthorizedHandler) {
	base := func(method, id, action string) AuthorizedHandler {
		return AuthorizedHandler{DB: db, Origin: origin, Now: now,
			Route: ManagementRoute{Method: method, Path: imageCachePath, OperationID: id, Action: action},
			Resolve: func(_ context.Context, _ *sql.Tx, _ *http.Request, actor auth.Principal) (AuthorizationTarget, error) {
				return AuthorizationTarget{Action: action, Resource: auth.Resource{Type: "installation", ID: actor.InstallationID}}, nil
			}}
	}
	read := func(ctx context.Context, tx *sql.Tx) (imagecache.Configuration, error) {
		var raw string
		var version int64
		if err := tx.QueryRowContext(ctx, "SELECT image_cache_settings_json,image_cache_version FROM installations WHERE singleton=1").Scan(&raw, &version); err != nil {
			return imagecache.Configuration{}, err
		}
		return imagecache.DecodeConfiguration(raw, version, defaultPath)
	}
	reply := func(config imagecache.Configuration) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			snapshot := imagecache.RuntimeStatus{Health: imagecache.Health{State: "unknown", Capabilities: []string{}}, CachedImages: []imagecache.CachedImage{}}
			if runtime != nil {
				snapshot = runtime()
			}
			w.Header().Set("ETag", strconv.Quote(strconv.FormatInt(config.Version, 10)))
			noticeJSON(http.StatusOK, struct {
				imagecache.Configuration
				imagecache.RuntimeStatus
			}{config, snapshot}, "").ServeHTTP(w, r)
		})
	}

	get := base(http.MethodGet, "getImageCache", "infrastructure.view")
	get.Apply = func(ctx context.Context, tx *sql.Tx, _ *http.Request, _ auth.Principal, _ AuthorizationTarget, _ auth.Decision) (http.Handler, error) {
		config, err := read(ctx, tx)
		if err != nil {
			return nil, err
		}
		return reply(config), nil
	}
	put := base(http.MethodPut, "updateImageCache", "infrastructure.manage")
	put.Apply = func(ctx context.Context, tx *sql.Tx, r *http.Request, actor auth.Principal, target AuthorizationTarget, _ auth.Decision) (http.Handler, error) {
		version, err := RequireVersion(r)
		if err != nil {
			return nil, err
		}
		var input imagecache.Settings
		if err := DecodeJSON(nil, r, &input, 8192); err != nil {
			return nil, err
		}
		if err := input.Validate(); err != nil {
			return nil, Problem{Status: 400, Code: "image_cache.invalid_settings", Title: err.Error()}
		}
		if _, err := tx.ExecContext(ctx, "UPDATE installations SET version=version WHERE singleton=1"); err != nil {
			return nil, err
		}
		config, err := read(ctx, tx)
		if err != nil {
			return nil, err
		}
		if version != config.Version {
			return nil, Problem{Status: 412, Code: "request.version_mismatch", Title: "Refresh the registry cache settings before updating"}
		}
		if config.Activation.Phase == "staged" || config.Activation.Phase == "draining" {
			return nil, Problem{Status: 409, Code: "image_cache.activation_in_progress", Title: "Registry cache activation is in progress",
				Fields: map[string]string{"operationId": config.Activation.OperationID}}
		}
		if config.Activation.Phase != "failed" && input.Enabled == config.Effective.Enabled &&
			input.StoragePath == config.Effective.StoragePath && input.Budget() == config.Effective.Budget() &&
			input.VolumeHighPercent == config.Effective.VolumeHighPercent && input.VolumeLowPercent == config.Effective.VolumeLowPercent {
			return reply(config), nil
		}
		next := config.Version + 1
		op, err := journal.CreateInTx(ctx, tx, operation.Request{Actor: actor.User.ID, Key: "image-cache-settings/" + strconv.FormatInt(next, 10),
			Type: "setup", OriginatingAction: imagecache.SettingsAction, Generation: next,
			Steps: []operation.Step{
				{Key: "validate", Label: "Validate cache storage"},
				{Key: "drain", Label: "Finish active pulls"},
				{Key: "activate", Label: "Activate cache settings"},
			}})
		if err != nil {
			return nil, err
		}
		config.Version = next
		config.Desired = input
		config.Activation = imagecache.Activation{Phase: "staged", OperationID: op.ID}
		encoded, err := json.Marshal(config)
		if err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE installations SET image_cache_settings_json=$1,image_cache_version=$2 WHERE singleton=1`, string(encoded), next); err != nil {
			return nil, err
		}
		if _, err := audit.Append(ctx, tx, audit.Event{OccurredAt: now(), ActorType: "user", ActorID: actor.User.ID,
			Action: "infrastructure.image_cache.update", ResourceType: "installation", ResourceID: target.Resource.ID,
			Outcome: "allowed", ReasonCode: "administrator_policy"}); err != nil {
			return nil, err
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Location", "/api/v1/operations/"+op.ID)
			noticeJSON(http.StatusAccepted, struct {
				operation.Operation
				OperationID string `json:"operationId"`
			}{op, op.ID}, "").ServeHTTP(w, r)
		}), nil
	}
	return get, put
}
