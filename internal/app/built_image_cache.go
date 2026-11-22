package app

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/guest"
	"github.com/jflatten/ShiftBox/internal/identity"
	"github.com/jflatten/ShiftBox/internal/imagecache"
	"github.com/jflatten/ShiftBox/internal/proxy"
	"github.com/jflatten/ShiftBox/internal/web"
	"github.com/opencontainers/go-digest"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
)

const (
	builtImagePullLifetime     = 5 * time.Minute
	builtImageUploadLifetime   = 15 * time.Minute
	builtImageMaxClosureGrants = 256
	builtImageRepositoryPrefix = "shiftbox-built/"
)

type builtImageSource interface {
	AuthorizeProjectSource(context.Context, string, string) (int64, error)
	ReadProjectConfig(context.Context, string, string, string, string) (identity.GitHubOriginConfig, error)
	AuthorizationViewInTx(context.Context, *sql.Tx, string) (identity.GitHubAuthorizationView, error)
}

type builtImageService struct {
	cache    *imageCacheRuntime
	source   builtImageSource
	registry *proxy.Registry
	now      func() time.Time
	host     string
	grantsMu sync.Mutex
	reads    map[string]*builtImageReadGrant
	previous map[string]*builtImageRebuildHold
}

type builtImagePublication struct {
	AttemptID string    `json:"attemptId"`
	Image     string    `json:"image"`
	ExpiresAt time.Time `json:"expiresAt"`
}

type builtImageTransfer struct {
	lease    *imagecache.BuiltImageLease
	cache    *imagecache.Cache
	scope    imagecache.BuiltScope
	manifest digest.Digest
	close    func()
}

type builtImageStorage struct {
	path       string
	generation int64
}

type builtImageSlot struct {
	repository, path, config string
	manifest                 digest.Digest
	scope                    imagecache.BuiltScope
	storage                  builtImageStorage
}

type builtImageAttempt struct {
	id, installation, project, workspace, operation                string
	os, arch, repository, path, config, kind, state                string
	workspaceGeneration, dispatch, policyGeneration, sourceVersion int64
	storage                                                        builtImageStorage
	manifest                                                       sql.NullString
	expires                                                        time.Time
}

type builtImageReadGrant struct {
	authority     builtImageAuthority
	sourceVersion int64
	storage       builtImageStorage
	slot          builtImageSlot
	lease         *imagecache.BuiltImageLease
	expires       time.Time
	ctx           context.Context
}

type builtImageRebuildHold struct {
	slot    builtImageSlot
	ctx     context.Context
	release func()
}

func newBuiltImageService(cache *imageCacheRuntime, source builtImageSource, registry *proxy.Registry, now func() time.Time) *builtImageService {
	service := &builtImageService{cache: cache, source: source, registry: registry, now: now,
		reads: make(map[string]*builtImageReadGrant), previous: make(map[string]*builtImageRebuildHold)}
	if cache != nil {
		if endpoint, err := url.Parse(cache.guestEndpoint); err == nil && endpoint.Scheme == "https" && endpoint.User == nil {
			service.host = endpoint.Host
		}
	}
	return service
}

func (s *builtImageService) lookup(ctx context.Context, contact guest.Contact, operationID string, dispatchVersion int64, configSHA256 string) (string, error) {
	authority, version, _, err := s.negotiate(ctx, contact, operationID, dispatchVersion, configSHA256, false)
	if err != nil || authority.kind != "launch" {
		return "", builtImageDecline(err)
	}
	cache, storage, unlock, err := s.lockStorage(ctx)
	if err != nil {
		return "", builtImageDecline(err)
	}
	defer unlock()
	var slot builtImageSlot
	if err := database.Transaction(ctx, s.cache.db, func(tx *sql.Tx) error {
		if err := s.recheck(ctx, tx, contact, authority, version, false); err != nil {
			return err
		}
		var err error
		slot, err = readBuiltImageSlot(ctx, tx, authority)
		return err
	}); err != nil {
		return "", builtImageDecline(err)
	}
	if !slot.matches(authority, configSHA256, storage) {
		return "", nil
	}
	lease, err := cache.AcquireBuiltImage(slot.scope, slot.manifest, authority.platform())
	if err != nil {
		return "", builtImageDecline(err)
	}
	grant := &builtImageReadGrant{authority: authority, sourceVersion: version, storage: storage,
		slot: slot, lease: lease, expires: s.now().Add(builtImagePullLifetime)}
	id, err := s.admitRead(ctx, contact, grant)
	if err != nil {
		lease.Close()
		return "", builtImageDecline(err)
	}
	return s.reference(id, "@"+slot.manifest.String()), nil
}

