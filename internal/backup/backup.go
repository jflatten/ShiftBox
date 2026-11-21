package backup

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"filippo.io/age"
	"golang.org/x/sys/unix"
)

const manifestName = "manifest.json"

var (
	errArchiveInvalid          = errors.New("management archive validation failed")
	errArchiveDirectoryMissing = errors.New("backup output directory does not exist")
)

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
func CaptureArchive(ctx context.Context, output, password string, metadata ArchiveMetadata, checkpointParent string,
	capture func(context.Context, string) (map[string]string, error),
) (ArchiveEvidence, error) {
	if !filepath.IsAbs(output) || !metadata.valid() || password == "" || capture == nil {
		return ArchiveEvidence{}, errArchiveInvalid
	}
	if err := ctx.Err(); err != nil {
		return ArchiveEvidence{}, err
	}
	parent, name, err := openArchiveDestination(output)
	if err != nil {
		return ArchiveEvidence{}, err
	}
	defer parent.Close()

	// Plaintext belongs in a private controller staging area. The requested
	// destination directory receives only the encrypted archive.
	if !privateCheckpointDirectory(checkpointParent) {
		return ArchiveEvidence{}, errors.New("backup checkpoint requires a private staging parent")
	}
	dir, err := os.MkdirTemp(checkpointParent, ".backup-checkpoint-")
	if err != nil {
		return ArchiveEvidence{}, errors.New("cannot stage protected backup checkpoint")
	}
	defer os.RemoveAll(dir)
	sources, err := capture(ctx, dir)
	if err != nil {
		return ArchiveEvidence{}, err
	}
	return createArchive(ctx, parent, name, password, metadata, sources)
}

// CreateArchive writes an age-encrypted management archive from staged files.
// It publishes only a closed and verified file and refuses to replace a target.
func CreateArchive(ctx context.Context, output, password string, metadata ArchiveMetadata, sources map[string]string) (ArchiveEvidence, error) {
	if !filepath.IsAbs(output) || !metadata.valid() || password == "" {
		return ArchiveEvidence{}, errArchiveInvalid
	}
	parent, name, err := openArchiveDestination(output)
	if err != nil {
		return ArchiveEvidence{}, err
	}
	defer parent.Close()
	return createArchive(ctx, parent, name, password, metadata, sources)
}

// CheckArchiveDestination rejects an occupied archive path before the CLI
// starts a backup. Publication checks again atomically.
func CheckArchiveDestination(output string) error {
	parent, _, err := openArchiveDestination(output)
	if errors.Is(err, errArchiveDirectoryMissing) {
		// The CLI preflight reports conflicts. Capture records a missing
		// parent as a failed operation, just as it does for other I/O errors.
		return nil
	}
	if err != nil {
		return err
	}
	return parent.Close()
}

func openArchiveDestination(output string) (*os.Root, string, error) {
	if !filepath.IsAbs(output) || filepath.Clean(output) != output || strings.ContainsRune(output, 0) {
		return nil, "", errArchiveInvalid
	}
	name := filepath.Base(output)
	if !filepath.IsLocal(name) {
		return nil, "", errArchiveInvalid
	}
	// The authorized operator can choose an existing directory, including a
	// directory alias. Keep its handle so later path changes cannot redirect I/O.
	parent, err := os.OpenRoot(filepath.Dir(output))
	if errors.Is(err, os.ErrNotExist) {
		return nil, "", errArchiveDirectoryMissing
	}
	if err != nil {
		return nil, "", errors.New("backup output directory is unavailable")
	}
	if _, err := parent.Lstat(name); !errors.Is(err, os.ErrNotExist) {
		return nil, "", errors.Join(errors.New("backup output path is unavailable"), parent.Close())
	}
	return parent, name, nil
}

func createArchive(ctx context.Context, parent *os.Root, name, password string, metadata ArchiveMetadata, sources map[string]string) (ArchiveEvidence, error) {
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
	temp := ".backup-" + rand.Text() + ".age"
	file, err := parent.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return ArchiveEvidence{}, errors.New("cannot create encrypted backup output")
	}
	defer parent.Remove(temp)
	writeErr := encodeArchive(ctx, file, recipient, data, doc, sources)
	info, statErr := file.Stat()
	if err := errors.Join(writeErr, statErr, file.Sync(), file.Close()); err != nil {
		return ArchiveEvidence{}, errors.New("cannot finish encrypted backup")
	}
	closed, err := parent.OpenFile(temp, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return ArchiveEvidence{}, errArchiveInvalid
	}
	if closedInfo, err := closed.Stat(); err != nil || !os.SameFile(info, closedInfo) {
		return ArchiveEvidence{}, errors.Join(errArchiveInvalid, closed.Close())
	}
	evidence, err := VerifyArchive(ctx, closed, password)
	if err := errors.Join(err, closed.Close()); err != nil || evidence.Metadata != metadata ||
		evidence.ManifestDigest != digestBytes(data) {
		return ArchiveEvidence{}, errArchiveInvalid
	}
	if err := ctx.Err(); err != nil {
		return ArchiveEvidence{}, err
	}
	if err := publishArchive(parent, temp, name, info); err != nil {
		return ArchiveEvidence{}, err
	}
	return evidence, nil
}

