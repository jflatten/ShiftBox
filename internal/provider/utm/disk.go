package utm

import (
	"context"
	"encoding/binary"
	"io"
	"math"
	"os"
	"path"
	"strings"

	"github.com/jflatten/ShiftBox/internal/provider"
	"github.com/jflatten/ShiftBox/internal/release"
)

// verifyStagedDisk checks the one persistent disk in the MVP artifact through
// the public QCOW2 header. This measures the staged source before import.
func verifyStagedDisk(ctx context.Context, artifact release.Artifact, requested int64) error {
	size, err := stagedDiskCapacity(ctx, artifact)
	if err != nil {
		return err
	}
	if size != requested {
		return provider.ErrCapabilityUnsupported
	}
	return nil
}

func stagedDiskCapacity(ctx context.Context, artifact release.Artifact) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	root, err := os.OpenRoot(artifact.Bundle)
	if err != nil {
		return 0, provider.ErrOutcomeUnknown
	}
	defer root.Close()

	var image string
	for _, resource := range artifact.Manifest.Resources {
		if resource.Type != "disk" {
			continue
		}
		_, relative, ok := strings.Cut(resource.Path, "/")
		if !ok || image != "" || path.Ext(relative) != ".qcow2" {
			return 0, provider.ErrCapabilityUnsupported
		}
		image = relative
	}
	if image == "" {
		return 0, provider.ErrCapabilityUnsupported
	}
	return qcowCapacity(root, image)
}

func qcowCapacity(root *os.Root, name string) (int64, error) {
	info, err := root.Lstat(name)
	if err != nil || !info.Mode().IsRegular() {
		return 0, provider.ErrOutcomeUnknown
	}
	file, err := root.Open(name)
	if err != nil {
		return 0, provider.ErrOutcomeUnknown
	}
	defer file.Close()

	var header [104]byte
	// QCOW2 v2 uses a 72-byte base header. V3 adds incompatible features
	// and the header length. All fields use network byte order.
	if _, err := io.ReadFull(file, header[:72]); err != nil || string(header[:4]) != "QFI\xfb" {
		return 0, provider.ErrCapabilityUnsupported
	}
	read := binary.BigEndian
	if read.Uint64(header[8:16]) != 0 || read.Uint32(header[16:20]) != 0 || read.Uint32(header[32:36]) != 0 {
		return 0, provider.ErrCapabilityUnsupported
	}
	switch read.Uint32(header[4:8]) {
	case 2:
	case 3:
		if _, err := io.ReadFull(file, header[72:]); err != nil ||
			read.Uint64(header[72:80])&(1<<2) != 0 || read.Uint32(header[100:104]) < uint32(len(header)) {
			return 0, provider.ErrCapabilityUnsupported
		}
	default:
		return 0, provider.ErrCapabilityUnsupported
	}
	size := read.Uint64(header[24:32])
	if size == 0 || size > math.MaxInt64 || size%512 != 0 {
		return 0, provider.ErrCapabilityUnsupported
	}
	return int64(size), nil
}
