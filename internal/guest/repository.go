package guest

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/jflatten/ShiftBox/internal/template"
)

var gitObjectID = regexp.MustCompile(`^[a-f0-9]{40}$`)

type repositorySource struct {
	URL    string `json:"url"`
	Ref    string `json:"ref"`
	Config string `json:"config"`
}

type repositoryEvidence struct {
	repositorySource
	Path   string `json:"path"`
	Commit string `json:"commit"`
}

type repositoryPreparation struct {
	project string
	token   func(context.Context) (string, error)
	run     func(context.Context, string, string, ...string) ([]byte, error)
}

func (r repositoryPreparation) prepare(ctx context.Context, source repositorySource) (repositoryEvidence, error) {
	if !filepath.IsAbs(r.project) || !template.ValidSource(source.URL, source.Config) || !validDevContainerPath(source.Config) ||
		len(source.URL) > 2048 || len(source.Config) > 2048 || !template.ValidRepositoryRef(source.Ref) {
		return repositoryEvidence{}, errors.New("invalid repository preparation input")
	}
	if _, err := os.Lstat(r.project); err == nil {
		return r.prepared(source)
	} else if !errors.Is(err, os.ErrNotExist) {
		return repositoryEvidence{}, err
	}
	if r.token == nil {
		return repositoryEvidence{}, errors.New("owner Git authorization is unavailable")
	}
	token, err := r.token(ctx)
	if err != nil || !ValidGitAccessToken(token) {
		if ctx.Err() != nil {
			return repositoryEvidence{}, ctx.Err()
		}
		return repositoryEvidence{}, errors.New("owner Git authorization is unavailable")
	}
	// Publish the checkout and its receipt together. An interrupted clone can
	// never be mistaken for a retained checkout, and retries do not reset work.
	parent := filepath.Dir(r.project)
	staging, err := os.MkdirTemp(parent, ".repository-")
	if err != nil {
		return repositoryEvidence{}, err
	}
	defer os.RemoveAll(staging)
	if _, err := r.git(ctx, staging, token, "clone", "--no-checkout", "--no-recurse-submodules", "--", source.URL, "."); err != nil {
		return repositoryEvidence{}, err
	}
	if err := r.checkout(ctx, staging, token, source.Ref); err != nil {
		return repositoryEvidence{}, err
	}
	id, err := r.git(ctx, staging, "", "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return repositoryEvidence{}, err
	}
	commit := strings.TrimSpace(string(id))
	if !gitObjectID.MatchString(commit) {
		return repositoryEvidence{}, errors.New("Git did not identify the prepared commit")
	}
	evidence := repositoryEvidence{repositorySource: source, Path: r.project, Commit: commit}
	data, err := json.Marshal(evidence)
	if err != nil {
		return repositoryEvidence{}, err
	}
	if err := writePrivateState(repositoryReceipt(staging), data); err != nil {
		return repositoryEvidence{}, err
	}
	if _, err := os.Lstat(r.project); !errors.Is(err, os.ErrNotExist) {
		return repositoryEvidence{}, errors.New("retained project appeared during checkout")
	}
	if err := os.Rename(staging, r.project); err != nil {
		return repositoryEvidence{}, err
	}
	dir, err := os.Open(parent)
	if err != nil {
		return repositoryEvidence{}, err
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil {
		return repositoryEvidence{}, err
	}
	return evidence, nil
}

func (r repositoryPreparation) prepared(source repositorySource) (repositoryEvidence, error) {
	root, err := os.OpenRoot(r.project)
	if err != nil {
		return repositoryEvidence{}, errors.New("cannot open retained project")
	}
	defer root.Close()
	file, err := root.Open(repositoryReceipt("."))
	if err != nil {
		return repositoryEvidence{}, errors.New("retained project has no preparation receipt")
	}
	defer file.Close()
	var evidence repositoryEvidence
	if err := decodeAgentJSON(file, &evidence); err != nil || evidence.repositorySource != source ||
		evidence.Path != r.project || !gitObjectID.MatchString(evidence.Commit) {
		return repositoryEvidence{}, errors.New("retained project does not match the requested repository snapshot")
	}
	return evidence, nil
}

func repositoryReceipt(project string) string {
	return filepath.Join(project, ".git", "shiftbox-checkout.json")
}

func (r repositoryPreparation) checkout(ctx context.Context, dir, token, ref string) error {
	if ref == "" {
		_, err := r.git(ctx, dir, "", "reset", "--hard", "HEAD")
		return err
	}
	if gitObjectID.MatchString(ref) {
		if _, err := r.git(ctx, dir, "", "cat-file", "-e", ref+"^{commit}"); err != nil {
			if _, err := r.git(ctx, dir, token, "fetch", "--no-recurse-submodules", "--", "origin", ref); err != nil {
				return err
			}
		}
		_, err := r.git(ctx, dir, "", "switch", "--detach", "--", ref)
		return err
	}
	branch, explicitBranch := strings.CutPrefix(ref, "refs/heads/")
	tag, explicitTag := strings.CutPrefix(ref, "refs/tags/")
	tracking := "refs/remotes/origin/" + branch
	if !explicitTag {
		_, err := r.git(ctx, dir, "", "show-ref", "--verify", "--quiet", "--", tracking)
		if err == nil {
			_, err := r.git(ctx, dir, "", "switch", "--track", "-C", branch, "--", tracking)
			return err
		}
		if explicitBranch {
			return err
		}
	}
	_, err := r.git(ctx, dir, "", "switch", "--detach", "--", "refs/tags/"+tag)
	return err
}

func (r repositoryPreparation) git(ctx context.Context, dir, token string, args ...string) ([]byte, error) {
	run := r.run
	if run == nil {
		run = runRepositoryGit
	}
	output, err := run(ctx, dir, token, args...)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("repository Git " + args[0] + " failed")
	}
	return output, nil
}

func runRepositoryGit(ctx context.Context, dir, token string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "/usr/bin/git", args...)
	cmd.Dir = dir
	cmd.WaitDelay = 2 * time.Second
	cmd.Env = []string{
		"PATH=/usr/bin:/bin", "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_ALLOW_PROTOCOL=https",
		"GIT_CONFIG_KEY_0=http.followRedirects", "GIT_CONFIG_VALUE_0=false",
		"GIT_CONFIG_KEY_1=core.hooksPath", "GIT_CONFIG_VALUE_1=/dev/null",
	}
	if token != "" {
		basic := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + token))
		cmd.Env = append(cmd.Env, "GIT_CONFIG_COUNT=3", "GIT_CONFIG_KEY_2=http.https://github.com/.extraHeader",
			"GIT_CONFIG_VALUE_2=Authorization: Basic "+basic)
	} else {
		cmd.Env = append(cmd.Env, "GIT_CONFIG_COUNT=2")
	}
	var stdout devContainerOutput
	cmd.Stdout = &stdout
	err := cmd.Run()
	return stdout.buffer.Bytes(), err
}
