package release

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"
)

// GenerateArtifactManifest hashes the files in a prepared UTM export without
// changing the bundle. Component versions and provenance come from the build.
func GenerateArtifactManifest(ctx context.Context, directory, bundle string, metadata ArtifactManifest) (ArtifactManifest, error) {
	if !fs.ValidPath(bundle) || path.Base(bundle) != bundle || path.Ext(bundle) != ".utm" ||
		strings.Contains(bundle, "\\") || len(metadata.Resources) != 0 {
		return ArtifactManifest{}, fmt.Errorf("%w: expected one prepared UTM bundle without a resource list", ErrInvalidArtifact)
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return ArtifactManifest{}, fmt.Errorf("open prepared export: %w", err)
	}
	defer root.Close()
	info, err := root.Lstat(bundle)
	if err != nil || !info.IsDir() {
		return ArtifactManifest{}, fmt.Errorf("%w: bundle must be a directory", ErrInvalidArtifact)
	}

	err = fs.WalkDir(root.FS(), bundle, func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		relative, _ := strings.CutPrefix(name, bundle+"/")
		kind := exportResourceType(relative)
		if kind == "" || !entry.Type().IsRegular() {
			return fmt.Errorf("%w: unexpected export file %q", ErrInvalidArtifact, relative)
		}
		digest, err := digestExportFile(ctx, root, name)
		if err != nil {
			return err
		}
		metadata.Resources = append(metadata.Resources, ArtifactResource{Path: name, Type: kind, SHA256: digest})
		return nil
	})
	if err != nil {
		return ArtifactManifest{}, err
	}
	if _, _, err := manifestInventory(metadata); err != nil {
		return ArtifactManifest{}, err
	}
	return metadata, nil
}

func exportResourceType(relative string) string {
	switch relative {
	case "config.plist":
		return "vm"
	case "Data/efi_vars.fd":
		return "media"
	}
	if path.Dir(relative) == "Data" && path.Ext(relative) == ".qcow2" {
		return "disk"
	}
	return ""
}

func digestExportFile(ctx context.Context, root *os.Root, name string) (string, error) {
	file, err := root.Open(name)
	if err != nil {
		return "", fmt.Errorf("open export file: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("%w: expected a regular export file", ErrInvalidArtifact)
	}
	digest := sha256.New()
	if _, err := io.Copy(digest, artifactReader{ctx, file}); err != nil {
		return "", fmt.Errorf("hash export file: %w", err)
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}
