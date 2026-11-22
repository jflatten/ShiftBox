package identity

import (
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"
)

const (
	originRepository   = "https://github.com/example/private.git"
	originAPIRepo      = "/repos/example/private"
	originCoverage     = "/user/installations/55/repositories"
	originConfigPath   = ".devcontainer/devcontainer.json"
	originConfigBytes  = "{\r\n  // café\r\n  \"image\": \"debian:stable\",\r\n}\r\n"
	originMainCommit   = "0123456789abcdef0123456789abcdef01234567"
	originBranchCommit = "1123456789abcdef0123456789abcdef01234567"
	originTagCommit    = "2123456789abcdef0123456789abcdef01234567"
	originTagObject    = "3123456789abcdef0123456789abcdef01234567"
	originRootTree     = "4123456789abcdef0123456789abcdef01234567"
	originConfigTree   = "5123456789abcdef0123456789abcdef01234567"
)

// TestGitHubOriginAuthorizationChecksCoverageOnly verifies launch lookup can
// obtain the current owner authorization version without resolving a ref.
func TestGitHubOriginAuthorizationChecksCoverageOnly(t *testing.T) {
	app := connectedOriginFixture(t)
	installOriginReplies(t, app, "access-origin", originCovered())
	version, err := app.AuthorizeProjectSource(t.Context(), "owner", "https://GitHub.com/Example/Private")
	if err != nil || version != 1 {
		t.Errorf("AuthorizeProjectSource(covered repository) = %d, %v, want version 1", version, err)
	}
}

// TestGitHubOriginReadsExactBytes verifies the exact blob bytes are hashed
// after GitHub refreshes the requesting owner's expired token. It also checks
// that the authorization version belongs to the token used for every read.
func TestGitHubOriginReadsExactBytes(t *testing.T) {
	app, _, now := gitHubBrowserFixture(t)
	initial := githubTokenFixture(now, "expired")
	initial.AccessExpiresAt = now.Add(-time.Minute)
	if err := app.service.store.connect(t.Context(), "owner", "42", initial, now); err != nil {
		t.Fatal(err)
	}
	requests := append([]originHTTPReply{
		{method: http.MethodPost, host: "github.com", path: "/login/oauth/access_token", body: `{"access_token":"access-renewed","refresh_token":"refresh-renewed","expires_in":3600,"refresh_token_expires_in":86400,"token_type":"bearer"}`},
		originCovered(),
	}, originFileReplies(originMainCommit, originConfigBytes)...)
	installOriginReplies(t, app, "access-renewed", requests...)
	got, err := app.ReadProjectConfig(t.Context(), "owner", "https://GitHub.com/Example/Private", originMainCommit, originConfigPath)
	want := GitHubOriginConfig{Repository: originRepository, Revision: originMainCommit,
		ConfigSHA256: fmt.Sprintf("%x", sha256.Sum256([]byte(originConfigBytes))), AuthorizationVersion: 3}
	if err != nil || got != want {
		t.Fatalf("ReadProjectConfig(current owner, committed CRLF file) = %+v, %v, want %+v", got, err, want)
	}
}

// TestGitHubOriginResolvesGuestRefs exercises the same resolution order as
// the guest checkout. Exact branch matches win over tags, explicit namespaces
// do not fall through, and annotated tags must resolve to a commit.
func TestGitHubOriginResolvesGuestRefs(t *testing.T) {
	app := connectedOriginFixture(t)
	for _, test := range []struct {
		name    string
		ref     string
		commit  string
		resolve []originHTTPReply
	}{
		{name: "default branch", commit: originMainCommit, resolve: []originHTTPReply{
			{path: originAPIRepo, body: `{"full_name":"Example/Private","default_branch":"trunk"}`},
			originRef("heads/trunk", "commit", originMainCommit),
		}},
		{name: "branch wins over same-named tag", ref: "release/stable", commit: originBranchCommit, resolve: []originHTTPReply{
			originRef("heads/release/stable", "commit", originBranchCommit),
		}},
		{name: "fallback to lightweight tag", ref: "v1", commit: originTagCommit, resolve: []originHTTPReply{
			{path: originAPIRepo + "/git/ref/heads/v1", status: http.StatusNotFound},
			originRef("tags/v1", "commit", originTagCommit),
		}},
		{name: "explicit branch", ref: "refs/heads/release/stable", commit: originBranchCommit, resolve: []originHTTPReply{
			originRef("heads/release/stable", "commit", originBranchCommit),
		}},
		{name: "explicit annotated tag", ref: "refs/tags/release/stable", commit: originTagCommit, resolve: []originHTTPReply{
			originRef("tags/release/stable", "tag", originTagObject),
			{path: originAPIRepo + "/git/tags/" + originTagObject, body: fmt.Sprintf(`{"sha":%q,"object":{"type":"commit","sha":%q}}`, originTagObject, originTagCommit)},
		}},
		{name: "full commit", ref: originMainCommit, commit: originMainCommit},
	} {
		t.Run(test.name, func(t *testing.T) {
			requests := append([]originHTTPReply{originCovered()}, test.resolve...)
			requests = append(requests, originFileReplies(test.commit, originConfigBytes)...)
			installOriginReplies(t, app, "access-origin", requests...)
			got, err := app.ReadProjectConfig(t.Context(), "owner", originRepository, test.ref, originConfigPath)
			if err != nil || got.Revision != test.commit || got.AuthorizationVersion != 1 || got.Repository != originRepository {
				t.Errorf("ReadProjectConfig(ref %q) = %+v, %v, want commit %s with authorization version 1", test.ref, got, err, test.commit)
			}
		})
	}
}