func (s *builtImageService) admitRead(ctx context.Context, contact guest.Contact, grant *builtImageReadGrant) (string, error) {
	s.grantsMu.Lock()
	defer s.grantsMu.Unlock()
	for id, current := range s.reads {
		if current.ctx.Err() == nil && current.expires.Sub(s.now()) > time.Minute && current.authority == grant.authority &&
			current.sourceVersion == grant.sourceVersion && current.slot == grant.slot {
			grant.lease.Close()
			return id, nil
		}
	}
	if len(s.reads) >= builtImageMaxClosureGrants {
		return "", imagecache.ErrBuiltStorageFull
	}
	id := uuid.NewString()
	// A read grant outlives negotiation, but policy revocation and its fixed
	// deadline end the entire closure lease even while the guest is idle.
	transfer, stop, err := grant.authority.registerBuiltImageTransfer(context.Background(), s.registry, id, grant.expires,
		func(context.Context) error {
			return database.Transaction(ctx, s.cache.db, func(tx *sql.Tx) error {
				if err := s.recheck(ctx, tx, contact, grant.authority, grant.sourceVersion, false); err != nil {
					return err
				}
				current, err := readBuiltImageSlot(ctx, tx, grant.authority)
				if err != nil {
					return err
				}
				if current != grant.slot {
					return imagecache.ErrBuiltImageUnavailable
				}
				return nil
			})
		})
	if err != nil {
		return "", err
	}
	grant.ctx = transfer
	s.reads[id] = grant
	context.AfterFunc(transfer, func() {
		s.grantsMu.Lock()
		delete(s.reads, id)
		s.grantsMu.Unlock()
		stop()
		grant.lease.Close()
	})
	return id, nil
}

