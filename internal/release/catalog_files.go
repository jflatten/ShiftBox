package release

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

const catalogManifestName = "manifest.json"

// Catalog binds release records to content in a controller-owned directory.
type Catalog struct {
	store *Store
	root  string
}

// NewCatalog uses the supplied absolute directory for immutable release files.
func NewCatalog(store *Store, root string) *Catalog {
	return &Catalog{store: store, root: root}
}

// Import stages verified content and records a release awaiting boot validation.
// An interrupted publication can be resumed using the same ID and manifest.
func (c *Catalog) Import(ctx context.Context, id, actor string, now time.Time, source string, input io.Reader) (CatalogEntry, error) {
	if !filepath.IsAbs(c.root) {
		return CatalogEntry{}, fmt.Errorf("%w: managed storage must be absolute", ErrInvalidArtifact)
	}
	raw, manifest, err := readArtifactManifest(input)
	if err != nil {
		return CatalogEntry{}, err
	}
	bundle, _, err := manifestInventory(manifest)
	if err != nil {
		return CatalogEntry{}, err
	}
	sum := sha256.Sum256(raw)
	digest := hex.EncodeToString(sum[:])
	entry, err := c.store.Get(ctx, id)
	switch {
	case err == nil:
		if entry.Artifact.ManifestSHA256 != digest || entry.ImportedBy != actor || entry.State == "rejected" {
			return CatalogEntry{}, ErrCatalogConflict
		}
		// A completed publication can be reused even if the upload is gone.
		directory := filepath.Join(c.root, digest)
		if _, err := os.Lstat(directory); err == nil {
			if _, err := c.verifyManaged(ctx, entry); err != nil {
				return CatalogEntry{}, err
			}
			return entry, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return CatalogEntry{}, err
		}
	case !errors.Is(err, sql.ErrNoRows):
		return CatalogEntry{}, err
	}

	if err := os.MkdirAll(c.root, 0700); err != nil {
		return CatalogEntry{}, fmt.Errorf("create managed catalog directory: %w", err)
	}
	pending, err := os.MkdirTemp(c.root, ".import-")
	if err != nil {
		return CatalogEntry{}, fmt.Errorf("create catalog staging directory: %w", err)
	}
	defer os.RemoveAll(pending)
	artifact, err := StageArtifact(ctx, source, bytes.NewReader(raw), pending, bundle)
	if err != nil {
		return CatalogEntry{}, err
	}
	if err := os.WriteFile(filepath.Join(pending, catalogManifestName), raw, 0600); err != nil {
		return CatalogEntry{}, fmt.Errorf("write managed catalog manifest: %w", err)
	}
	if entry.ID == "" {
		artifact.Bundle = bundle
		if err := c.store.BeginImport(ctx, id, artifact, actor, now); err != nil && !errors.Is(err, ErrCatalogConflict) {
			return CatalogEntry{}, err
		}
		entry, err = c.store.Get(ctx, id)
		if errors.Is(err, sql.ErrNoRows) {
			return CatalogEntry{}, ErrCatalogConflict
		}
		if err != nil {
			return CatalogEntry{}, err
		}
		if entry.Artifact.ManifestSHA256 != digest || entry.ImportedBy != actor || entry.State == "rejected" {
			return CatalogEntry{}, ErrCatalogConflict
		}
	}

	destination := filepath.Join(c.root, digest)
	if err := os.Rename(pending, destination); err != nil {
		// Another retry may have published the same content-addressed release.
		if _, checkErr := c.managedManifest(entry); checkErr != nil {
			return CatalogEntry{}, fmt.Errorf("publish catalog content: %w", err)
		}
		if _, err := ValidateArtifact(ctx, destination, bytes.NewReader(raw)); err != nil {
			return CatalogEntry{}, err
		}
	}
	return entry, nil
}

// ValidateImport publishes availability after a successful provider and guest
// boot check. An interrupted check leaves the release awaiting validation.
// The check receives a disposable copy of the verified bundle.
func (c *Catalog) ValidateImport(ctx context.Context, id string, check func(context.Context, Artifact) error) error {
	if check == nil {
		return ErrInvalidArtifact
	}
	entry, err := c.store.Get(ctx, id)
	if err != nil {
		return err
	}
	switch entry.State {
	case "available":
		_, err := c.verifyManaged(ctx, entry)
		return err
	case "validating":
	default:
		return ErrCatalogConflict
	}
	raw, err := c.managedManifest(entry)
	if err != nil {
		return err
	}
	stage, err := os.MkdirTemp(c.root, ".boot-check-")
	if err != nil {
		return fmt.Errorf("create boot validation staging directory: %w", err)
	}
	defer os.RemoveAll(stage)
	artifact, err := StageArtifact(ctx, filepath.Join(c.root, entry.Artifact.ManifestSHA256), bytes.NewReader(raw), stage, entry.Artifact.Bundle)
	if err != nil {
		return err
	}
	if err := check(ctx, artifact); err != nil {
		return err
	}
	if _, err := c.verifyManaged(ctx, entry); err != nil {
		return err
	}
	return c.store.CompleteImport(ctx, id, artifact.ManifestSHA256, true)
}

func (c *Catalog) verifyManaged(ctx context.Context, entry CatalogEntry) (Artifact, error) {
	raw, err := c.managedManifest(entry)
	if err != nil {
		return Artifact{}, err
	}
	directory := filepath.Join(c.root, entry.Artifact.ManifestSHA256)
	artifact, err := ValidateArtifact(ctx, directory, bytes.NewReader(raw))
	if err != nil {
		return Artifact{}, err
	}
	artifact.Bundle = filepath.Join(directory, artifact.Bundle)
	return artifact, nil
}

// Stage copies an available release into a workspace staging directory after
// checking that both the manifest and its files still match the catalog.
func (c *Catalog) Stage(ctx context.Context, id, stageRoot, bundleName string) (Artifact, error) {
	entry, err := c.store.Get(ctx, id)
	if err != nil {
		return Artifact{}, err
	}
	if entry.State != "available" {
		return Artifact{}, ErrCatalogConflict
	}
	raw, err := c.managedManifest(entry)
	if err != nil {
		return Artifact{}, err
	}
	return StageArtifact(ctx, filepath.Join(c.root, entry.Artifact.ManifestSHA256), bytes.NewReader(raw), stageRoot, bundleName)
}

func (c *Catalog) managedManifest(entry CatalogEntry) ([]byte, error) {
	digest := entry.Artifact.ManifestSHA256
	if !filepath.IsAbs(c.root) || !validDigest(digest) {
		return nil, ErrInvalidArtifact
	}
	directory := filepath.Join(c.root, digest)
	if info, err := os.Lstat(directory); err != nil || !info.IsDir() {
		return nil, fmt.Errorf("%w: managed catalog directory unavailable", ErrInvalidArtifact)
	}
	path := filepath.Join(directory, catalogManifestName)
	if info, err := os.Lstat(path); err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: managed catalog manifest unavailable", ErrInvalidArtifact)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("%w: cannot open managed catalog manifest", ErrInvalidArtifact)
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, maxManifestBytes+1))
	if err != nil || len(raw) > maxManifestBytes {
		return nil, fmt.Errorf("%w: cannot read managed catalog manifest", ErrInvalidArtifact)
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != digest {
		return nil, fmt.Errorf("%w: managed catalog manifest digest mismatch", ErrInvalidArtifact)
	}
	return raw, nil
}
