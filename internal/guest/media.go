package guest

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// ErrInvalidEnrollmentSeed identifies malformed provider delivery material.
var ErrInvalidEnrollmentSeed = errors.New("invalid guest enrollment seed")

// EnrollmentSeed is the payload the guest reads from the recovery medium.
// It contains the public trust anchor and a single short-lived credential.
type EnrollmentSeed struct {
	Grant        string `json:"grant"`
	Endpoint     string `json:"endpoint"`
	CAPEM        string `json:"caPem"`
	PollEndpoint string `json:"pollEndpoint"`
}

func (s EnrollmentSeed) valid() bool {
	key, err := base64.RawURLEncoding.Strict().DecodeString(s.Grant)
	if err != nil || len(key) != 32 || !validPollEndpoint(s.PollEndpoint) {
		return false
	}
	endpoint, err := url.Parse(s.Endpoint)
	if err != nil || endpoint.Scheme != "https" || endpoint.Hostname() == "" ||
		endpoint.Path != "/internal/v1/enroll" || endpoint.User != nil || endpoint.RawQuery != "" ||
		endpoint.ForceQuery || endpoint.Fragment != "" {
		return false
	}
	raw := bytes.TrimSpace([]byte(s.CAPEM))
	block, rest := pem.Decode(raw)
	if block == nil || block.Type != "CERTIFICATE" || len(block.Headers) > 0 ||
		len(bytes.TrimSpace(rest)) > 0 || !bytes.HasPrefix(raw, []byte("-----BEGIN CERTIFICATE-----")) {
		return false
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	return err == nil && cert.IsCA && cert.BasicConstraintsValid && cert.CheckSignatureFrom(cert) == nil
}

// BuildEnrollmentMedia creates a private macOS ISO for one delivery attempt.
// Its caller retains the image until consumption or explicit invalidation.
func BuildEnrollmentMedia(ctx context.Context, root string, seed EnrollmentSeed) (string, error) {
	if !filepath.IsAbs(root) || !seed.valid() {
		return "", ErrInvalidEnrollmentSeed
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(seed)
	if err != nil {
		return "", ErrInvalidEnrollmentSeed
	}
	dir, err := os.MkdirTemp(root, "enrollment-")
	if err != nil {
		return "", errors.New("cannot create private enrollment directory")
	}
	image := filepath.Join(dir, "enrollment.iso")
	keep := false
	defer func() {
		if !keep {
			_ = os.RemoveAll(dir)
		}
	}()
	payload := filepath.Join(dir, "payload")
	if err := os.Mkdir(payload, 0700); err != nil {
		return "", errors.New("cannot prepare enrollment payload")
	}
	if err := os.WriteFile(filepath.Join(payload, "ENROLLMENT.JSON"), encoded, 0600); err != nil {
		return "", errors.New("cannot write enrollment payload")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/usr/bin/hdiutil", "makehybrid", "-iso", "-iso-volume-name", "SHIFTBOX", "-o", image, payload)
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", errors.New("cannot build enrollment image")
	}
	if err := os.Chmod(image, 0600); err != nil {
		return "", errors.New("cannot protect enrollment image")
	}
	if err := os.RemoveAll(payload); err != nil {
		return "", errors.New("cannot remove enrollment staging payload")
	}
	keep = true
	return image, nil
}

func clearEnrollmentImage(ctx context.Context, image string) error {
	// Replace the path atomically. The running VM may still hold its old
	// descriptor, and the next boot needs readable media at the same path.
	stage, err := os.MkdirTemp(filepath.Dir(image), ".clear-seed-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	payload := filepath.Join(stage, "payload")
	if err := os.Mkdir(payload, 0700); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(payload, "README.TXT"), []byte("Empty ShiftBox enrollment media.\n"), 0600); err != nil {
		return err
	}
	blank := filepath.Join(stage, "empty.iso")
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/usr/bin/hdiutil", "makehybrid", "-iso", "-iso-volume-name", "SHIFTBOX_EMPTY", "-o", blank, payload)
	if err := cmd.Run(); err != nil {
		return errors.New("cannot clear consumed enrollment media")
	}
	if err := os.Chmod(blank, 0600); err != nil {
		return err
	}
	return os.Rename(blank, image)
}