func (s *builtImageService) begin(ctx context.Context, contact guest.Contact, operationID string, dispatchVersion int64, configSHA256 string) (builtImagePublication, error) {
	authority, version, resumeOnly, err := s.negotiate(ctx, contact, operationID, dispatchVersion, configSHA256, true)
	if err != nil {
		return builtImagePublication{}, builtImageDecline(err)
	}
	cache, storage, unlock, err := s.lockStorage(ctx)
	if err != nil {
		return builtImagePublication{}, builtImageDecline(err)
	}
	defer unlock()
	now := s.now().UTC()
	id := uuid.NewString()
	expires := now.Add(builtImageUploadLifetime)
	err = database.Transaction(ctx, s.cache.db, func(tx *sql.Tx) error {
		// Negotiation began on an active dispatch. Only that same dispatch
		// may finish admission if it succeeded during the source check.
		if err := s.recheck(ctx, tx, contact, authority, version, true); err != nil {
			return err
		}
		var currentID, currentConfig, currentExpiry string
		err := tx.QueryRowContext(ctx, `SELECT id,config_sha256,expires_at FROM built_image_attempts
			WHERE operation_id=$1 AND dispatch_version=$2 AND policy_generation=$3 AND storage_generation=$4 AND storage_path=$5
			AND source_authorization_version=$6 AND expires_at>$7 AND state IN ('admitted','receiving','complete')
			ORDER BY created_at DESC LIMIT 1`, operationID, dispatchVersion, authority.policyGeneration,
			storage.generation, storage.path, version, builtImageTime(now)).Scan(&currentID, &currentConfig, &currentExpiry)
		if err == nil {
			if currentConfig != configSHA256 {
				return guest.ErrGuestUnauthorized
			}
			id = currentID
			expires, err = time.Parse(time.RFC3339Nano, currentExpiry)
			return err
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if resumeOnly {
			return guest.ErrGuestUnauthorized
		}
		repository, _ := identity.CanonicalGitHubRepository(authority.repositoryURL)
		_, err = tx.ExecContext(ctx, `INSERT INTO built_image_attempts(id,installation_id,project_id,workspace_id,workspace_generation,
			operation_id,dispatch_version,target_os,target_arch,repository_url,config_path,config_sha256,policy_generation,
			source_authorization_version,kind,state,created_at,expires_at,storage_path,storage_generation)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,'admitted',$16,$17,$18,$19)`,
			id, authority.installationID, authority.projectID, authority.workspaceID, authority.generation, operationID, dispatchVersion,
			authority.targetOS, authority.targetArch, repository, authority.configPath, configSHA256, authority.policyGeneration,
			version, authority.kind, builtImageTime(now), builtImageTime(expires), storage.path, storage.generation)
		return err
	})
	if err != nil {
		return builtImagePublication{}, builtImageDecline(err)
	}
	if authority.kind == "rebuild" {
		if err := s.holdRebuildPrevious(ctx, contact, cache, storage, authority, version, id, expires); err != nil {
			return builtImagePublication{}, builtImageDecline(err)
		}
	}
	return builtImagePublication{AttemptID: id, Image: s.reference(id, ":candidate"), ExpiresAt: expires}, nil
}

func (s *builtImageService) holdRebuildPrevious(ctx context.Context, contact guest.Contact, cache *imagecache.Cache,
	storage builtImageStorage, authority builtImageAuthority, version int64, id string, expires time.Time) error {
	var slot builtImageSlot
	if err := database.Transaction(ctx, s.cache.db, func(tx *sql.Tx) error {
		var err error
		slot, err = readBuiltImageSlot(ctx, tx, authority)
		return err
	}); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}
	// Even a stale candidate must leave the current image usable for new
	// launches with its exact config. The candidate's config is checked at finish.
	if !slot.inScope(authority, storage) {
		return nil
	}

	s.grantsMu.Lock()
	var replaced *builtImageRebuildHold
	defer func() {
		s.grantsMu.Unlock()
		if replaced != nil {
			replaced.release()
		}
	}()
	previous := s.previous[id]
	if previous != nil && previous.slot == slot && previous.ctx.Err() == nil {
		return nil
	}
	if previous == nil && len(s.previous) >= builtImageMaxClosureGrants {
		return imagecache.ErrBuiltStorageFull
	}
	lease, err := cache.AcquireBuiltImage(slot.scope, slot.manifest, authority.platform())
	if errors.Is(err, imagecache.ErrBuiltImageUnavailable) {
		return nil
	}
	if err != nil {
		return err
	}
	transfer, stop, err := authority.registerBuiltImageTransfer(context.Background(), s.registry, id, expires,
		func(context.Context) error {
			return database.Transaction(ctx, s.cache.db, func(tx *sql.Tx) error {
				if err := s.recheck(ctx, tx, contact, authority, version, true); err != nil {
					return err
				}
				current, err := readBuiltImageSlot(ctx, tx, authority)
				if err != nil {
					return err
				}
				if current != slot {
					return imagecache.ErrBuiltImageUnavailable
				}
				return nil
			})
		})
	if err != nil {
		lease.Close()
		return err
	}
	hold := &builtImageRebuildHold{slot: slot, ctx: transfer}
	hold.release = sync.OnceFunc(func() {
		s.grantsMu.Lock()
		if s.previous[id] == hold {
			delete(s.previous, id)
		}
		s.grantsMu.Unlock()
		lease.Close()
		stop()
	})
	s.previous[id] = hold
	replaced = previous
	context.AfterFunc(transfer, hold.release)
	return nil
}

func (s *builtImageService) releasePrevious(id string) {
	s.grantsMu.Lock()
	hold := s.previous[id]
	s.grantsMu.Unlock()
	if hold != nil {
		hold.release()
	}
}

