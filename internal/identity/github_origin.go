package identity

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/jflatten/ShiftBox/internal/template"
)

const (
	githubRepositoryPrefix    = "https://github.com/"
	githubOriginConfigLimit   = 1 << 20
	githubOriginMetadataLimit = 1 << 20
	githubOriginTreeLimit     = 8 << 20
	githubOriginBlobJSONLimit = 2 << 20
	githubOriginTagDepth      = 8
)

var (
	githubRepositoryName = regexp.MustCompile(`^[a-z0-9_.-]+/[a-z0-9_.-]+$`)
	githubObjectID       = regexp.MustCompile(`^[0-9a-f]{40}$`)
	errOriginUnavailable = errors.New("GitHub origin configuration is unavailable")
	errOriginUnsafe      = errors.New("GitHub origin configuration could not be verified")
)

// GitHubOriginConfig is a verified observation of the project's configured
// source. The authorization version belongs to the owner token used for the read.
type GitHubOriginConfig struct {
	Repository           string
	Revision             string
	ConfigSHA256         string
	AuthorizationVersion int64
}

type githubOriginReader struct {
	http       githubHTTP
	token      string
	repository string
}

type githubOriginObject struct {
	Type string `json:"type"`
	SHA  string `json:"sha"`
}

type githubOriginTreeEntry struct {
	githubOriginObject
	Path string `json:"path"`
	Mode string `json:"mode"`
	Size int64  `json:"size"`
}

// CanonicalGitHubRepository returns a consistent URL for an MVP GitHub source.
func CanonicalGitHubRepository(repository string) (string, bool) {
	if len(repository) > 2048 || !template.ValidSource(repository, "devcontainer.json") {
		return "", false
	}
	repo, err := url.Parse(repository)
	if err != nil {
		return "", false
	}
	name := strings.TrimSuffix(strings.ToLower(strings.Trim(repo.Path, "/")), ".git")
	if !githubRepositoryName.MatchString(name) || !fs.ValidPath(name) {
		return "", false
	}
	return githubRepositoryPrefix + name + ".git", true
}

// AuthorizeProjectSource verifies live owner coverage for the repository and
// returns the version of the authorization that permitted access.
func (g *GitHub) AuthorizeProjectSource(ctx context.Context, owner, repository string) (int64, error) {
	g = g.Active()
	if g == nil {
		return 0, ErrGitHubReconnect
	}
	canonical, ok := CanonicalGitHubRepository(repository)
	if !ok {
		return 0, ErrGitHubRepositoryDenied
	}
	ctx, cancel := context.WithTimeout(ctx, 2*githubRefreshTimeout)
	defer cancel()
	auth, err := g.service.repositoryAuthorization(ctx, owner, canonical)
	if err != nil {
		return 0, err
	}
	return auth.Version, nil
}

// ReadProjectConfig hashes the exact config blob at the current configured
// ref using the workspace owner's live GitHub authorization.
func (g *GitHub) ReadProjectConfig(ctx context.Context, owner, repository, ref, configPath string) (GitHubOriginConfig, error) {
	g = g.Active()
	if g == nil {
		return GitHubOriginConfig{}, ErrGitHubReconnect
	}
	canonical, ok := CanonicalGitHubRepository(repository)
	if !ok {
		return GitHubOriginConfig{}, ErrGitHubRepositoryDenied
	}
	if len(configPath) > 2048 || !template.ValidSource(canonical, configPath) || !template.ValidRepositoryRef(ref) {
		return GitHubOriginConfig{}, errOriginUnsafe
	}
	ctx, cancel := context.WithTimeout(ctx, 2*githubRefreshTimeout)
	defer cancel()
	auth, err := g.service.repositoryAuthorization(ctx, owner, canonical)
	if err != nil {
		return GitHubOriginConfig{}, err
	}
	name := strings.TrimSuffix(strings.TrimPrefix(canonical, githubRepositoryPrefix), ".git")
	reader := githubOriginReader{http: g.http, token: auth.Tokens.Access, repository: name}
	revision, digest, err := reader.readConfig(ctx, ref, configPath)
	if errors.Is(err, ErrGitHubReconnect) {
		return GitHubOriginConfig{}, errors.Join(err, g.service.store.requireReconnect(ctx, owner, auth.Version, g.service.now()))
	}
	if err != nil {
		return GitHubOriginConfig{}, err
	}
	if err := g.service.checkCurrentAuthorization(ctx, owner, auth); err != nil {
		return GitHubOriginConfig{}, err
	}
	return GitHubOriginConfig{Repository: canonical, Revision: revision, ConfigSHA256: digest, AuthorizationVersion: auth.Version}, nil
}