// TestGitHubOriginDeclinesUnverifiedSource checks that denied coverage, an
// unavailable branch, ambiguous Git objects, unsafe paths and unreadable blobs
// cannot publish an apparently matching configuration.
func TestGitHubOriginDeclinesUnverifiedSource(t *testing.T) {
	app := connectedOriginFixture(t)
	commitRead := originFileReplies(originMainCommit, originConfigBytes)
	throughTrees := append([]originHTTPReply{originCovered()}, commitRead[:3]...)
	withBlob := func(reply originHTTPReply) []originHTTPReply {
		reply.path = commitRead[3].path
		return append(slices.Clone(throughTrees), reply)
	}
	for _, test := range []struct {
		name       string
		owner      string
		repository string
		ref        string
		path       string
		requests   []originHTTPReply
		wantErr    error
	}{
		{name: "no owner credential", owner: "different-owner", ref: originMainCommit, wantErr: ErrGitHubReconnect},
		{name: "repository outside installation", ref: originMainCommit, wantErr: ErrGitHubRepositoryDenied, requests: []originHTTPReply{
			{path: originCoverage, body: `{"total_count":1,"repositories":[{"full_name":"example/not-covered"}]}`},
		}},
		{name: "non-GitHub source", repository: "https://attacker.example/example/private.git", ref: originMainCommit},
		{name: "escaping config path", ref: originMainCommit, path: ".devcontainer/../devcontainer.json"},
		{name: "missing explicit branch", ref: "refs/heads/v1", requests: []originHTTPReply{
			originCovered(), {path: originAPIRepo + "/git/ref/heads/v1", status: http.StatusNotFound},
		}},
		{name: "unavailable branch is not a tag miss", ref: "v1", requests: []originHTTPReply{
			originCovered(), {path: originAPIRepo + "/git/ref/heads/v1", status: http.StatusServiceUnavailable},
		}},
		{name: "mismatched branch name", ref: "v1", requests: []originHTTPReply{
			originCovered(), {path: originAPIRepo + "/git/ref/heads/v1", body: fmt.Sprintf(`{"ref":"refs/heads/v10","object":{"type":"commit","sha":%q}}`, originMainCommit)},
		}},
		{name: "mismatched commit", ref: originMainCommit, requests: []originHTTPReply{
			originCovered(), {path: commitRead[0].path, body: fmt.Sprintf(`{"sha":%q,"tree":{"sha":%q}}`, originTagCommit, originRootTree)},
		}},
		{name: "symlink at selected path", ref: originMainCommit, requests: []originHTTPReply{
			originCovered(), commitRead[0], commitRead[1],
			{path: commitRead[2].path, body: fmt.Sprintf(`{"sha":%q,"truncated":false,"tree":[{"path":"devcontainer.json","type":"blob","mode":"120000","sha":%q,"size":12}]}`, originConfigTree, originBlobID(originConfigBytes))},
		}},
		{name: "overflowing config blob", ref: originMainCommit, requests: withBlob(originHTTPReply{
			body: fmt.Sprintf(`{"size":8388608,"encoding":"base64","content":"","sha":%q}`, originBlobID(originConfigBytes)),
		})},
		{name: "malformed response body", ref: originMainCommit, requests: withBlob(originHTTPReply{
			body: `{"encoding":"base64","content":"not terminated`,
		})},
		{name: "off-site blob redirect", ref: originMainCommit, requests: withBlob(originHTTPReply{
			status: http.StatusFound, location: "https://attacker.example/private-file",
		})},
	} {
		t.Run(test.name, func(t *testing.T) {
			owner, repository, configPath := test.owner, test.repository, test.path
			if owner == "" {
				owner = "owner"
			}
			if repository == "" {
				repository = originRepository
			}
			if configPath == "" {
				configPath = originConfigPath
			}
			installOriginReplies(t, app, "access-origin", test.requests...)
			got, err := app.ReadProjectConfig(t.Context(), owner, repository, test.ref, configPath)
			if err == nil || test.wantErr != nil && !errors.Is(err, test.wantErr) || got != (GitHubOriginConfig{}) {
				t.Errorf("ReadProjectConfig(%q) = %+v, %v, want no evidence and a decline (%v)", test.name, got, err, test.wantErr)
			}
		})
	}
}