func (s *builtImageService) transfer(ctx context.Context, contact guest.Contact, scopeID string) (context.Context, *builtImageTransfer, error) {
	cache, storage, unlock, err := s.lockStorage(ctx)
	if err != nil {
		return nil, nil, err
	}
	s.grantsMu.Lock()
	grant := s.reads[scopeID]
	s.grantsMu.Unlock()
	if grant != nil {
		transfer, info, err := s.readTransfer(ctx, contact, scopeID, grant, cache, storage, unlock)
		if err != nil {
			unlock()
		}
		return transfer, info, err
	}
	transfer, info, err := s.uploadTransfer(ctx, contact, scopeID, cache, storage, unlock)
	if err != nil {
		unlock()
	}
	return transfer, info, err
}

func (s *builtImageService) readTransfer(ctx context.Context, contact guest.Contact, id string, grant *builtImageReadGrant, cache *imagecache.Cache, storage builtImageStorage, unlock func()) (context.Context, *builtImageTransfer, error) {
	if grant.ctx.Err() != nil || grant.storage != storage ||
		grant.authority.workspaceID != contact.WorkspaceID || grant.authority.generation != contact.Generation {
		return nil, nil, guest.ErrGuestUnauthorized
	}
	request, cancel := context.WithCancel(ctx)
	watch := context.AfterFunc(grant.ctx, cancel)
	transfer, stop, err := grant.authority.registerBuiltImageTransfer(request, s.registry, id, grant.expires,
		func(ctx context.Context) error {
			return database.Transaction(ctx, s.cache.db, func(tx *sql.Tx) error {
				return s.recheck(ctx, tx, contact, grant.authority, grant.sourceVersion, true)
			})
		})
	if err != nil {
		watch()
		cancel()
		return nil, nil, err
	}
	return transfer, &builtImageTransfer{lease: grant.lease, cache: cache, scope: grant.slot.scope, manifest: grant.slot.manifest,
		close: func() { stop(); watch(); cancel(); unlock() }}, nil
}

func (s *builtImageService) uploadTransfer(ctx context.Context, contact guest.Contact, id string, cache *imagecache.Cache, storage builtImageStorage, unlock func()) (context.Context, *builtImageTransfer, error) {
	attempt, authority, err := s.attemptAuthority(ctx, contact, id)
	if err != nil {
		return nil, nil, err
	}
	if attempt.storage != storage || !attempt.writable() {
		return nil, nil, guest.ErrGuestUnauthorized
	}
	if authority.kind == "rebuild" {
		// Reacquire the prior closure before storing bytes if the controller
		// restarted or the current slot changed during an overlapping rebuild.
		if err := s.holdRebuildPrevious(ctx, contact, cache, storage, authority, attempt.sourceVersion, id, attempt.expires); err != nil {
			return nil, nil, err
		}
	}
	transfer, stop, err := authority.registerBuiltImageTransfer(ctx, s.registry, id, attempt.expires, func(ctx context.Context) error {
		return database.Transaction(ctx, s.cache.db, func(tx *sql.Tx) error {
			if err := s.recheck(ctx, tx, contact, authority, attempt.sourceVersion, true); err != nil {
				return err
			}
			result, err := tx.ExecContext(ctx, `UPDATE built_image_attempts SET state='receiving'
				WHERE id=$1 AND state IN ('admitted','receiving')`, id)
			return builtImageChanged(result, err)
		})
	})
	if err != nil {
		return nil, nil, err
	}
	return transfer, &builtImageTransfer{cache: cache, scope: attempt.scope(), close: func() { stop(); unlock() }}, nil
}

