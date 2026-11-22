package guest

import (
	"errors"
	"io"
	"os"
	"path/filepath"
)

const workspaceLauncher = "#!/bin/sh\nexec /run/shiftbox-credentials/shiftbox \"$@\"\n"

// prepareWorkspaceCommand publishes the actually selected, signed guest agent.
// A stable launcher is file-mounted into the container; its directory-mounted
// binary can then be atomically replaced without pinning an obsolete inode.
func prepareWorkspaceCommand(executable, directory string) error {
	input, err := os.Open(executable)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.CreateTemp(directory, ".shiftbox-command-*")
	if err != nil {
		return err
	}
	defer os.Remove(output.Name())
	if _, err := io.Copy(output, input); err != nil {
		output.Close()
		return err
	}
	if err := output.Chmod(0755); err != nil {
		output.Close()
		return err
	}
	if err := output.Close(); err != nil {
		return err
	}
	if err := os.Rename(output.Name(), filepath.Join(directory, "shiftbox")); err != nil {
		return err
	}
	launcher := filepath.Join(directory, "shiftbox-launcher")
	if current, err := os.ReadFile(launcher); err == nil {
		if string(current) != workspaceLauncher {
			return errors.New("workspace command launcher is unexpected")
		}
		return os.Chmod(launcher, 0755)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.WriteFile(launcher, []byte(workspaceLauncher), 0755); err != nil {
		return err
	}
	// The guest supervisor uses umask 0077. The container's remote user still
	// needs execute permission on this deliberately shared helper.
	return os.Chmod(launcher, 0755)
}
