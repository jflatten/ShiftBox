package release

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// ErrInvalidArtifact indicates that a candidate cannot be admitted to the catalog.
var ErrInvalidArtifact = errors.New("invalid platform artifact")

// Artifact describes the checked manifest and the relative path to its UTM bundle.
type Artifact struct {
	Manifest       ArtifactManifest
	ManifestSHA256 string
	Bundle         string
}

// ArtifactManifest describes a versioned platform export.
type ArtifactManifest struct {
	SchemaVersion   int                `json:"schemaVersion"`
	ReleaseID       string             `json:"releaseId"`
	Architecture    string             `json:"architecture"`
	PlatformVersion string             `json:"platformVersion"`
	ProtocolVersion string             `json:"protocolVersion"`
	Guest           ArtifactComponent  `json:"guest"`
	IDE             ArtifactComponent  `json:"ide"`
	Provenance      ArtifactProvenance `json:"provenance"`
	Resources       []ArtifactResource `json:"resources"`
}

// ArtifactComponent records the version and digest of a bundled component.
type ArtifactComponent struct {
	Version string `json:"version"`
	SHA256  string `json:"sha256"`
	Source  string `json:"source,omitempty"`
}

// ArtifactProvenance records the source and build revision reviewed on import.
type ArtifactProvenance struct {
	Source   string `json:"source"`
	Revision string `json:"revision"`
}

// ArtifactResource ties an exported bundle file to its role and SHA-256 digest.
type ArtifactResource struct {
	Path   string `json:"path"`
	Type   string `json:"type"`
	SHA256 string `json:"sha256"`
}

// ValidateArtifact verifies the manifest and every file in the UTM export.
// Call it on a staged copy before publishing the copy to managed storage.
func ValidateArtifact(ctx context.Context, directory string, input io.Reader) (Artifact, error) {
	return checkArtifact(ctx, directory, input, "")
}

// StageArtifact verifies an export while copying its files into an exclusive
// bundle directory. The caller owns the staging directory and its cleanup.
func StageArtifact(ctx context.Context, directory string, input io.Reader, stageRoot, name string) (Artifact, error) {
	if !filepath.IsAbs(stageRoot) || filepath.Base(name) != name || filepath.Ext(name) != ".utm" {
		return Artifact{}, fmt.Errorf("%w: invalid staging location", ErrInvalidArtifact)
	}
	destination := filepath.Join(stageRoot, name)
	if err := os.Mkdir(destination, 0700); err != nil {
		return Artifact{}, fmt.Errorf("create artifact staging directory: %w", err)
	}
	artifact, err := checkArtifact(ctx, directory, input, destination)
	if err != nil {
		return Artifact{}, errors.Join(err, os.RemoveAll(destination))
	}
	return artifact, nil
}

func checkArtifact(ctx context.Context, directory string, input io.Reader, destination string) (Artifact, error) {
	const maxManifestBytes = 1 << 20
	raw, err := io.ReadAll(io.LimitReader(input, maxManifestBytes+1))
	if err != nil || len(raw) > maxManifestBytes {
		return Artifact{}, fmt.Errorf("%w: cannot read manifest", ErrInvalidArtifact)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var manifest ArtifactManifest
	if err := decoder.Decode(&manifest); err != nil || decoder.Decode(new(any)) != io.EOF {
		return Artifact{}, fmt.Errorf("%w: malformed manifest", ErrInvalidArtifact)
	}

	bundle, resources, err := manifestInventory(manifest)
	if err != nil {
		return Artifact{}, err
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return Artifact{}, fmt.Errorf("%w: cannot open export", ErrInvalidArtifact)
	}
	defer root.Close()
	if info, err := root.Lstat(bundle); err != nil || !info.IsDir() {
		return Artifact{}, fmt.Errorf("%w: bundle must be a directory", ErrInvalidArtifact)
	}

	err = fs.WalkDir(root.FS(), bundle, func(name string, entry fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			return fmt.Errorf("%w: cannot enumerate bundle", ErrInvalidArtifact)
		}
		if entry.IsDir() {
			return nil
		}
		expected, found := resources[name]
		if !found || !entry.Type().IsRegular() {
			return fmt.Errorf("%w: undeclared or non-regular bundle file", ErrInvalidArtifact)
		}
		if err := checkResource(ctx, root, name, expected, destination, bundle); err != nil {
			return err
		}
		delete(resources, name)
		return nil
	})
	if err != nil {
		return Artifact{}, err
	}
	if len(resources) != 0 {
		return Artifact{}, fmt.Errorf("%w: missing bundle files", ErrInvalidArtifact)
	}

	sum := sha256.Sum256(raw)
	if destination != "" {
		bundle = destination
	}
	return Artifact{Manifest: manifest, ManifestSHA256: hex.EncodeToString(sum[:]), Bundle: bundle}, nil
}

