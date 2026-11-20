package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/sys/unix"
)

const (
	sshConfigStart = "# BEGIN SHIFTBOX WORKSPACE SSH"
	sshConfigEnd   = "# END SHIFTBOX WORKSPACE SSH"
	sshAliasPrefix = "shiftbox-"
)

type sshConfigPaths struct {
	ConfigFile       string
	AliasStateFile   string
	HelperExecutable string
	PinDir           string
}

type sshAliasWorkspace struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// reconcileSSHConfig writes the current authoritative discovery snapshot. The
// private alias history is retained separately, including ineligible workspaces.
func reconcileSSHConfig(paths sshConfigPaths, origin, accountID string, eligible []sshAliasWorkspace) error {
	origin, err := canonicalSSHOrigin(origin)
	if err != nil {
		return err
	}
	if (accountID == "" && len(eligible) > 0) || strings.IndexFunc(accountID, unicode.IsControl) >= 0 {
		return errors.New("invalid workspace SSH account")
	}
	for _, path := range []string{paths.ConfigFile, paths.AliasStateFile, paths.HelperExecutable, paths.PinDir} {
		if !filepath.IsAbs(path) || strings.IndexFunc(path, unicode.IsControl) >= 0 {
			return errors.New("workspace SSH paths must be absolute and contain no control characters")
		}
	}
	if err := os.MkdirAll(filepath.Dir(paths.AliasStateFile), 0700); err != nil {
		return err
	}
	lock, err := os.OpenFile(paths.AliasStateFile+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX); err != nil {
		return err
	}

	// Replace the target of a dotfiles symlink, retaining the user's symlink.
	configPath, err := filepath.EvalSymlinks(paths.ConfigFile)
	if errors.Is(err, os.ErrNotExist) {
		if info, statErr := os.Lstat(paths.ConfigFile); statErr == nil && info.Mode()&os.ModeSymlink != 0 {
			return errors.New("SSH config symlink has no readable target")
		}
		configPath = paths.ConfigFile
	} else if err != nil {
		return err
	}
	config, mode, err := readSSHUserConfig(configPath)
	if err != nil {
		return err
	}
	userConfig, err := withoutSSHManagedBlock(config)
	if err != nil {
		return err
	}
	if accountID == "" {
		// An unpaired helper still owns its previous block. Clear that block
		// without depending on which account installed it or rewriting history.
		return replaceSSHFile(configPath, config, userConfig, mode)
	}
	previous, err := os.ReadFile(paths.AliasStateFile)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	histories := make(map[string]map[string]string)
	if len(previous) > 0 {
		if err := json.Unmarshal(previous, &histories); err != nil || histories == nil {
			return errors.New("invalid workspace SSH alias history")
		}
	}
	scope := origin + "\x00" + accountID
	names := histories[scope]
	if names == nil {
		names = make(map[string]string)
		histories[scope] = names
	}
	used, err := sshUserAliases(userConfig, filepath.Dir(paths.ConfigFile))
	if err != nil {
		return err
	}
	// A command using a saved alias from a different account must never resolve
	// to the current account's similarly named workspace.
	for olderScope, olderNames := range histories {
		if olderScope == scope {
			continue
		}
		for _, alias := range olderNames {
			used[alias] = true
		}
	}
	managed, err := renderSSHManagedBlock(paths, origin, accountID, eligible, names, used)
	if err != nil {
		return err
	}
	state, err := json.MarshalIndent(histories, "", "  ")
	if err != nil {
		return err
	}
	// Reserving new names before the SSH config replacement keeps the same
	// aliases on retry, even if updating the config fails.
	if err := replaceSSHFile(paths.AliasStateFile, previous, append(state, '\n'), 0600); err != nil {
		return err
	}
	return replaceSSHFile(configPath, config, append(managed, userConfig...), mode)
}

func renderSSHManagedBlock(paths sshConfigPaths, origin, accountID string, eligible []sshAliasWorkspace,
	history map[string]string, used map[string]bool,
) ([]byte, error) {
	for id, alias := range history {
		if !validManagedAlias(alias) || used[alias] {
			delete(history, id)
			continue
		}
		used[alias] = true
	}
	if len(eligible) == 0 {
		return nil, nil
	}
	workspaces := slices.Clone(eligible)
	slices.SortFunc(workspaces, func(a, b sshAliasWorkspace) int { return strings.Compare(a.ID, b.ID) })
	var block bytes.Buffer
	fmt.Fprintln(&block, sshConfigStart)
	lastID := ""
	for _, workspace := range workspaces {
		hostKeyAlias, knownHosts, err := sshHostPin(origin, workspace.ID, paths.PinDir)
		if err != nil {
			return nil, err
		}
		if workspace.ID == lastID {
			return nil, errors.New("duplicate workspace SSH identity")
		}
		lastID = workspace.ID
		host := history[workspace.ID]
		if host == "" {
			stem := sshAliasStem(workspace.Name)
			host = stem
			for suffix := 2; used[host]; suffix++ {
				host = stem + "-" + strconv.Itoa(suffix)
			}
			history[workspace.ID] = host
			used[host] = true
		}
		fmt.Fprintf(&block, "\nHost %s\n", host)
		fmt.Fprintf(&block, "    HostName %s\n", host)
		fmt.Fprintf(&block, "    ProxyCommand exec %s proxy --origin %s --account %s --workspace %s --pin-dir %s\n",
			sshShellArgument(paths.HelperExecutable), sshShellArgument(origin), sshShellArgument(accountID),
			sshShellArgument(workspace.ID), sshShellArgument(paths.PinDir))
		fmt.Fprintf(&block, "    HostKeyAlias %s\n", hostKeyAlias)
		fmt.Fprintf(&block, "    UserKnownHostsFile %s\n", strconv.Quote(strings.ReplaceAll(knownHosts, "%", "%%")))
		block.WriteString("    GlobalKnownHostsFile none\n    KnownHostsCommand none\n    StrictHostKeyChecking yes\n" +
			"    UpdateHostKeys no\n    VerifyHostKeyDNS no\n    CheckHostIP no\n    HostKeyAlgorithms ssh-ed25519\n" +
			"    ProxyUseFdpass no\n    ControlMaster no\n    ControlPath none\n" +
			"    CanonicalizeHostname no\n    ConnectTimeout 270\n")
	}
	// Reopen the top-level user scope after our literal Host stanzas.
	fmt.Fprintf(&block, "\nHost *\n%s\n", sshConfigEnd)
	return block.Bytes(), nil
}

