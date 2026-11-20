package guest

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/creack/pty"
	"golang.org/x/crypto/ssh"
)

type workspaceSSHTerminal struct {
	Term           string
	Width, Height  uint32
	PixelW, PixelH uint32
	Modes          string
}

type workspaceSSHWindow struct {
	Width, Height  uint32
	PixelW, PixelH uint32
}

func (t workspaceSSHTerminal) size() *pty.Winsize {
	// Forced OpenSSH terminals have no local dimensions when stdin is a pipe.
	width, height := t.Width, t.Height
	if width == 0 {
		width = 80
	}
	if height == 0 {
		height = 24
	}
	return (workspaceSSHWindow{Width: width, Height: height}).size()
}

func (w workspaceSSHWindow) size() *pty.Winsize {
	if w.Width == 0 || w.Width > 65535 || w.Height == 0 || w.Height > 65535 {
		return nil
	}
	return &pty.Winsize{Cols: uint16(w.Width), Rows: uint16(w.Height)}
}

func serveWorkspaceSSHSession(ctx context.Context, target workspaceSSHTarget, incoming ssh.NewChannel) {
	channel, requests, err := incoming.Accept()
	if err != nil {
		return
	}
	defer channel.Close()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var terminal workspaceSSHTerminal
	for request := range requests {
		if request.Type == "pty-req" {
			var proposed workspaceSSHTerminal
			if terminal.Term != "" || ssh.Unmarshal(request.Payload, &proposed) != nil ||
				proposed.Term == "" || len(proposed.Term) > 64 || strings.ContainsAny(proposed.Term, "\x00\r\n=") ||
				proposed.size() == nil {
				_ = request.Reply(false, nil)
				continue
			}
			terminal = proposed
			_ = request.Reply(true, nil)
			continue
		}
		args := workspaceSSHSessionArgs(request.Type, request.Payload)
		if len(args) == 0 || request.Type == "subsystem" && terminal.Term != "" {
			_ = request.Reply(false, nil)
			continue
		}
		cmd := target.command(ctx, terminal.Term, args)
		cmd.WaitDelay = 2 * time.Second
		file, done, err := startWorkspaceSSHProgram(ctx, cmd, channel, terminal)
		_ = request.Reply(err == nil, nil)
		if err == nil {
			waitWorkspaceSSHProgram(ctx, cancel, channel, requests, file, done)
		}
		return
	}
}

func workspaceSSHSessionArgs(name string, payload []byte) []string {
	switch name {
	case "shell":
		if len(payload) == 0 {
			return []string{"/bin/sh", "-c", `exec "${SHELL:-/bin/sh}"`}
		}
	case "exec":
		var request struct{ Command string }
		if ssh.Unmarshal(payload, &request) == nil && !strings.ContainsRune(request.Command, 0) {
			return []string{"/bin/sh", "-c", `exec "${SHELL:-/bin/sh}" -c "$1"`, "shiftbox-ssh", request.Command}
		}
	case "subsystem":
		var request struct{ Name string }
		if ssh.Unmarshal(payload, &request) == nil && request.Name == "sftp" {
			return []string{workspaceSSHBinary, "ssh-sftp"}
		}
	}
	return nil
}

func startWorkspaceSSHProgram(ctx context.Context, cmd *exec.Cmd, channel ssh.Channel, terminal workspaceSSHTerminal) (*os.File, <-chan uint32, error) {
	manageWorkspaceSSHProcessGroup(cmd, terminal.Term != "")
	done := make(chan uint32, 1)
	if terminal.Term == "" {
		stdin, err := cmd.StdinPipe()
		if err != nil {
			return nil, nil, err
		}
		cmd.Stdout, cmd.Stderr = channel, channel.Stderr()
		if err := cmd.Start(); err != nil {
			_ = stdin.Close()
			return nil, nil, err
		}
		go func() {
			defer stdin.Close()
			_, _ = io.Copy(stdin, channel)
		}()
		go func() { done <- workspaceSSHExitStatus(cmd.Wait()) }()
		return nil, done, nil
	}
	file, err := pty.StartWithSize(cmd, terminal.size())
	if err != nil {
		return nil, nil, err
	}
	output := make(chan struct{})
	go func() { _, _ = io.Copy(file, channel) }()
	go func() {
		defer close(output)
		_, _ = io.Copy(channel, file)
	}()
	go func() {
		status := workspaceSSHExitStatus(cmd.Wait())
		select {
		case <-output:
		case <-ctx.Done():
		case <-time.After(2 * time.Second):
		}
		_ = file.Close()
		done <- status
	}()
	return file, done, nil
}

// The Dev Container CLI starts Docker as a subprocess. Revoke both processes
// together so they cannot keep the exec session's pipes or terminal open.
func manageWorkspaceSSHProcessGroup(cmd *exec.Cmd, terminal bool) {
	if !terminal {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	}
	// pty.StartWithSize creates a new session for interactive commands.
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
}

func waitWorkspaceSSHProgram(ctx context.Context, cancel context.CancelFunc, channel ssh.Channel, requests <-chan *ssh.Request, terminal *os.File, done <-chan uint32) {
	for {
		select {
		case <-ctx.Done():
			_ = channel.Close()
			<-done
			return
		case status := <-done:
			_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{status}))
			return
		case request, ok := <-requests:
			if !ok {
				cancel()
				_ = channel.Close()
				<-done
				return
			}
			var window workspaceSSHWindow
			resized := false
			if request.Type == "window-change" && terminal != nil && ssh.Unmarshal(request.Payload, &window) == nil {
				if size := window.size(); size != nil {
					resized = pty.Setsize(terminal, size) == nil
				}
			}
			_ = request.Reply(resized, nil)
		}
	}
}

func workspaceSSHExitStatus(err error) uint32 {
	if err == nil {
		return 0
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() > 0 {
		return uint32(exit.ExitCode())
	}
	return 1
}
