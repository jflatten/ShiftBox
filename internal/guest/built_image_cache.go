package guest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/opencontainers/go-digest"
)

// BuiltImageOfferHeader advertises support without changing the poll JSON.
const BuiltImageOfferHeader = "ShiftBox-Built-Images"

const builtImageAPIPath = "/internal/v1/built-images"

var errBuiltImagePublicationPending = errors.New("controller is waiting for the rebuild to finish")

type devContainerBuiltImage struct {
	ID           string `json:"builtImageID"`
	ConfigSHA256 string `json:"configSHA256"`
	Hit          bool   `json:"imageCacheHit"`
}

type builtImageGuest struct {
	lookup  func(context.Context, string) (string, error)
	publish func(context.Context, devContainerBuiltImage) error
}

type builtImageSelectionKey struct{}

type builtImageSelection struct {
	id, configSHA256 string
}

func builtImageSelectionFromContext(ctx context.Context) (string, string) {
	selection, _ := ctx.Value(builtImageSelectionKey{}).(builtImageSelection)
	return selection.id, selection.configSHA256
}

func (d devContainer) selectBuiltImage(ctx context.Context) context.Context {
	if d.builtImage == nil || d.builtImage.lookup == nil {
		return ctx
	}
	hash := d.builtImageConfigSHA256()
	if hash == "" {
		return ctx
	}
	id, err := d.builtImage.lookup(ctx, hash)
	if err != nil || !validLocalBuiltImage(id) {
		return ctx
	}
	return context.WithValue(ctx, builtImageSelectionKey{}, builtImageSelection{id: id, configSHA256: hash})
}

// builtImageConfigSHA256 is a preflight hint. The pinned CLI checks these
// bytes again at its own parser boundary before it can select the cached image.
func (d devContainer) builtImageConfigSHA256() string {
	root, err := os.OpenRoot(d.workspace)
	if err != nil {
		return ""
	}
	defer root.Close()
	path := d.config
	if path == "" {
		path = ".devcontainer/devcontainer.json"
		if _, err := root.Lstat(path); errors.Is(err, os.ErrNotExist) {
			path = ".devcontainer.json"
		}
	}
	if !validDevContainerPath(path) {
		return ""
	}
	file, err := root.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return ""
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > devContainerOutputMax {
		return ""
	}
	data, err := io.ReadAll(io.LimitReader(file, devContainerOutputMax+1))
	if err != nil || len(data) > devContainerOutputMax {
		return ""
	}
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

func (d devContainer) queueBuiltImage(ctx context.Context, candidate devContainerBuiltImage) {
	if d.builtImage == nil || d.builtImage.publish == nil || candidate.Hit ||
		!validLocalBuiltImage(candidate.ID) || !validLocalBuiltImage("sha256:"+candidate.ConfigSHA256) {
		return
	}
	// A full worker queue leaves the ordinary launch or rebuild intact.
	_ = d.builtImage.publish(ctx, candidate)
}

type builtImageGuestTransport struct {
	stateDir string
	endpoint string
	host     string
	client   *http.Client
	queue    chan builtImageUpload
}

type builtImageNegotiation struct {
	OperationID     string `json:"operationId"`
	DispatchVersion int64  `json:"dispatchVersion"`
	ConfigSHA256    string `json:"configSha256"`
}

type builtImageUpload struct {
	request builtImageNegotiation
	imageID string
}

type builtImagePublication struct {
	AttemptID string    `json:"attemptId"`
	Image     string    `json:"image"`
	ExpiresAt time.Time `json:"expiresAt"`
}

func newBuiltImageGuestTransport(stateDir, endpoint string, client *http.Client) *builtImageGuestTransport {
	if !validPollEndpoint(endpoint) {
		return nil
	}
	origin, err := url.Parse(endpoint)
	if err != nil {
		return nil
	}
	return &builtImageGuestTransport{
		stateDir: stateDir, endpoint: endpoint, host: origin.Host, client: client,
		// One pending build is enough for the serial workspace operation loop.
		queue: make(chan builtImageUpload, 1),
	}
}

func (g *builtImageGuestTransport) forDispatch(dispatch guestDispatch) *builtImageGuest {
	built := &builtImageGuest{
		publish: func(_ context.Context, image devContainerBuiltImage) error {
			candidate := builtImageUpload{builtImageNegotiation{dispatch.ID, dispatch.Version, image.ConfigSHA256}, image.ID}
			select {
			case g.queue <- candidate:
				return nil
			default:
				return errors.New("built-image publication queue is busy")
			}
		},
	}
	if dispatch.Command == ensureDevelopmentCommand {
		built.lookup = func(ctx context.Context, hash string) (string, error) {
			return g.lookup(ctx, builtImageNegotiation{dispatch.ID, dispatch.Version, hash})
		}
	}
	return built
}

func (g *builtImageGuestTransport) lookup(ctx context.Context, input builtImageNegotiation) (string, error) {
	var hit struct {
		Image string `json:"image"`
	}
	found, err := g.negotiate(ctx, "/lookup", input, http.StatusOK, &hit)
	if err != nil || !found {
		return "", err
	}
	if _, ok := g.referenceScope(hit.Image, true); !ok {
		return "", errors.New("invalid built-image pull reference")
	}
	if err := updateBuiltImageRegistry(g.stateDir, g.endpoint); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	platform := "linux/" + runtime.GOARCH
	if _, err := runBuiltImageDocker(ctx, "image", "pull", "--platform", platform, hit.Image); err != nil {
		return "", err
	}
	out, err := runBuiltImageDocker(ctx, "image", "inspect", "--format", "{{.Id}} {{.Os}}/{{.Architecture}}", hit.Image)
	if err != nil {
		return "", err
	}
	fields := strings.Fields(string(out))
	if len(fields) != 2 || !validLocalBuiltImage(fields[0]) || fields[1] != platform {
		return "", errors.New("built-image pull did not produce the expected platform")
	}
	return fields[0], nil
}

func (g *builtImageGuestTransport) runUploads(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case candidate := <-g.queue:
			g.publish(ctx, candidate)
		}
	}
}