func (s *builtImageService) finish(ctx context.Context, contact guest.Contact, id string) (string, error) {
	attempt, authority, err := s.attemptAuthority(ctx, contact, id)
	if err != nil {
		if errors.Is(err, guest.ErrGuestUnauthorized) && attempt.workspace == contact.WorkspaceID && attempt.workspaceGeneration == contact.Generation {
			s.releasePrevious(id)
		}
		return "", err
	}
	if attempt.terminal() {
		s.releasePrevious(id)
		return attempt.state, nil
	}
	ctx, cancel := context.WithDeadline(ctx, attempt.expires)
	defer cancel()
	if err := s.waitForRebuildSuccess(ctx, contact, authority, attempt); err != nil {
		if errors.Is(err, guest.ErrGuestUnauthorized) {
			s.releasePrevious(id)
		}
		return "", err
	}
	manifest, lease, err := s.preparePublication(ctx, contact, authority, attempt)
	if err != nil && !errors.Is(err, imagecache.ErrBuiltImageUnavailable) {
		return "", err
	}
	var origin identity.GitHubOriginConfig
	if lease != nil {
		defer lease.Close()
		// Remote source verification happens outside the write transaction and
		// outside the runtime storage lock. The staged closure stays pinned.
		origin, err = s.source.ReadProjectConfig(ctx, authority.ownerID, authority.repositoryURL, authority.repositoryRef, authority.configPath)
		if err != nil {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			origin = identity.GitHubOriginConfig{}
		}
	}
	disposition, err := s.promotePublication(ctx, contact, authority, attempt, manifest, origin)
	if err == nil || errors.Is(err, guest.ErrGuestUnauthorized) {
		s.releasePrevious(id)
	}
	return disposition, err
}

func (s *builtImageService) waitForRebuildSuccess(ctx context.Context, contact guest.Contact, authority builtImageAuthority, attempt builtImageAttempt) error {
	if attempt.kind != "rebuild" {
		return nil
	}
	return database.Transaction(ctx, s.cache.db, func(tx *sql.Tx) error {
		if err := s.recheck(ctx, tx, contact, authority, attempt.sourceVersion, true); err != nil {
			return err
		}
		return builtImageRebuildOutcome(ctx, tx, attempt)
	})
}

// A staged rebuild can finish uploading before the controller finishes the
// workspace operation. Verification and promotion wait for both successes.
func builtImageRebuildOutcome(ctx context.Context, tx *sql.Tx, attempt builtImageAttempt) error {
	if attempt.kind != "rebuild" {
		return nil
	}
	var operation, dispatch string
	err := tx.QueryRowContext(ctx, `SELECT o.status,g.status FROM operations o JOIN guest_dispatches g ON g.operation_id=o.id
		WHERE o.id=$1 AND g.dispatch_version=$2`, attempt.operation, attempt.dispatch).Scan(&operation, &dispatch)
	if errors.Is(err, sql.ErrNoRows) {
		return guest.ErrGuestUnauthorized
	}
	if err != nil {
		return err
	}
	if operation == "succeeded" && dispatch == "succeeded" {
		return nil
	}
	if (operation == "running" || operation == "waiting") && (dispatch == "pending" || dispatch == "running" || dispatch == "succeeded") {
		return web.ErrBuiltImagePromotionPending
	}
	return guest.ErrGuestUnauthorized
}

func (s *builtImageService) preparePublication(ctx context.Context, contact guest.Contact, authority builtImageAuthority, attempt builtImageAttempt) (digest.Digest, *imagecache.BuiltImageLease, error) {
	cache, storage, unlock, err := s.lockStorage(ctx)
	if err != nil {
		return "", nil, err
	}
	defer unlock()
	if storage != attempt.storage {
		return "", nil, guest.ErrGuestUnauthorized
	}
	manifest, ok := cache.BuiltCandidate(attempt.scope())
	if !ok || attempt.manifest.Valid && attempt.manifest.String != manifest.String() {
		return "", nil, imagecache.ErrBuiltImageUnavailable
	}
	lease, err := cache.AcquireBuiltImage(attempt.scope(), manifest, authority.platform())
	if err != nil {
		return "", nil, err
	}
	if err := database.Transaction(ctx, s.cache.db, func(tx *sql.Tx) error {
		if err := s.recheck(ctx, tx, contact, authority, attempt.sourceVersion, true); err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `UPDATE built_image_attempts SET state='complete',manifest_digest=$1
			WHERE id=$2 AND state IN ('admitted','receiving','complete')
			AND (manifest_digest IS NULL OR manifest_digest=$1)`, manifest.String(), attempt.id)
		if err != nil {
			return err
		}
		affected, err := result.RowsAffected()
		if err != nil || affected == 1 {
			return err
		}
		current, err := readBuiltImageAttempt(ctx, tx, attempt.id)
		if err == nil && current.terminal() {
			return nil
		}
		if err != nil {
			return err
		}
		return guest.ErrGuestUnauthorized
	}); err != nil {
		lease.Close()
		return "", nil, err
	}
	return manifest, lease, nil
}