func publishArchive(parent *os.Root, temp, name string, verified os.FileInfo) error {
	// If the chosen path was renamed or retargeted during capture, the CLI
	// must not report a successful backup at an address that no longer names it.
	current, err := os.OpenRoot(parent.Name())
	if err != nil {
		return errors.New("backup output directory changed before publication")
	}
	defer current.Close()
	originalInfo, err := parent.Stat(".")
	if err != nil {
		return errors.New("cannot confirm backup output directory")
	}
	currentInfo, err := current.Stat(".")
	if err != nil || !os.SameFile(originalInfo, currentInfo) {
		return errors.New("backup output directory changed before publication")
	}
	staged, err := parent.Lstat(temp)
	if err != nil || !os.SameFile(verified, staged) {
		return errArchiveInvalid
	}
	if err := parent.Link(temp, name); err != nil {
		return errors.New("cannot publish encrypted backup without replacing an existing file")
	}
	published, err := parent.Lstat(name)
	if err != nil || !os.SameFile(verified, published) {
		return errArchiveInvalid
	}
	directory, err := parent.Open(".")
	if err != nil {
		return errors.New("cannot confirm backup directory durability")
	}
	if err := errors.Join(directory.Sync(), directory.Close()); err != nil {
		return errors.New("cannot confirm backup directory durability")
	}
	return nil
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
	evidence, _, err := verifyArchive(ctx, input, password)
	return evidence, err
}

func verifyArchive(ctx context.Context, input io.Reader, password string) (ArchiveEvidence, manifest, error) {
	return readArchive(ctx, input, password, nil)
}

// readArchive always authenticates the same canonical stream. A staging
// callback may copy regular entries into a private, disposable directory.
func readArchive(ctx context.Context, input io.Reader, password string,
	stage func(archiveFile, io.Reader) error) (ArchiveEvidence, manifest, error) {
	identity, err := age.NewScryptIdentity(password)
	if err != nil {
		return ArchiveEvidence{}, manifest{}, errArchiveInvalid
	}
	sum := sha256.New()
	count := &archiveBytes{}
	plaintext, err := age.Decrypt(io.TeeReader(contextReader{ctx, input}, io.MultiWriter(sum, count)), identity)
	if err != nil {
		return ArchiveEvidence{}, manifest{}, errArchiveInvalid
	}
	reader := tar.NewReader(plaintext)
	const maxManifest = 64 << 10
	header, err := reader.Next()
	if err != nil || header.Typeflag != tar.TypeReg || header.Name != manifestName || header.Size <= 0 || header.Size > maxManifest {
		return ArchiveEvidence{}, manifest{}, errArchiveInvalid
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		return ArchiveEvidence{}, manifest{}, errArchiveInvalid
	}
	var doc manifest
	if err := json.Unmarshal(data, &doc); err != nil || !doc.valid() {
		return ArchiveEvidence{}, manifest{}, errArchiveInvalid
	}
	canonical, err := json.Marshal(doc)
	if err != nil || !bytes.Equal(data, canonical) {
		return ArchiveEvidence{}, manifest{}, errArchiveInvalid
	}
	for _, item := range doc.Files {
		header, err := reader.Next()
		if err != nil || header.Typeflag != tar.TypeReg || header.Name != item.Name || header.Size != item.Size {
			return ArchiveEvidence{}, manifest{}, errArchiveInvalid
		}
		hash := sha256.New()
		counter := &archiveBytes{}
		entry := io.TeeReader(reader, io.MultiWriter(hash, counter))
		if stage != nil {
			err = stage(item, entry)
		} else {
			_, err = io.Copy(io.Discard, entry)
		}
		if err != nil || counter.n != item.Size || hex.EncodeToString(hash.Sum(nil)) != item.Digest {
			return ArchiveEvidence{}, manifest{}, errArchiveInvalid
		}
	}
	if _, err := reader.Next(); err != io.EOF {
		return ArchiveEvidence{}, manifest{}, errArchiveInvalid
	}
	// Force authentication of the encrypted stream terminator after tar EOF.
	if n, err := io.Copy(io.Discard, plaintext); err != nil || n != 0 {
		return ArchiveEvidence{}, manifest{}, errArchiveInvalid
	}
	return ArchiveEvidence{Metadata: doc.Metadata, ManifestDigest: digestBytes(data),
		ArchiveDigest: hex.EncodeToString(sum.Sum(nil)), Size: count.n}, doc, nil
}

func digestBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

type contextReader struct {
	ctx context.Context
	io.Reader
}

// Read stops archive I/O once the checkpoint context is canceled.
func (r contextReader) Read(data []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.Reader.Read(data)
}

type archiveBytes struct{ n int64 }

// Write counts bytes for archive evidence without retaining their contents.
func (c *archiveBytes) Write(data []byte) (int, error) {
	c.n += int64(len(data))
	return len(data), nil
}
