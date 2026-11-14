package backup

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	"filippo.io/age"
)

const manifestName = "manifest.json"

var errArchiveInvalid = errors.New("management archive validation failed")

// ArchiveEvidence is safe to persist after the encrypted file is closed and read back.
type ArchiveEvidence struct {
	Metadata       ArchiveMetadata
	ManifestDigest string
	ArchiveDigest  string
	Size           int64
}

// CaptureArchive owns the private staging directory through publication.
// The profile's capture function must release its management write fence
// before returning the staged files for encryption.
func CaptureArchive(ctx context.Context, output, password string, metadata ArchiveMetadata,
	capture func(context.Context, string) (map[string]string, error),
) (ArchiveEvidence, error) {
	if !filepath.IsAbs(output) || !metadata.valid() || password == "" || capture == nil {
		return ArchiveEvidence{}, errArchiveInvalid
	}
	if err := ctx.Err(); err != nil {
		return ArchiveEvidence{}, err
	}
	if _, err := os.Lstat(output); !errors.Is(err, os.ErrNotExist) {
		return ArchiveEvidence{}, errors.New("backup output path is unavailable")
	}
	dir, err := os.MkdirTemp(filepath.Dir(output), ".backup-checkpoint-")
	if err != nil {
		return ArchiveEvidence{}, errors.New("cannot stage protected backup checkpoint")
	}
	defer os.RemoveAll(dir)
	sources, err := capture(ctx, dir)
	if err != nil {
		return ArchiveEvidence{}, err
	}
	return CreateArchive(ctx, output, password, metadata, sources)
}

// CreateArchive writes an age-encrypted management archive from staged files.
// It publishes only a closed and verified file and refuses to replace a target.
func CreateArchive(ctx context.Context, output, password string, metadata ArchiveMetadata, sources map[string]string) (ArchiveEvidence, error) {
	if !filepath.IsAbs(output) || !metadata.valid() || password == "" {
		return ArchiveEvidence{}, errArchiveInvalid
	}
	if _, err := os.Lstat(output); !errors.Is(err, os.ErrNotExist) {
		return ArchiveEvidence{}, errors.New("backup output path is unavailable")
	}
	names := archiveNames(metadata.Profile)
	if len(sources) != len(names) {
		return ArchiveEvidence{}, errArchiveInvalid
	}
	doc := manifest{Version: archiveVersion, Metadata: metadata}
	for _, name := range names {
		file, err := openCheckpoint(sources[name])
		if err != nil {
			return ArchiveEvidence{}, err
		}
		hash := sha256.New()
		n, err := io.Copy(hash, contextReader{ctx, file})
		if err := errors.Join(err, file.Close()); err != nil || n == 0 {
			return ArchiveEvidence{}, errArchiveInvalid
		}
		doc.Files = append(doc.Files, archiveFile{Name: name, Size: n, Digest: hex.EncodeToString(hash.Sum(nil))})
	}
	data, err := json.Marshal(doc)
	if err != nil {
		return ArchiveEvidence{}, err
	}
	recipient, err := age.NewScryptRecipient(password)
	if err != nil {
		return ArchiveEvidence{}, errArchiveInvalid
	}
	dir := filepath.Dir(output)
	file, err := os.CreateTemp(dir, ".backup-*.age")
	if err != nil {
		return ArchiveEvidence{}, errors.New("cannot create encrypted backup output")
	}
	defer os.Remove(file.Name())
	writeErr := encodeArchive(ctx, file, recipient, data, doc, sources)
	if err := errors.Join(writeErr, file.Sync(), file.Close()); err != nil {
		return ArchiveEvidence{}, errors.New("cannot finish encrypted backup")
	}
	closed, err := os.Open(file.Name())
	if err != nil {
		return ArchiveEvidence{}, errArchiveInvalid
	}
	evidence, err := VerifyArchive(ctx, closed, password)
	if err := errors.Join(err, closed.Close()); err != nil || evidence.Metadata != metadata ||
		evidence.ManifestDigest != digestBytes(data) {
		return ArchiveEvidence{}, errArchiveInvalid
	}
	if err := ctx.Err(); err != nil {
		return ArchiveEvidence{}, err
	}
	if err := os.Link(file.Name(), output); err != nil {
		return ArchiveEvidence{}, errors.New("cannot publish encrypted backup without replacing an existing file")
	}
	parent, err := os.Open(dir)
	if err != nil {
		return ArchiveEvidence{}, errors.New("cannot confirm backup directory durability")
	}
	if err := errors.Join(parent.Sync(), parent.Close()); err != nil {
		return ArchiveEvidence{}, errors.New("cannot confirm backup directory durability")
	}
	return evidence, nil
}

