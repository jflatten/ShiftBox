package guest

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

func (d devContainer) prepareProjectOwner(ctx context.Context, containerID, remoteUser string) error {
	run := d.run
	if run == nil {
		run = runDevContainerCLI
	}
	args := []string{"container", "exec"}
	if remoteUser != "" {
		args = append(args, "--user", remoteUser)
	}
	output, err := run(ctx, "/usr/bin/docker", append(args, "--", containerID, "/bin/sh", "-c", `printf '%s %s\n' "$(id -u)" "$(id -g)"`)...)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("could not resolve the Dev Container developer identity")
	}
	ids := strings.Fields(string(output))
	if len(ids) != 2 {
		return errors.New("invalid Dev Container developer identity")
	}
	uid, uidErr := strconv.ParseUint(ids[0], 10, 31)
	gid, gidErr := strconv.ParseUint(ids[1], 10, 31)
	if uidErr != nil || gidErr != nil {
		return errors.New("invalid Dev Container developer identity")
	}
	// The receipt stays with agent state, outside the mounted checkout. A
	// normal restart preserves any individual ownership changes in the tree.
	receipt := d.workspace + ".owner"
	current, err := os.ReadFile(receipt)
	owner := strconv.FormatUint(uid, 10) + ":" + strconv.FormatUint(gid, 10)
	if err == nil && string(current) == owner {
		return nil
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	setOwner := d.setProjectOwner
	if setOwner == nil {
		setOwner = assignTreeOwner
	}
	if err := setOwner(ctx, d.workspace, int(uid), int(gid)); err != nil {
		return err
	}
	return writePrivateState(receipt, []byte(owner))
}

// assignTreeOwner changes ownership within a retained directory without
// following symlinks or touching entries that already have the requested owner.
func assignTreeOwner(ctx context.Context, directory string, uid, gid int) error {
	root, err := os.OpenRoot(directory)
	if err != nil {
		return err
	}
	defer root.Close()
	return fs.WalkDir(root.FS(), ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		owner := info.Sys().(*syscall.Stat_t)
		if int(owner.Uid) == uid && int(owner.Gid) == gid {
			return nil
		}
		return root.Lchown(filepath.FromSlash(path), uid, gid)
	})
}