func (r githubOriginReader) readConfig(ctx context.Context, ref, configPath string) (string, string, error) {
	revision, err := r.resolveRevision(ctx, ref)
	if err != nil {
		return "", "", err
	}
	var commit struct {
		SHA  string             `json:"sha"`
		Tree githubOriginObject `json:"tree"`
	}
	if err := r.get(ctx, "/git/commits/"+revision, &commit, githubOriginMetadataLimit); err != nil {
		return "", "", err
	}
	if commit.SHA != revision || !githubObjectID.MatchString(commit.Tree.SHA) {
		return "", "", errOriginUnsafe
	}
	entry, err := r.configEntry(ctx, commit.Tree.SHA, configPath)
	if err != nil {
		return "", "", err
	}
	digest, err := r.configDigest(ctx, entry)
	if err != nil {
		return "", "", err
	}
	return revision, digest, nil
}

func (r githubOriginReader) resolveRevision(ctx context.Context, ref string) (string, error) {
	if githubObjectID.MatchString(ref) {
		return ref, nil
	}
	branch, explicitBranch := strings.CutPrefix(ref, "refs/heads/")
	tag, explicitTag := strings.CutPrefix(ref, "refs/tags/")
	if ref == "" {
		var repo struct {
			FullName      string `json:"full_name"`
			DefaultBranch string `json:"default_branch"`
		}
		if err := r.get(ctx, "", &repo, githubOriginMetadataLimit); err != nil {
			return "", err
		}
		if !strings.EqualFold(repo.FullName, r.repository) || !template.ValidRepositoryRef(repo.DefaultBranch) {
			return "", errOriginUnsafe
		}
		branch, explicitBranch = repo.DefaultBranch, true
	}
	if !explicitTag {
		object, found, err := r.reference(ctx, "heads", branch)
		if err != nil {
			return "", err
		}
		if found {
			if object.Type != "commit" {
				return "", errOriginUnsafe
			}
			return object.SHA, nil
		}
		if explicitBranch {
			return "", errOriginUnavailable
		}
	}
	object, found, err := r.reference(ctx, "tags", tag)
	if err != nil {
		return "", err
	}
	if !found {
		return "", errOriginUnavailable
	}
	return r.resolveTag(ctx, object)
}

func (r githubOriginReader) reference(ctx context.Context, namespace, name string) (githubOriginObject, bool, error) {
	if name == "." || !fs.ValidPath(name) {
		return githubOriginObject{}, false, errOriginUnsafe
	}
	parts := strings.Split(name, "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	var ref struct {
		Name   string             `json:"ref"`
		Object githubOriginObject `json:"object"`
	}
	status, err := r.http.get(ctx, r.token, "/repos/"+r.repository+"/git/ref/"+namespace+"/"+strings.Join(parts, "/"), &ref, githubOriginMetadataLimit)
	if err != nil {
		return githubOriginObject{}, false, err
	}
	if status == http.StatusNotFound {
		return githubOriginObject{}, false, nil
	}
	if err := originResponseStatus(status); err != nil {
		return githubOriginObject{}, false, err
	}
	if ref.Name != "refs/"+namespace+"/"+name || !githubObjectID.MatchString(ref.Object.SHA) {
		return githubOriginObject{}, false, errOriginUnsafe
	}
	return ref.Object, true, nil
}

