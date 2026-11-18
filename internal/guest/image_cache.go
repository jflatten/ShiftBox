package guest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/distribution/reference"
)

// ImageCacheOfferHeader advertises the cache mode outside the strict poll body
// so current and previous guest releases can share the same protocol.
const ImageCacheOfferHeader = "ShiftBox-Image-Cache"

const (
	dockerExecutable = "/usr/bin/docker"
	managedHostsMark = "# Managed by ShiftBox image cache\n"
)

func imageCacheHosts(endpoint string, directFallback bool, caPath, certPath, keyPath string) ([]byte, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil ||
		u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" {
		return nil, errors.New("the image cache endpoint is not a verified HTTPS origin")
	}
	for _, name := range []string{caPath, certPath, keyPath} {
		if !filepath.IsAbs(name) || strings.ContainsAny(name, "\\\"\r\n\t\x00") {
			return nil, errors.New("invalid image cache certificate path")
		}
	}
	var section string
	if directFallback {
		section = "[host." + strconv.Quote(endpoint) + "]\n"
	} else {
		// A terminal cache endpoint makes restricted guests independent of
		// the resolver's parent fallback behavior.
		section = "server = " + strconv.Quote(endpoint) + "\n"
	}
	return fmt.Appendf([]byte(managedHostsMark+section),
		"capabilities = [\"pull\", \"resolve\"]\nca = %q\nclient = [[%q, %q]]\n",
		caPath, certPath, keyPath), nil
}

func (d devContainer) prepareImage(ctx context.Context) error {
	image := d.literalImage()
	if image == "" {
		body, err := d.command(ctx, "read-configuration")
		if err != nil {
			return err
		}
		var config struct {
			Configuration *struct {
				Image string `json:"image"`
			} `json:"configuration"`
		}
		if len(body) > devContainerOutputMax || json.Unmarshal(body, &config) != nil || config.Configuration == nil {
			return errors.New("Dev Container CLI did not return resolved configuration")
		}
		image = config.Configuration.Image
	}
	if image == "" {
		return nil // The embedded Docker builder handles Dockerfile bases.
	}
	if strings.HasPrefix(image, "-") || strings.ContainsAny(image, " \t\r\n\x00") {
		return errors.New("invalid devcontainer image reference")
	}
	if _, err := d.runner()(ctx, dockerExecutable, "pull", image); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("cannot pull the devcontainer image")
	}
	return nil
}

// literalImage recognizes an explicit image in ordinary JSON. The reference CLI
// owns discovery, JSONC, substitutions, and all other configuration formats.
func (d devContainer) literalImage() string {
	if !filepath.IsAbs(d.workspace) || !validDevContainerPath(d.config) {
		return ""
	}
	root, err := os.OpenRoot(d.workspace)
	if err != nil {
		return ""
	}
	defer root.Close()
	file, err := root.OpenFile(d.config, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return ""
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > devContainerOutputMax {
		return ""
	}
	body, err := io.ReadAll(io.LimitReader(file, devContainerOutputMax+1))
	var fields map[string]json.RawMessage
	if err != nil || len(body) > devContainerOutputMax || json.Unmarshal(body, &fields) != nil {
		return ""
	}
	for key := range fields {
		if (key != "image" && strings.EqualFold(key, "image")) || key == "build" || key == "dockerFile" || key == "dockerComposeFile" {
			return ""
		}
	}
	var image string
	if json.Unmarshal(fields["image"], &image) != nil {
		return ""
	}
	if _, err := reference.ParseNormalizedNamed(image); err != nil {
		return ""
	}
	return image
}

// updateImageCache atomically switches Docker to the current guest key pair.
// New certificate paths keep the previous pair complete during renewal.
func updateImageCache(stateDir, pollEndpoint, mode string) error {
	const hostsDirectory = "/etc/docker/certs.d/_default"
	path := filepath.Join(hostsDirectory, "hosts.toml")
	old, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	managed := bytes.HasPrefix(old, []byte(managedHostsMark))
	if mode == "" {
		if managed {
			return os.Remove(path)
		}
		return nil
	}
	if mode != "mirror" && mode != "mirror-and-direct" {
		return errors.New("invalid image cache mode")
	}
	if len(old) > 0 && !managed {
		return errors.New("Docker already has an unmanaged default registry configuration")
	}
	origin, err := url.Parse(pollEndpoint)
	if err != nil || !validPollEndpoint(pollEndpoint) {
		return errors.New("invalid image cache poll endpoint")
	}
	origin.Path, origin.RawPath = "", ""
	record, err := readAgentIdentity(stateDir)
	if err != nil {
		return err
	}
	recordHash := sha256.Sum256([]byte(record.CertificatePEM))
	credentials := filepath.Join(stateDir, "image-cache", fmt.Sprintf("%x", recordHash))
	caPath, certPath, keyPath := filepath.Join(credentials, "ca.pem"),
		filepath.Join(credentials, "client.pem"), filepath.Join(credentials, "client.key")
	hosts, err := imageCacheHosts(origin.String(), mode == "mirror-and-direct", caPath, certPath, keyPath)
	if err != nil {
		return err
	}
	if bytes.Equal(old, hosts) {
		return nil
	}
	if err := os.MkdirAll(credentials, 0700); err != nil {
		return err
	}
	for _, entry := range []struct {
		path string
		data []byte
	}{{caPath, []byte(record.CAPEM)}, {certPath, []byte(record.CertificatePEM)}, {keyPath, record.KeyPEM}} {
		if err := writePrivateState(entry.path, entry.data); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(hostsDirectory, 0755); err != nil {
		return err
	}
	return writePrivateState(path, hosts)
}

// ImageCacheMode allows direct fallback only for the verified open egress
// policy. A restricted workspace always uses the terminal mirror.
func ImageCacheMode(policy string, retentionEnabled bool) string {
	var snapshot struct {
		Mode string `json:"mode"`
	}
	if json.Unmarshal([]byte(policy), &snapshot) == nil && snapshot.Mode == "shared" {
		if !retentionEnabled {
			return ""
		}
		return "mirror-and-direct"
	}
	return "mirror"
}
