// Command manifest writes the resource inventory for a prepared ShiftBox UTM export.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/jflatten/ShiftBox/internal/release"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: go run ./platform/packer/manifest <bundle.utm> < build-metadata.json")
		os.Exit(2)
	}
	if err := writeManifest(ctx, os.Args[1], os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func writeManifest(ctx context.Context, bundle string, input io.Reader, output io.Writer) error {
	const maxMetadata = 64 << 10
	var metadata release.ArtifactManifest
	decoder := json.NewDecoder(io.LimitReader(input, maxMetadata))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&metadata); err != nil || decoder.Decode(new(any)) != io.EOF {
		return errors.New("expected one platform build metadata document")
	}
	bundle = filepath.Clean(bundle)
	manifest, err := release.GenerateArtifactManifest(ctx, filepath.Dir(bundle), filepath.Base(bundle), metadata)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	return encoder.Encode(manifest)
}