func encodeArchive(ctx context.Context, dst io.Writer, recipient *age.ScryptRecipient, data []byte, doc manifest, sources map[string]string) error {
	encrypted, err := age.Encrypt(dst, recipient)
	if err != nil {
		return err
	}
	writer := tar.NewWriter(encrypted)
	writeErr := writeArchiveFiles(ctx, writer, data, doc, sources)
	return errors.Join(writeErr, writer.Close(), encrypted.Close())
}

func writeArchiveFiles(ctx context.Context, writer *tar.Writer, data []byte, doc manifest, sources map[string]string) error {
	if err := writer.WriteHeader(archiveHeader(manifestName, int64(len(data)))); err != nil {
		return err
	}
	if _, err := writer.Write(data); err != nil {
		return err
	}
	for _, item := range doc.Files {
		file, err := openCheckpoint(sources[item.Name])
		if err != nil {
			return err
		}
		headerErr := writer.WriteHeader(archiveHeader(item.Name, item.Size))
		if headerErr != nil {
			return errors.Join(headerErr, file.Close())
		}
		n, copyErr := io.Copy(writer, contextReader{ctx, file})
		if err := errors.Join(copyErr, file.Close()); err != nil || n != item.Size {
			return errArchiveInvalid
		}
	}
	return nil
}

func archiveHeader(name string, size int64) *tar.Header {
	return &tar.Header{Name: name, Mode: 0600, Size: size, Typeflag: tar.TypeReg}
}

func openCheckpoint(path string) (*os.File, error) {
	if !filepath.IsAbs(path) {
		return nil, errArchiveInvalid
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, errArchiveInvalid
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, errArchiveInvalid
	}
	operatorFile, err := file.Stat()
	if err != nil || !os.SameFile(info, operatorFile) {
		return nil, errors.Join(errArchiveInvalid, file.Close())
	}
	return file, nil
}

// VerifyArchive authenticates the entire age stream and matches each regular
// entry against the canonical manifest without extracting plaintext to disk.
func VerifyArchive(ctx context.Context, input io.Reader, password string) (ArchiveEvidence, error) {
	identity, err := age.NewScryptIdentity(password)
	if err != nil {
		return ArchiveEvidence{}, errArchiveInvalid
	}
	sum := sha256.New()
	count := &archiveBytes{}
	plaintext, err := age.Decrypt(io.TeeReader(contextReader{ctx, input}, io.MultiWriter(sum, count)), identity)
	if err != nil {
		return ArchiveEvidence{}, errArchiveInvalid
	}
	reader := tar.NewReader(plaintext)
	const maxManifest = 64 << 10
	header, err := reader.Next()
	if err != nil || header.Typeflag != tar.TypeReg || header.Name != manifestName || header.Size <= 0 || header.Size > maxManifest {
		return ArchiveEvidence{}, errArchiveInvalid
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		return ArchiveEvidence{}, errArchiveInvalid
	}
	var doc manifest
	if err := json.Unmarshal(data, &doc); err != nil || !doc.valid() {
		return ArchiveEvidence{}, errArchiveInvalid
	}
	canonical, err := json.Marshal(doc)
	if err != nil || !bytes.Equal(data, canonical) {
		return ArchiveEvidence{}, errArchiveInvalid
	}
	for _, item := range doc.Files {
		header, err := reader.Next()
		if err != nil || header.Typeflag != tar.TypeReg || header.Name != item.Name || header.Size != item.Size {
			return ArchiveEvidence{}, errArchiveInvalid
		}
		hash := sha256.New()
		n, err := io.Copy(hash, reader)
		if err != nil || n != item.Size || hex.EncodeToString(hash.Sum(nil)) != item.Digest {
			return ArchiveEvidence{}, errArchiveInvalid
		}
	}
	if _, err := reader.Next(); err != io.EOF {
		return ArchiveEvidence{}, errArchiveInvalid
	}
	// Force authentication of the encrypted stream terminator after tar EOF.
	if n, err := io.Copy(io.Discard, plaintext); err != nil || n != 0 {
		return ArchiveEvidence{}, errArchiveInvalid
	}
	return ArchiveEvidence{Metadata: doc.Metadata, ManifestDigest: digestBytes(data),
		ArchiveDigest: hex.EncodeToString(sum.Sum(nil)), Size: count.n}, nil
}

func digestBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

type contextReader struct {
	ctx context.Context
	io.Reader
}

func (r contextReader) Read(data []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.Reader.Read(data)
}

type archiveBytes struct{ n int64 }

func (c *archiveBytes) Write(data []byte) (int, error) {
	c.n += int64(len(data))
	return len(data), nil
}
