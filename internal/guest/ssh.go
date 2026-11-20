package guest

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

const (
	workspaceSSHBinary = credentialDirectory + "/shiftbox"
	sshSetupTimeout    = 10 * time.Second
)

func newWorkspaceSSH(stateDir string, runtime devContainer) (*sshTunnelService, error) {
	signer, err := loadWorkspaceSSHKey(filepath.Join(stateDir, "ssh-host-key"))
	if err != nil {
		return nil, err
	}
	// The private reverse-tunnel stream is opened only by the authorized
	// controller. The SSH username cannot select a VM or developer account.
	config := &ssh.ServerConfig{NoClientAuth: true}
	config.AddHostKey(signer)
	return &sshTunnelService{
		hostKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey()))),
		serve: func(ctx context.Context, conn net.Conn) {
			serveWorkspaceSSH(ctx, conn, runtime, config)
		},
	}, nil
}

func loadWorkspaceSSHKey(path string) (ssh.Signer, error) {
	info, err := os.Lstat(path)
	var seed []byte
	switch {
	case errors.Is(err, os.ErrNotExist):
		seed = make([]byte, ed25519.SeedSize)
		if _, err := rand.Read(seed); err != nil {
			return nil, err
		}
		if err := writePrivateState(path, seed); err != nil {
			return nil, err
		}
	case err != nil:
		return nil, err
	default:
		if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() != ed25519.SeedSize {
			return nil, errors.New("invalid workspace SSH host key file")
		}
		seed, err = os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		if len(seed) != ed25519.SeedSize {
			return nil, errors.New("invalid workspace SSH host key")
		}
	}
	return ssh.NewSignerFromKey(ed25519.NewKeyFromSeed(seed))
}

type workspaceSSHTarget struct {
	runtime    devContainer
	id         string
	forwarding bool
}

func (d devContainer) workspaceSSHTarget(ctx context.Context) (workspaceSSHTarget, error) {
	if !filepath.IsAbs(d.workspace) {
		return workspaceSSHTarget{}, errors.New("invalid workspace SSH project path")
	}
	const docker = "/usr/bin/docker"
	run := d.runner()
	out, err := run(ctx, docker, "container", "ls", "--quiet", "--no-trunc", "--filter", "label="+developmentLabel)
	id := strings.TrimSpace(string(out))
	if err != nil || !dockerContainerID.MatchString(id) {
		return workspaceSSHTarget{}, errors.New("workspace SSH requires exactly one running primary container")
	}
	out, err = run(ctx, docker, "container", "inspect", "--format", "{{.HostConfig.NetworkMode}}", "--", id)
	if err != nil {
		return workspaceSSHTarget{}, errors.New("could not inspect the primary container network")
	}
	config, err := d.workspaceSSHConfig()
	if err != nil {
		return workspaceSSHTarget{}, err
	}
	d.config = config
	return workspaceSSHTarget{runtime: d, id: id, forwarding: workspaceSSHForwarding(strings.TrimSpace(string(out)))}, nil
}

func (d devContainer) workspaceSSHConfig() (string, error) {
	config := d.config
	if config == "" && d.repository != nil {
		// Use the same captured config as the browser IDE even when the
		// operation journal now holds a later maintenance or update receipt.
		root, err := os.OpenRoot(d.workspace)
		if err != nil {
			return "", err
		}
		defer root.Close()
		file, err := root.Open(repositoryReceipt("."))
		if err != nil {
			return "", err
		}
		defer file.Close()
		var evidence repositoryEvidence
		if err := decodeAgentJSON(file, &evidence); err != nil || evidence.Path != d.workspace || evidence.Config == "" {
			return "", errors.New("workspace SSH checkout configuration is unavailable")
		}
		config = evidence.Config
	}
	if config != "" && !validDevContainerPath(config) {
		return "", errors.New("invalid workspace SSH checkout configuration")
	}
	return config, nil
}

func workspaceSSHForwarding(mode string) bool {
	return mode != "" && mode != "host" && !strings.HasPrefix(mode, "container:")
}

func (t workspaceSSHTarget) command(ctx context.Context, terminal string, command []string) *exec.Cmd {
	// JSON log format forces terminal mode in the Dev Container CLI even
	// for piped commands. Text mode keeps the SFTP and forwarding bytes raw.
	args := t.runtime.commandBase("exec", "/usr/bin/docker", "text")
	if t.runtime.config != "" {
		args = append(args, "--config", filepath.Join(t.runtime.workspace, t.runtime.config))
	}
	args = append(args, "--container-id", t.id)
	if terminal != "" {
		args = append(args, "--remote-env", "TERM="+terminal)
	}
	args = append(args, command...)
	if t.runtime.sshExec != nil {
		return t.runtime.sshExec(ctx, args...)
	}
	return exec.CommandContext(ctx, devContainerCLI, args...)
}

func serveWorkspaceSSH(ctx context.Context, conn net.Conn, runtime devContainer, config *ssh.ServerConfig) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer conn.Close()
	interrupt := context.AfterFunc(ctx, func() {
		_ = conn.SetDeadline(time.Now())
		_ = conn.Close()
	})
	defer interrupt()
	setup, stopSetup := context.WithTimeout(ctx, sshSetupTimeout)
	target, err := runtime.workspaceSSHTarget(setup)
	stopSetup()
	if err != nil {
		return
	}
	_ = conn.SetDeadline(time.Now().Add(sshSetupTimeout))
	server, channels, requests, err := ssh.NewServerConn(conn, config)
	if err != nil {
		return
	}
	defer server.Close()
	_ = conn.SetDeadline(time.Time{})
	var workers sync.WaitGroup
	workers.Go(func() { ssh.DiscardRequests(requests) })
	slots := make(chan struct{}, 32)
	for incoming := range channels {
		select {
		case slots <- struct{}{}:
			workers.Go(func() {
				defer func() { <-slots }()
				switch incoming.ChannelType() {
				case "session":
					serveWorkspaceSSHSession(ctx, target, incoming)
				case "direct-tcpip":
					serveWorkspaceSSHForward(ctx, target, incoming)
				default:
					_ = incoming.Reject(ssh.UnknownChannelType, "unsupported workspace SSH channel")
				}
			})
		default:
			_ = incoming.Reject(ssh.ResourceShortage, "too many workspace SSH channels")
		}
	}
	cancel()
	workers.Wait()
}