func (g *builtImageGuestTransport) publish(ctx context.Context, candidate builtImageUpload) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	var publication builtImagePublication
	found, err := g.negotiate(ctx, "/publications", candidate.request, http.StatusCreated, &publication)
	if err != nil || !found || publication.ExpiresAt.IsZero() {
		return
	}
	scope, ok := g.referenceScope(publication.Image, false)
	if !ok || scope != publication.AttemptID {
		return
	}
	if err := updateBuiltImageRegistry(g.stateDir, g.endpoint); err != nil {
		return
	}
	uploadCtx, stop := context.WithDeadline(ctx, publication.ExpiresAt)
	defer stop()
	if _, err := runBuiltImageDocker(uploadCtx, "image", "tag", candidate.imageID, publication.Image); err != nil {
		return
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		// A short-lived staging tag has no workspace lifecycle dependency.
		_, _ = runBuiltImageDocker(cleanup, "image", "rm", publication.Image)
	}()
	var pushed bool
	for range 2 {
		if uploadCtx.Err() != nil {
			return
		}
		_, err := runBuiltImageDocker(uploadCtx, "image", "push", "--platform", "linux/"+runtime.GOARCH, publication.Image)
		if err == nil {
			pushed = true
			break
		}
	}
	if !pushed {
		return
	}
	g.finishPublication(uploadCtx, scope)
}

// finishPublication retries only while the controller is waiting for the
// rebuild outcome. The upload grant bounds the entire wait.
func (g *builtImageGuestTransport) finishPublication(ctx context.Context, scope string) {
	path := "/publications/" + scope + "/finish"
	for delay := time.Second; ctx.Err() == nil; delay = min(delay*2, 5*time.Second) {
		var result struct {
			Disposition string `json:"disposition"`
		}
		if _, err := g.negotiate(ctx, path, nil, http.StatusOK, &result); !errors.Is(err, errBuiltImagePublicationPending) {
			return
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (g *builtImageGuestTransport) negotiate(ctx context.Context, path string, data any, success int, output any) (bool, error) {
	var payload []byte
	if data != nil {
		var err error
		payload, err = json.Marshal(data)
		if err != nil {
			return false, err
		}
	}
	endpoint := guestDiagnosticEndpoint(g.endpoint, builtImageAPIPath+path)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return false, err
	}
	if data != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := g.client.Do(request)
	if err != nil {
		return false, err
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case http.StatusNoContent:
		return false, nil
	case http.StatusConflict:
		return false, errBuiltImagePublicationPending
	case success:
		if err := decodeAgentJSON(response.Body, output); err != nil {
			return false, err
		}
		return true, nil
	default:
		return false, errors.New("controller declined the built-image request")
	}
}

func (g *builtImageGuestTransport) referenceScope(value string, byDigest bool) (string, bool) {
	path, ok := strings.CutPrefix(value, g.host+"/shiftbox-built/")
	if !ok {
		return "", false
	}
	scope, found := strings.CutSuffix(path, ":candidate")
	if byDigest {
		var ref string
		scope, ref, found = strings.Cut(path, "@")
		if !validLocalBuiltImage(ref) {
			return "", false
		}
	}
	id, err := uuid.Parse(scope)
	return scope, found && err == nil && id != uuid.Nil && id.String() == scope
}

func validLocalBuiltImage(value string) bool {
	id := digest.Digest(value)
	return id.Validate() == nil && id.Algorithm() == digest.SHA256
}

func runBuiltImageDocker(ctx context.Context, arguments ...string) ([]byte, error) {
	return runDevContainerCLI(ctx, dockerExecutable, append([]string{"--host=unix:///var/run/docker.sock"}, arguments...)...)
}
