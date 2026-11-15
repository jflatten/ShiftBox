package guest

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func (d devContainer) retainedHomeRoot() string {
	return filepath.Join(filepath.Dir(d.workspace), "retained-home")
}

func (d devContainer) retainedHomeData() string {
	return filepath.Join(d.retainedHomeRoot(), "data")
}

func (d devContainer) retainedHome() (string, error) {
	root := d.retainedHomeRoot()
	info, err := os.Lstat(root)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil || !info.IsDir() {
		return "", errors.New("retained developer home is unavailable")
	}
	target, err := os.ReadFile(filepath.Join(root, "target"))
	if err != nil || !validHomeTarget(string(target)) {
		return "", errors.New("invalid retained developer home path")
	}
	info, err = os.Lstat(d.retainedHomeData())
	if err != nil || !info.IsDir() {
		return "", errors.New("retained developer home directory is unavailable")
	}
	return string(target), nil
}

func validHomeTarget(target string) bool {
	if !filepath.IsAbs(target) || filepath.Clean(target) != target || strings.ContainsAny(target, "\x00\r\n,") {
		return false
	}
	if target == "/" {
		return false
	}
	for _, reserved := range []string{ideDirectory, credentialDirectory, codeServerDir, "/usr/local/bin/shiftbox"} {
		if target == reserved || strings.HasPrefix(target, reserved+"/") || strings.HasPrefix(reserved, target+"/") {
			return false
		}
	}
	return true
}

func (d devContainer) ensureRetainedHome(ctx context.Context, result devContainerResult, options []string) (devContainerResult, error) {
	run := d.runner()
	target, err := d.retainedHome()
	if err != nil {
		return devContainerResult{}, err
	}
	if target == "" {
		target, err = d.seedRetainedHome(ctx, result, run)
		if err != nil {
			return devContainerResult{}, err
		}
	}
	mounted, err := d.hasRetainedHomeMount(ctx, result.ID, target, run)
	if err != nil {
		return devContainerResult{}, err
	}
	if !mounted {
		previous := result.ID
		result, err = d.up(ctx, append(options, "--remove-existing-container")...)
		if err != nil {
			return devContainerResult{}, err
		}
		if !dockerContainerID.MatchString(result.ID) || result.ID == previous || result.Merged == nil {
			return devContainerResult{}, errors.New("Dev Container did not replace the unmounted home")
		}
		mounted, err = d.hasRetainedHomeMount(ctx, result.ID, target, run)
		if err != nil {
			return devContainerResult{}, err
		}
	}
	if !mounted {
		return devContainerResult{}, errors.New("Dev Container did not mount the retained home")
	}
	if err := d.verifyHomeOwner(ctx, result, target); err != nil {
		return devContainerResult{}, err
	}
	return result, nil
}

func (d devContainer) verifyHomeOwner(ctx context.Context, result devContainerResult, target string) error {
	actual, uid, gid, err := developerHome(ctx, result, d.runner())
	if err != nil {
		return err
	}
	if actual != target {
		return errors.New("Dev Container developer home changed")
	}
	expected, err := os.ReadFile(filepath.Join(d.retainedHomeRoot(), "owner"))
	if err != nil || string(expected) != homeOwner(uid, gid) {
		return errors.New("Dev Container developer home ownership changed")
	}
	return nil
}

func (d devContainer) hasRetainedHomeMount(ctx context.Context, id, target string, run func(context.Context, string, ...string) ([]byte, error)) (bool, error) {
	mounts, err := containerMounts(ctx, id, run)
	if err != nil {
		return false, err
	}
	return retainedHomeMounted(mounts, d.retainedHomeData(), target)
}

type dockerMount struct {
	Type, Source, Destination, Name string
	RW                              bool
}

func containerMounts(ctx context.Context, id string, run func(context.Context, string, ...string) ([]byte, error)) ([]dockerMount, error) {
	out, err := run(ctx, "/usr/bin/docker", "container", "inspect", "--format", "{{json .Mounts}}", "--", id)
	if err != nil {
		return nil, err
	}
	var mounts []dockerMount
	if err := json.Unmarshal(out, &mounts); err != nil {
		return nil, errors.New("invalid Dev Container mount inventory")
	}
	return mounts, nil
}

func retainedHomeMounted(mounts []dockerMount, source, target string) (bool, error) {
	for _, mount := range mounts {
		if mount.Destination == target {
			if mount.Type == "bind" && mount.Source == source && mount.RW {
				return true, nil
			}
			return false, errors.New("Dev Container already has a different mount at the developer home")
		}
	}
	return false, nil
}

func developerHome(ctx context.Context, result devContainerResult, run func(context.Context, string, ...string) ([]byte, error)) (string, int, int, error) {
	args := []string{"container", "exec"}
	if result.Merged.RemoteUser != "" {
		args = append(args, "--user", result.Merged.RemoteUser)
	}
	args = append(args, "--", result.ID, "/bin/sh", "-c",
		`set -eu; uid=$(id -u); gid=$(id -g); entry=$(getent passwd "$uid"); IFS=: read -r name pass uid_field gid_field gecos home shell <<EOF
$entry
EOF
printf '%s %s\n%s\n' "$uid" "$gid" "$home"`)
	out, err := run(ctx, "/usr/bin/docker", args...)
	if err != nil {
		return "", 0, 0, err
	}
	ids, rawTarget, _ := strings.Cut(string(out), "\n")
	target := strings.TrimSuffix(rawTarget, "\n")
	numbers := strings.Fields(ids)
	if !validHomeTarget(target) || len(numbers) != 2 {
		return "", 0, 0, errors.New("invalid Dev Container developer home")
	}
	uid, uidErr := strconv.ParseUint(numbers[0], 10, 31)
	gid, gidErr := strconv.ParseUint(numbers[1], 10, 31)
	if uidErr != nil || gidErr != nil {
		return "", 0, 0, errors.New("invalid Dev Container developer identity")
	}
	return target, int(uid), int(gid), nil
}

func (d devContainer) seedRetainedHome(ctx context.Context, result devContainerResult, run func(context.Context, string, ...string) ([]byte, error)) (string, error) {
	target, uid, gid, err := developerHome(ctx, result, run)
	if err != nil {
		return "", err
	}
	if _, err := d.hasRetainedHomeMount(ctx, result.ID, target, run); err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp(filepath.Dir(d.workspace), ".retained-home-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	data := filepath.Join(dir, "data")
	if err := os.Mkdir(data, 0700); err != nil {
		return "", err
	}
	if _, err := run(ctx, "/usr/bin/docker", "cp", "-a", result.ID+":"+target+"/.", data); err != nil {
		return "", err
	}
	if err := os.Chown(data, uid, gid); err != nil {
		return "", err
	}
	if err := writePrivateState(filepath.Join(dir, "target"), []byte(target)); err != nil {
		return "", err
	}
	if err := writePrivateState(filepath.Join(dir, "owner"), []byte(homeOwner(uid, gid))); err != nil {
		return "", err
	}
	if err := os.Rename(dir, d.retainedHomeRoot()); err != nil {
		return "", err
	}
	parent, err := os.Open(filepath.Dir(d.workspace))
	if err != nil {
		return "", err
	}
	defer parent.Close()
	return target, parent.Sync()
}

func homeOwner(uid, gid int) string {
	return strconv.Itoa(uid) + ":" + strconv.Itoa(gid)
}