func (s *builtImageService) promotePublication(ctx context.Context, contact guest.Contact, authority builtImageAuthority, attempt builtImageAttempt, manifest digest.Digest, origin identity.GitHubOriginConfig) (string, error) {
	cache, storage, unlock, err := s.lockStorage(ctx)
	if err != nil {
		return "", err
	}
	defer unlock()
	if storage != attempt.storage {
		return "", guest.ErrGuestUnauthorized
	}
	disposition := "discarded"
	err = database.Transaction(ctx, s.cache.db, func(tx *sql.Tx) error {
		if err := s.recheck(ctx, tx, contact, authority, attempt.sourceVersion, true); err != nil {
			return err
		}
		current, err := readBuiltImageAttempt(ctx, tx, attempt.id)
		if err != nil {
			return err
		}
		if current.terminal() {
			disposition = current.state
			return nil
		}
		if !current.matches(authority) || !s.now().Before(current.expires) || current.state == "expired" {
			return guest.ErrGuestUnauthorized
		}
		if err := builtImageRebuildOutcome(ctx, tx, current); err != nil {
			return err
		}
		ready := manifest != "" && current.state == "complete" && current.manifest.String == manifest.String() &&
			origin.Repository == current.repository && origin.ConfigSHA256 == current.config && origin.Revision != "" &&
			origin.AuthorizationVersion == current.sourceVersion
		if ready && current.kind == "launch" {
			var err error
			ready, err = canPromoteBuiltImage(ctx, tx, cache, authority, current.config, storage)
			if err != nil {
				return err
			}
		}
		if ready {
			if err := writeBuiltImageSlot(ctx, tx, current, manifest, origin.Revision, s.now()); err != nil {
				return err
			}
			disposition = "promoted"
		}
		result, err := tx.ExecContext(ctx, `UPDATE built_image_attempts SET state=$1 WHERE id=$2`, disposition, current.id)
		return builtImageChanged(result, err)
	})
	if err != nil {
		return "", err
	}
	return disposition, nil
}

func canPromoteBuiltImage(ctx context.Context, tx *sql.Tx, cache *imagecache.Cache, authority builtImageAuthority, config string, storage builtImageStorage) (bool, error) {
	existing, err := readBuiltImageSlot(ctx, tx, authority)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if !existing.matches(authority, config, storage) {
		return true, nil
	}
	lease, err := cache.AcquireBuiltImage(existing.scope, existing.manifest, authority.platform())
	if errors.Is(err, imagecache.ErrBuiltImageUnavailable) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	lease.Close()
	return false, nil
}