func withoutSSHManagedBlock(config []byte) ([]byte, error) {
	var user bytes.Buffer
	inside, seen := false, false
	for _, line := range bytes.SplitAfter(config, []byte{'\n'}) {
		switch string(bytes.TrimRight(line, "\r\n")) {
		case sshConfigStart:
			if seen {
				return nil, errors.New("duplicate ShiftBox workspace SSH config block")
			}
			inside, seen = true, true
		case sshConfigEnd:
			if !inside {
				return nil, errors.New("unmatched ShiftBox workspace SSH config block end")
			}
			inside = false
		default:
			if !inside {
				user.Write(line)
			}
		}
	}
	if inside {
		return nil, errors.New("incomplete ShiftBox workspace SSH config block")
	}
	return user.Bytes(), nil
}

var sshConfigWord = regexp.MustCompile(`"[^"]*"|'[^']*'|[^[:space:]=]+`)

func sshUserAliases(config []byte, dir string) (map[string]bool, error) {
	aliases := make(map[string]bool)
	seen := make(map[string]bool)
	var collect func([]byte, int) error
	collect = func(config []byte, depth int) error {
		if depth > 16 {
			return errors.New("SSH config includes are nested too deeply")
		}
		for _, line := range strings.Split(string(config), "\n") {
			line, _, _ = strings.Cut(line, "#")
			fields := sshConfigWord.FindAllString(line, -1)
			if len(fields) == 0 {
				continue
			}
			switch strings.ToLower(fields[0]) {
			case "host":
				for _, field := range fields[1:] {
					host := strings.Trim(field, "\"'")
					if !strings.ContainsAny(host, "*?!") {
						aliases[strings.ToLower(host)] = true
					}
				}
			case "include":
				for _, field := range fields[1:] {
					pattern := os.ExpandEnv(strings.Trim(field, "\"'"))
					if tail, ok := strings.CutPrefix(pattern, "~/"); ok {
						pattern = filepath.Join(filepath.Dir(dir), tail)
					}
					if !filepath.IsAbs(pattern) {
						pattern = filepath.Join(dir, pattern)
					}
					matches, err := filepath.Glob(pattern)
					if err != nil {
						return err
					}
					for _, match := range matches {
						if seen[match] {
							continue
						}
						seen[match] = true
						included, _, err := readSSHUserConfig(match)
						if err != nil {
							return err
						}
						if err := collect(included, depth+1); err != nil {
							return err
						}
					}
				}
			}
		}
		return nil
	}
	return aliases, collect(config, 0)
}

func sshAliasStem(name string) string {
	stem := strings.Join(strings.FieldsFunc(strings.ToLower(name), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9')
	}), "-")
	if len(stem) > 40 {
		stem = strings.TrimRight(stem[:40], "-")
	}
	if stem == "" {
		stem = "workspace"
	}
	return sshAliasPrefix + stem
}

func validManagedAlias(alias string) bool {
	return strings.HasPrefix(alias, sshAliasPrefix) && len(alias) <= 63 && len(alias) > len(sshAliasPrefix) &&
		strings.IndexFunc(alias, func(r rune) bool {
			return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-')
		}) < 0
}

func sshShellArgument(value string) string {
	value = strings.ReplaceAll(value, "%", "%%")
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func readSSHUserConfig(path string) ([]byte, os.FileMode, error) {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, 0600, nil
	}
	if err != nil {
		return nil, 0, err
	}
	if !info.Mode().IsRegular() {
		return nil, 0, errors.New("SSH config is not a regular file")
	}
	data, err := os.ReadFile(path)
	return data, info.Mode().Perm(), err
}

func replaceSSHFile(path string, previous, next []byte, mode os.FileMode) error {
	if bytes.Equal(previous, next) {
		return nil
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(dir, ".shiftbox-ssh-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	_, writeErr := file.Write(next)
	modeErr := file.Chmod(mode)
	if err := errors.Join(writeErr, modeErr, file.Close()); err != nil {
		return err
	}
	current, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if !bytes.Equal(current, previous) {
		return errors.New("workspace SSH config changed during reconciliation; retry")
	}
	return os.Rename(file.Name(), path)
}
