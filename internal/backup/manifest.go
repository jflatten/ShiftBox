package backup

import (
	"encoding/hex"
	"slices"
	"strings"
)

const archiveVersion = 1

// ArchiveMetadata identifies the installation and application schema captured.
type ArchiveMetadata struct {
	InstallationID string `json:"installationId"`
	Release        string `json:"release"`
	Profile        string `json:"profile"`
	Schema         int64  `json:"schema"`
}

type manifest struct {
	Version  int             `json:"version"`
	Metadata ArchiveMetadata `json:"metadata"`
	Files    []archiveFile   `json:"files"`
}

type archiveFile struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	Digest string `json:"sha256"`
}

func (m ArchiveMetadata) valid() bool {
	return strings.TrimSpace(m.InstallationID) != "" && len(m.InstallationID) <= 128 &&
		strings.TrimSpace(m.Release) != "" && len(m.Release) <= 128 && m.Schema > 0 &&
		(m.Profile == "sqlite" || m.Profile == "postgres")
}

func archiveNames(profile string) []string {
	names := []string{"configuration.json", "guest-ca.json", "installation.key"}
	if profile == "postgres" {
		names = append(names, "application.dump", "dex.dump")
	} else {
		names = append(names, "application.db", "dex.db")
	}
	slices.Sort(names)
	return names
}

func (m manifest) valid() bool {
	if m.Version != archiveVersion || !m.Metadata.valid() {
		return false
	}
	names := archiveNames(m.Metadata.Profile)
	if len(names) != len(m.Files) {
		return false
	}
	for index, item := range m.Files {
		if item.Name != names[index] || item.Size <= 0 || !archiveDigest(item.Digest) {
			return false
		}
	}
	return true
}

func archiveDigest(input string) bool {
	if len(input) != 64 || strings.ToLower(input) != input {
		return false
	}
	_, err := hex.DecodeString(input)
	return err == nil
}