func writeBuiltImageSlot(ctx context.Context, tx *sql.Tx, attempt builtImageAttempt, manifest digest.Digest, revision string, now time.Time) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO built_image_slots (installation_id,project_id,target_os,target_arch,repository_url,
		config_path,config_sha256,manifest_digest,policy_generation,storage_generation,storage_path,storage_scope,
		publication_kind,origin_revision,promoted_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)
		ON CONFLICT (installation_id,project_id,target_os,target_arch) DO UPDATE SET repository_url=excluded.repository_url,
		config_path=excluded.config_path,config_sha256=excluded.config_sha256,manifest_digest=excluded.manifest_digest,
		policy_generation=excluded.policy_generation,storage_generation=excluded.storage_generation,storage_path=excluded.storage_path,
		storage_scope=excluded.storage_scope,publication_kind=excluded.publication_kind,origin_revision=excluded.origin_revision,
		promoted_at=excluded.promoted_at`, attempt.installation, attempt.project, attempt.os, attempt.arch, attempt.repository,
		attempt.path, attempt.config, manifest.String(), attempt.policyGeneration, attempt.storage.generation,
		attempt.storage.path, attempt.id, attempt.kind, revision, builtImageTime(now))
	return err
}

func (s *builtImageService) lockStorage(ctx context.Context) (*imagecache.Cache, builtImageStorage, func(), error) {
	runtime := s.cache
	if runtime == nil || s.host == "" || s.registry == nil || s.source == nil || runtime.draining.Load() || !runtime.mu.TryRLock() {
		return nil, builtImageStorage{}, nil, imagecache.ErrBuiltImageUnavailable
	}
	unlock := runtime.mu.RUnlock
	if runtime.draining.Load() || runtime.unavailable || runtime.active == runtime.stream {
		unlock()
		return nil, builtImageStorage{}, nil, imagecache.ErrBuiltImageUnavailable
	}
	config, err := runtime.configuration(ctx)
	if err != nil {
		unlock()
		return nil, builtImageStorage{}, nil, err
	}
	return runtime.active, builtImageStorage{path: config.Effective.StoragePath, generation: config.Effective.Version}, unlock, nil
}

func (s *builtImageService) negotiate(ctx context.Context, contact guest.Contact, id string, version int64, hash string, allowResume bool) (builtImageAuthority, int64, bool, error) {
	if s.cache == nil || s.registry == nil || s.source == nil || s.host == "" || !validBuiltImageHash(hash) {
		return builtImageAuthority{}, 0, false, imagecache.ErrBuiltImageUnavailable
	}
	var authority builtImageAuthority
	var resumeOnly bool
	if err := database.Transaction(ctx, s.cache.db, func(tx *sql.Tx) error {
		var err error
		authority, err = loadBuiltImageAuthority(ctx, tx, contact, id, version, false)
		if allowResume && errors.Is(err, guest.ErrGuestUnauthorized) {
			authority, err = loadBuiltImageAuthority(ctx, tx, contact, id, version, true)
			resumeOnly = true
		}
		return err
	}); err != nil {
		return builtImageAuthority{}, 0, false, err
	}
	sourceVersion, err := s.source.AuthorizeProjectSource(ctx, authority.ownerID, authority.repositoryURL)
	if err != nil || sourceVersion < 1 {
		return builtImageAuthority{}, 0, false, guest.ErrGuestUnauthorized
	}
	return authority, sourceVersion, resumeOnly, nil
}

func (s *builtImageService) recheck(ctx context.Context, tx *sql.Tx, contact guest.Contact, expected builtImageAuthority, sourceVersion int64, allowSucceeded bool) error {
	current, err := loadBuiltImageAuthority(ctx, tx, contact, expected.operationID, expected.dispatchVersion, allowSucceeded)
	if err != nil {
		return err
	}
	view, err := s.source.AuthorizationViewInTx(ctx, tx, current.ownerID)
	if err != nil {
		return err
	}
	if view.State != "connected" {
		return guest.ErrGuestUnauthorized
	}
	return validateBuiltImageAuthority(current, expected, view.Version, sourceVersion)
}

func (s *builtImageService) attemptAuthority(ctx context.Context, contact guest.Contact, id string) (builtImageAttempt, builtImageAuthority, error) {
	if s.cache == nil || s.source == nil {
		return builtImageAttempt{}, builtImageAuthority{}, guest.ErrGuestUnauthorized
	}
	var attempt builtImageAttempt
	var auth builtImageAuthority
	err := database.Transaction(ctx, s.cache.db, func(tx *sql.Tx) error {
		var err error
		attempt, err = readBuiltImageAttempt(ctx, tx, id)
		if errors.Is(err, sql.ErrNoRows) {
			return guest.ErrGuestUnauthorized
		}
		if err != nil {
			return err
		}
		if attempt.workspace != contact.WorkspaceID || attempt.workspaceGeneration != contact.Generation || !s.now().Before(attempt.expires) {
			return guest.ErrGuestUnauthorized
		}
		auth, err = loadBuiltImageAuthority(ctx, tx, contact, attempt.operation, attempt.dispatch, true)
		if err != nil {
			return err
		}
		if !attempt.matches(auth) {
			return guest.ErrGuestUnauthorized
		}
		view, err := s.source.AuthorizationViewInTx(ctx, tx, auth.ownerID)
		if err != nil {
			return err
		}
		if view.State != "connected" || view.Version != attempt.sourceVersion {
			return guest.ErrGuestUnauthorized
		}
		return nil
	})
	return attempt, auth, err
}

func readBuiltImageAttempt(ctx context.Context, tx *sql.Tx, id string) (builtImageAttempt, error) {
	var a builtImageAttempt
	var expiry string
	err := tx.QueryRowContext(ctx, `SELECT id,installation_id,project_id,workspace_id,workspace_generation,operation_id,dispatch_version,
		target_os,target_arch,repository_url,config_path,config_sha256,policy_generation,source_authorization_version,kind,state,
		expires_at,manifest_digest,storage_path,storage_generation FROM built_image_attempts WHERE id=$1`, id).Scan(
		&a.id, &a.installation, &a.project, &a.workspace, &a.workspaceGeneration, &a.operation, &a.dispatch, &a.os, &a.arch,
		&a.repository, &a.path, &a.config, &a.policyGeneration, &a.sourceVersion, &a.kind, &a.state, &expiry, &a.manifest,
		&a.storage.path, &a.storage.generation)
	if err != nil {
		return builtImageAttempt{}, err
	}
	a.expires, err = time.Parse(time.RFC3339Nano, expiry)
	return a, err
}

func (a builtImageAttempt) matches(auth builtImageAuthority) bool {
	repo, ok := identity.CanonicalGitHubRepository(auth.repositoryURL)
	return ok && a.installation == auth.installationID && a.project == auth.projectID && a.workspace == auth.workspaceID &&
		a.workspaceGeneration == auth.generation && a.operation == auth.operationID && a.dispatch == auth.dispatchVersion &&
		a.os == auth.targetOS && a.arch == auth.targetArch && a.repository == repo && a.path == auth.configPath &&
		a.policyGeneration == auth.policyGeneration && a.kind == auth.kind
}

func (a builtImageAttempt) scope() imagecache.BuiltScope {
	return imagecache.BuiltScope{Generation: a.policyGeneration, ID: a.id}
}

func (a builtImageAttempt) terminal() bool { return a.state == "promoted" || a.state == "discarded" }

func (a builtImageAttempt) writable() bool { return a.state == "admitted" || a.state == "receiving" }

func (a builtImageAuthority) platform() v1.Platform {
	return v1.Platform{OS: a.targetOS, Architecture: a.targetArch}
}

func readBuiltImageSlot(ctx context.Context, tx *sql.Tx, authority builtImageAuthority) (builtImageSlot, error) {
	var slot builtImageSlot
	err := tx.QueryRowContext(ctx, `SELECT repository_url,config_path,config_sha256,manifest_digest,policy_generation,
		storage_generation,storage_path,storage_scope FROM built_image_slots
		WHERE installation_id=$1 AND project_id=$2 AND target_os=$3 AND target_arch=$4`, authority.installationID,
		authority.projectID, authority.targetOS, authority.targetArch).Scan(&slot.repository, &slot.path, &slot.config,
		&slot.manifest, &slot.scope.Generation, &slot.storage.generation, &slot.storage.path, &slot.scope.ID)
	return slot, err
}

func (s builtImageSlot) matches(authority builtImageAuthority, config string, storage builtImageStorage) bool {
	return s.config == config && s.inScope(authority, storage)
}

func (s builtImageSlot) inScope(authority builtImageAuthority, storage builtImageStorage) bool {
	repo, ok := identity.CanonicalGitHubRepository(authority.repositoryURL)
	return ok && s.repository == repo && s.path == authority.configPath &&
		s.scope.Generation == authority.policyGeneration && s.storage == storage
}

func (s *builtImageService) reference(id, suffix string) string {
	return s.host + "/" + builtImageRepositoryPrefix + id + suffix
}

func builtImageChanged(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err == nil && rows != 1 {
		return guest.ErrGuestUnauthorized
	}
	return err
}

func builtImageDecline(err error) error {
	if errors.Is(err, guest.ErrGuestUnauthorized) || errors.Is(err, sql.ErrNoRows) ||
		errors.Is(err, imagecache.ErrBuiltImageUnavailable) || errors.Is(err, imagecache.ErrBuiltStorageFull) {
		return nil
	}
	return err
}

func builtImageTime(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000000000Z") }

func validBuiltImageHash(hash string) bool {
	return digest.Digest("sha256:"+hash).Validate() == nil
}