func manifestInventory(manifest ArtifactManifest) (string, map[string]string, error) {
	if manifest.SchemaVersion != 1 || manifest.Architecture != "amd64" ||
		!hasMetadata(manifest.ReleaseID, manifest.PlatformVersion, manifest.ProtocolVersion,
			manifest.Guest.Version, manifest.IDE.Version, manifest.Provenance.Source, manifest.Provenance.Revision) ||
		!validDigest(manifest.Guest.SHA256) || !validDigest(manifest.IDE.SHA256) {
		return "", nil, fmt.Errorf("%w: incomplete or unsupported manifest metadata", ErrInvalidArtifact)
	}
	bundle := ""
	disks := 0
	config := false
	resources := make(map[string]string, len(manifest.Resources))
	for _, item := range manifest.Resources {
		directory, relative, ok := strings.Cut(item.Path, "/")
		if !ok || !fs.ValidPath(item.Path) || strings.Contains(item.Path, "\\") ||
			path.Ext(directory) != ".utm" || (bundle != "" && directory != bundle) || !validDigest(item.SHA256) {
			return "", nil, fmt.Errorf("%w: invalid bundle path or digest", ErrInvalidArtifact)
		}
		if _, exists := resources[item.Path]; exists {
			return "", nil, fmt.Errorf("%w: duplicate bundle path", ErrInvalidArtifact)
		}
		switch item.Type {
		case "vm":
			config = config || relative == "config.plist"
		case "disk":
			disks++
		case "media", "network":
		default:
			return "", nil, fmt.Errorf("%w: unsupported resource type", ErrInvalidArtifact)
		}
		bundle = directory
		resources[item.Path] = item.SHA256
	}
	if !config || disks == 0 {
		return "", nil, fmt.Errorf("%w: missing VM configuration or persistent disk", ErrInvalidArtifact)
	}
	return bundle, resources, nil
}

func hasMetadata(values ...string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			return false
		}
	}
	return true
}

func validDigest(value string) bool {
	if len(value) != sha256.Size*2 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func checkResource(ctx context.Context, root *os.Root, name, expected, destination, bundle string) error {
	file, err := root.Open(name)
	if err != nil {
		return fmt.Errorf("%w: cannot open bundle file", ErrInvalidArtifact)
	}
	defer file.Close()
	digest := sha256.New()
	var writer io.Writer = digest
	var copyFile *os.File
	if destination != "" {
		relative, _ := strings.CutPrefix(name, bundle+"/")
		path := filepath.Join(destination, relative)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return fmt.Errorf("create staged bundle directory: %w", err)
		}
		copyFile, err = os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return fmt.Errorf("create staged bundle file: %w", err)
		}
		writer = io.MultiWriter(digest, copyFile)
	}
	_, err = io.Copy(writer, artifactReader{ctx, file})
	if copyFile != nil {
		err = errors.Join(err, copyFile.Close())
	}
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("%w: cannot read bundle file", ErrInvalidArtifact)
	}
	if hex.EncodeToString(digest.Sum(nil)) != expected {
		return fmt.Errorf("%w: bundle file digest mismatch", ErrInvalidArtifact)
	}
	return nil
}

type artifactReader struct {
	ctx context.Context
	io.Reader
}

func (r artifactReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.Reader.Read(p)
}