func (r githubOriginReader) resolveTag(ctx context.Context, object githubOriginObject) (string, error) {
	for depth := 0; object.Type == "tag" && depth < githubOriginTagDepth; depth++ {
		var tag struct {
			SHA    string             `json:"sha"`
			Object githubOriginObject `json:"object"`
		}
		if err := r.get(ctx, "/git/tags/"+object.SHA, &tag, githubOriginMetadataLimit); err != nil {
			return "", err
		}
		if tag.SHA != object.SHA || !githubObjectID.MatchString(tag.Object.SHA) {
			return "", errOriginUnsafe
		}
		object = tag.Object
	}
	if object.Type != "commit" {
		return "", errOriginUnsafe
	}
	return object.SHA, nil
}

// configEntry walks immutable trees so a symlink or submodule cannot make
// the read attest bytes from a different path or repository.
func (r githubOriginReader) configEntry(ctx context.Context, treeSHA, configPath string) (githubOriginTreeEntry, error) {
	parts := strings.Split(configPath, "/")
	for i, name := range parts {
		entry, err := r.treeEntry(ctx, treeSHA, name)
		if err != nil {
			return githubOriginTreeEntry{}, err
		}
		if i == len(parts)-1 {
			if entry.Type != "blob" || entry.Mode != "100644" && entry.Mode != "100755" || entry.Size < 0 || entry.Size > githubOriginConfigLimit {
				return githubOriginTreeEntry{}, errOriginUnsafe
			}
			return entry, nil
		}
		if entry.Type != "tree" || entry.Mode != "040000" {
			return githubOriginTreeEntry{}, errOriginUnsafe
		}
		treeSHA = entry.SHA
	}
	return githubOriginTreeEntry{}, errOriginUnsafe
}

func (r githubOriginReader) treeEntry(ctx context.Context, treeSHA, name string) (githubOriginTreeEntry, error) {
	var tree struct {
		SHA       string                  `json:"sha"`
		Truncated bool                    `json:"truncated"`
		Entries   []githubOriginTreeEntry `json:"tree"`
	}
	if err := r.get(ctx, "/git/trees/"+treeSHA, &tree, githubOriginTreeLimit); err != nil {
		return githubOriginTreeEntry{}, err
	}
	if tree.Truncated || tree.SHA != treeSHA {
		return githubOriginTreeEntry{}, errOriginUnsafe
	}
	for _, entry := range tree.Entries {
		if entry.Path == name && githubObjectID.MatchString(entry.SHA) {
			return entry, nil
		}
	}
	return githubOriginTreeEntry{}, errOriginUnsafe
}

func (r githubOriginReader) configDigest(ctx context.Context, entry githubOriginTreeEntry) (string, error) {
	var blob struct {
		SHA      string `json:"sha"`
		Encoding string `json:"encoding"`
		Size     int64  `json:"size"`
		Content  []byte `json:"content"`
	}
	err := r.get(ctx, "/git/blobs/"+entry.SHA, &blob, githubOriginBlobJSONLimit)
	defer clear(blob.Content)
	if err != nil {
		return "", err
	}
	if blob.SHA != entry.SHA || blob.Encoding != "base64" || blob.Size != entry.Size || int64(len(blob.Content)) != entry.Size {
		return "", errOriginUnsafe
	}
	// Git identifies blobs by SHA-1 of their typed header and contents. The
	// cache uses a separate SHA-256 of the exact config bytes read by the CLI.
	object := sha1.New()
	// The hash writer accepts every byte and always returns a nil error.
	_, _ = fmt.Fprintf(object, "blob %d\x00", blob.Size)
	_, _ = object.Write(blob.Content)
	if fmt.Sprintf("%x", object.Sum(nil)) != entry.SHA {
		return "", errOriginUnsafe
	}
	return fmt.Sprintf("%x", sha256.Sum256(blob.Content)), nil
}

func (r githubOriginReader) get(ctx context.Context, path string, value any, limit int64) error {
	status, err := r.http.get(ctx, r.token, "/repos/"+r.repository+path, value, limit)
	if err != nil {
		return err
	}
	return originResponseStatus(status)
}

func originResponseStatus(status int) error {
	switch status {
	case http.StatusOK:
		return nil
	case http.StatusUnauthorized:
		return ErrGitHubReconnect
	default:
		return errOriginUnavailable
	}
}