// TestGitHubOriginDiscardsRevokedRead checks that a protected config response
// arriving after the owner disconnects cannot return cache evidence.
func TestGitHubOriginDiscardsRevokedRead(t *testing.T) {
	app := connectedOriginFixture(t)
	requests := append([]originHTTPReply{originCovered()}, originFileReplies(originMainCommit, originConfigBytes)...)
	requests[len(requests)-1].during = func() {
		if err := app.RevokeAuthorization(t.Context(), "owner"); err != nil {
			t.Errorf("revoking during file response: %v", err)
		}
	}
	installOriginReplies(t, app, "access-origin", requests...)
	got, err := app.ReadProjectConfig(t.Context(), "owner", originRepository, originMainCommit, originConfigPath)
	if !errors.Is(err, ErrGitHubReconnect) || got != (GitHubOriginConfig{}) {
		t.Errorf("ReadProjectConfig(revoked while reading) = %+v, %v, want a reconnect error with no evidence", got, err)
	}
}

type originHTTPReply struct {
	method, host, path, body, location string
	status                             int
	during                             func()
}

func connectedOriginFixture(t *testing.T) *GitHub {
	t.Helper()
	app, _, now := gitHubBrowserFixture(t)
	if err := app.service.store.connect(t.Context(), "owner", "42", githubTokenFixture(now, "origin"), now); err != nil {
		t.Fatal(err)
	}
	return app
}

func installOriginReplies(t *testing.T, app *GitHub, token string, replies ...originHTTPReply) {
	t.Helper()
	var requests int
	t.Cleanup(func() {
		if requests != len(replies) {
			t.Errorf("GitHub requests = %d, want %d", requests, len(replies))
		}
	})
	app.http.client.Transport = githubRoundTrip(func(r *http.Request) (*http.Response, error) {
		index := requests
		requests++
		if index >= len(replies) {
			return nil, fmt.Errorf("unexpected request %s %s", r.Method, r.URL)
		}
		reply := replies[index]
		method, host := reply.method, reply.host
		if method == "" {
			method = http.MethodGet
		}
		if host == "" {
			host = "api.github.com"
		}
		if r.Method != method || r.URL.Scheme != "https" || r.URL.Host != host || r.URL.Path != reply.path {
			t.Errorf("GitHub request %d = %s %s, want %s https://%s%s", index+1, r.Method, r.URL, method, host, reply.path)
			return nil, errors.New("unexpected GitHub request")
		}
		if method == http.MethodGet && r.Header.Get("Authorization") != "Bearer "+token {
			t.Error("origin GET did not use the current owner's token")
		}
		if method == http.MethodPost && (r.ParseForm() != nil || r.PostForm.Get("refresh_token") != "refresh-expired") {
			t.Error("token exchange did not use the connected owner's refresh credential")
		}
		if reply.during != nil {
			reply.during()
		}
		resp := githubResponse(reply.body)
		if reply.status != 0 {
			resp.StatusCode = reply.status
		}
		if reply.location != "" {
			resp.Header = http.Header{"Location": {reply.location}}
		}
		return resp, nil
	})
}

func originCovered() originHTTPReply {
	return originHTTPReply{path: originCoverage, body: `{"total_count":1,"repositories":[{"full_name":"Example/Private"}]}`}
}

func originRef(ref, kind, sha string) originHTTPReply {
	return originHTTPReply{path: originAPIRepo + "/git/ref/" + ref,
		body: fmt.Sprintf(`{"ref":%q,"object":{"type":%q,"sha":%q}}`, "refs/"+ref, kind, sha)}
}

func originFileReplies(commit string, raw string) []originHTTPReply {
	blobID := originBlobID(raw)
	encoded := base64.StdEncoding.EncodeToString([]byte(raw))
	// GitHub inserts line breaks in base64 response strings.
	encoded = strings.Join([]string{encoded[:16], encoded[16:]}, "\n") + "\n"
	return []originHTTPReply{
		{path: originAPIRepo + "/git/commits/" + commit, body: fmt.Sprintf(`{"sha":%q,"tree":{"sha":%q}}`, commit, originRootTree)},
		{path: originAPIRepo + "/git/trees/" + originRootTree, body: fmt.Sprintf(`{"sha":%q,"truncated":false,"tree":[{"path":".devcontainer","type":"tree","mode":"040000","sha":%q}]}`, originRootTree, originConfigTree)},
		{path: originAPIRepo + "/git/trees/" + originConfigTree, body: fmt.Sprintf(`{"sha":%q,"truncated":false,"tree":[{"path":"devcontainer.json","type":"blob","mode":"100644","sha":%q,"size":%d}]}`, originConfigTree, blobID, len(raw))},
		{path: originAPIRepo + "/git/blobs/" + blobID, body: fmt.Sprintf(`{"sha":%q,"encoding":"base64","size":%d,"content":%q}`, blobID, len(raw), encoded)},
	}
}

func originBlobID(raw string) string {
	return fmt.Sprintf("%x", sha1.Sum(fmt.Appendf(nil, "blob %d\x00%s", len(raw), raw)))
}
