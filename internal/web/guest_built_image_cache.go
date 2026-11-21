package web

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jflatten/ShiftBox/internal/guest"
	"github.com/jflatten/ShiftBox/internal/imagecache"
	"github.com/opencontainers/go-digest"
)

// BuiltImageAPIPath is available only on the authenticated guest listener.
const BuiltImageAPIPath = "/internal/v1/built-images"

// BuiltImageRegistryPath reserves the direct registry namespace for built images.
const BuiltImageRegistryPath = "/v2/shiftbox-built/"

// ErrBuiltImagePromotionPending tells the guest to retry finish while its
// explicit rebuild is still being verified by the controller.
var ErrBuiltImagePromotionPending = errors.New("built-image promotion is pending")

// BuiltImagePublication identifies a bounded, private upload attempt.
type BuiltImagePublication struct {
	AttemptID string    `json:"attemptId"`
	Image     string    `json:"image"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// BuiltImageTransfer gives the HTTP adapter one authorized read or write scope.
type BuiltImageTransfer struct {
	Cache    *imagecache.Cache
	Scope    imagecache.BuiltScope
	Lease    *imagecache.BuiltImageLease
	Manifest digest.Digest
	Close    func()
}

// GuestBuiltImageOperations binds HTTP negotiation to controller authority.
type GuestBuiltImageOperations struct {
	Authenticate func(context.Context, *x509.Certificate, time.Time) (guest.Contact, error)
	Lookup       func(context.Context, guest.Contact, string, int64, string) (string, error)
	Begin        func(context.Context, guest.Contact, string, int64, string) (BuiltImagePublication, error)
	Finish       func(context.Context, guest.Contact, string) (string, error)
	Transfer     func(context.Context, guest.Contact, string) (context.Context, BuiltImageTransfer, error)
}

// GuestBuiltImageCacheHandler serves only the bounded negotiation and OCI
// methods used by the pinned Docker client.
func GuestBuiltImageCacheHandler(operations GuestBuiltImageOperations, now func() time.Time) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.TLS == nil || r.TLS.Version < tls.VersionTLS13 || len(r.TLS.VerifiedChains) == 0 || len(r.TLS.VerifiedChains[0]) == 0 {
			http.Error(w, "Guest enrollment is required", http.StatusForbidden)
			return
		}
		leaf := r.TLS.VerifiedChains[0][0]
		contact, err := operations.Authenticate(r.Context(), leaf, now())
		if err != nil || contact.DesiredState != "running" {
			http.Error(w, "Guest enrollment is required", http.StatusForbidden)
			return
		}
		if r.URL.EscapedPath() != r.URL.Path {
			http.NotFound(w, r)
			return
		}
		if path, ok := strings.CutPrefix(r.URL.Path, BuiltImageRegistryPath); ok {
			serveBuiltImageRegistry(w, r, contact, leaf.NotAfter, path, operations)
			return
		}
		serveBuiltImageNegotiation(w, r, contact, operations)
	})
}

func serveBuiltImageNegotiation(w http.ResponseWriter, r *http.Request, contact guest.Contact, operations GuestBuiltImageOperations) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if r.URL.RawQuery != "" || r.URL.ForceQuery {
		http.NotFound(w, r)
		return
	}
	ctx := r.Context()
	switch r.URL.Path {
	case BuiltImageAPIPath + "/lookup", BuiltImageAPIPath + "/publications":
		var input struct {
			OperationID     string `json:"operationId"`
			DispatchVersion int64  `json:"dispatchVersion"`
			ConfigSHA256    string `json:"configSha256"`
		}
		err := DecodeJSON(w, r, &input, 1024)
		if err != nil || input.OperationID == "" || len(input.OperationID) > 128 ||
			input.DispatchVersion <= 0 || !validBuiltDigest("sha256:"+input.ConfigSHA256) {
			http.Error(w, "Invalid built-image request", http.StatusBadRequest)
			return
		}
		if r.URL.Path == BuiltImageAPIPath+"/lookup" {
			image, err := operations.Lookup(ctx, contact, input.OperationID, input.DispatchVersion, input.ConfigSHA256)
			if err != nil || image == "" {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			writeBuiltJSON(w, http.StatusOK, struct {
				Image string `json:"image"`
			}{image})
			return
		}
		publication, err := operations.Begin(ctx, contact, input.OperationID, input.DispatchVersion, input.ConfigSHA256)
		if err != nil || publication.AttemptID == "" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		writeBuiltJSON(w, http.StatusCreated, publication)
	default:
		attempt, ok := strings.CutPrefix(r.URL.Path, BuiltImageAPIPath+"/publications/")
		id, finish := strings.CutSuffix(attempt, "/finish")
		if !ok || !finish || !validBuiltScope(id) {
			http.NotFound(w, r)
			return
		}
		if r.ContentLength != 0 {
			http.Error(w, "Publication finish requires an empty body", http.StatusBadRequest)
			return
		}
		disposition, err := operations.Finish(ctx, contact, id)
		switch {
		case errors.Is(err, guest.ErrGuestUnauthorized):
			w.WriteHeader(http.StatusForbidden)
		case errors.Is(err, ErrBuiltImagePromotionPending):
			w.WriteHeader(http.StatusConflict)
		case err != nil:
			w.WriteHeader(http.StatusServiceUnavailable)
		default:
			writeBuiltJSON(w, http.StatusOK, struct {
				Disposition string `json:"disposition"`
			}{disposition})
		}
	}
}

func writeBuiltJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value) // A disconnected client cannot receive a second response.
}

func validBuiltScope(id string) bool {
	parsed, err := uuid.Parse(id)
	return err == nil && parsed != uuid.Nil && parsed.String() == id
}

func validBuiltDigest(value string) bool {
	id := digest.Digest(value)
	return id.Validate() == nil && id.Algorithm() == digest.SHA256
}

func serveBuiltImageRegistry(w http.ResponseWriter, r *http.Request, contact guest.Contact, certificateExpiry time.Time, path string, operations GuestBuiltImageOperations) {
	w.Header().Set("Docker-Distribution-API-Version", "registry/2.0")
	scope, object, ok := strings.Cut(path, "/")
	if !ok || !validBuiltScope(scope) {
		http.NotFound(w, r)
		return
	}
	ctx, cancel := context.WithDeadline(r.Context(), certificateExpiry)
	defer cancel()
	transferCtx, transfer, err := operations.Transfer(ctx, contact, scope)
	if err != nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	defer transfer.Close()
	releaseDeadlines, err := builtImageDeadlines(w, transferCtx)
	if err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	defer releaseDeadlines()
	r = r.WithContext(transferCtx)
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || query.Has("ns") {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if transfer.Lease != nil {
		serveBuiltImageObject(w, r, transfer, object, query)
		return
	}
	serveBuiltImageUpload(w, r, transfer, scope, object, query)
}

// The short poll connection defaults are unsuitable for a large layer. Both
// sides of each transfer remain bounded by the grant and the current leaf.
func builtImageDeadlines(w http.ResponseWriter, ctx context.Context) (func(), error) {
	deadline, ok := ctx.Deadline()
	if !ok {
		return nil, errors.New("missing built-image transfer deadline")
	}
	controller := http.NewResponseController(w)
	if err := controller.SetReadDeadline(deadline); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return nil, err
	}
	if err := controller.SetWriteDeadline(deadline); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return nil, err
	}
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		_ = controller.SetReadDeadline(time.Now())
		_ = controller.SetWriteDeadline(time.Now())
		close(done)
	})
	return func() {
		if !stop() {
			<-done
		}
	}, nil
}

func serveBuiltImageObject(w http.ResponseWriter, r *http.Request, transfer BuiltImageTransfer, object string, query url.Values) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	kind, ref, ok := strings.Cut(object, "/")
	if !ok || len(query) != 0 || !validBuiltDigest(ref) ||
		kind == "manifests" && ref != transfer.Manifest.String() ||
		kind == "blobs" && ref == transfer.Manifest.String() ||
		kind != "manifests" && kind != "blobs" {
		http.NotFound(w, r)
		return
	}
	reader, desc, err := transfer.Lease.Open(digest.Digest(ref))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer reader.Close()
	w.Header().Set("Docker-Content-Digest", desc.Digest.String())
	w.Header().Set("Content-Length", strconv.FormatInt(desc.Size, 10))
	w.Header().Set("Content-Type", desc.MediaType)
	http.ServeContent(w, r, ref, time.Time{}, reader)
}

func serveBuiltImageUpload(w http.ResponseWriter, r *http.Request, transfer BuiltImageTransfer, scope, object string, query url.Values) {
	root := BuiltImageRegistryPath + scope
	// The upload location is scoped to this admitted attempt. The pinned
	// client commits each monolithic blob with its digest in the query.
	location := root + "/blobs/uploads/" + scope
	switch {
	case r.Method == http.MethodPost && object == "blobs/uploads/" && r.ContentLength == 0:
		// A cross-repository mount falls back to a regular upload. A shared
		// physical blob can only be linked after these bytes are verified.
		w.Header().Set("Location", location)
		w.Header().Set("Docker-Upload-UUID", scope)
		w.WriteHeader(http.StatusAccepted)
	case r.Method == http.MethodPut && object == "blobs/uploads/"+scope:
		values := query["digest"]
		if len(query) != 1 || len(values) != 1 || !validBuiltDigest(values[0]) || r.ContentLength <= 0 {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		id := digest.Digest(values[0])
		err := transfer.Cache.PutBuiltBlob(r.Context(), transfer.Scope, id, r.ContentLength, r.Body)
		if err != nil {
			writeBuiltUploadError(w, err)
			return
		}
		w.Header().Set("Location", root+"/blobs/"+id.String())
		w.Header().Set("Docker-Content-Digest", id.String())
		w.WriteHeader(http.StatusCreated)
	case r.Method == http.MethodHead && len(query) == 0 && strings.HasPrefix(object, "blobs/"):
		ref := strings.TrimPrefix(object, "blobs/")
		if !validBuiltDigest(ref) {
			http.NotFound(w, r)
			return
		}
		id := digest.Digest(ref)
		size, found := transfer.Cache.BuiltBlobSize(transfer.Scope, id)
		if !found {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Docker-Content-Digest", id.String())
		w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
		w.WriteHeader(http.StatusOK)
	case r.Method == http.MethodHead && object == "manifests/candidate" && len(query) == 0:
		id, found := transfer.Cache.BuiltCandidate(transfer.Scope)
		if !found {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Docker-Content-Digest", id.String())
		w.WriteHeader(http.StatusOK)
	case r.Method == http.MethodPut && object == "manifests/candidate" && len(query) == 0:
		id, err := transfer.Cache.PutBuiltManifest(r.Context(), transfer.Scope, r.Header.Get("Content-Type"), r.ContentLength, r.Body)
		if err != nil {
			writeBuiltUploadError(w, err)
			return
		}
		w.Header().Set("Location", root+"/manifests/"+id.String())
		w.Header().Set("Docker-Content-Digest", id.String())
		w.WriteHeader(http.StatusCreated)
	default:
		http.NotFound(w, r)
	}
}

func writeBuiltUploadError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, imagecache.ErrBuiltInvalid), errors.Is(err, io.ErrUnexpectedEOF):
		w.WriteHeader(http.StatusBadRequest)
	case errors.Is(err, imagecache.ErrBuiltStorageFull):
		w.WriteHeader(http.StatusInsufficientStorage)
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		w.WriteHeader(http.StatusRequestTimeout)
	default:
		w.WriteHeader(http.StatusServiceUnavailable)
	}
}
