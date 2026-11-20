package guest

import (
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"os"
	"strconv"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

type workspaceSSHDirectTCP struct {
	Host       string
	Port       uint32
	OriginHost string
	OriginPort uint32
}

func workspaceSSHForwardAddress(host string, port uint32) string {
	if host == "localhost" {
		host = "127.0.0.1"
	}
	addr, err := netip.ParseAddr(host)
	if err != nil || !addr.IsLoopback() || addr.Zone() != "" || port == 0 || port > 65535 {
		return ""
	}
	return netip.AddrPortFrom(addr, uint16(port)).String()
}

func serveWorkspaceSSHForward(ctx context.Context, target workspaceSSHTarget, incoming ssh.NewChannel) {
	var request workspaceSSHDirectTCP
	if !target.forwarding || ssh.Unmarshal(incoming.ExtraData(), &request) != nil ||
		workspaceSSHForwardAddress(request.Host, request.Port) == "" {
		_ = incoming.Reject(ssh.Prohibited, "workspace forwarding requires primary-container loopback")
		return
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := target.command(ctx, "", []string{workspaceSSHBinary, "ssh-forward", request.Host, strconv.FormatUint(uint64(request.Port), 10)})
	manageWorkspaceSSHProcessGroup(cmd, false)
	input, err := cmd.StdinPipe()
	if err != nil {
		_ = incoming.Reject(ssh.ConnectionFailed, "could not start workspace forwarding")
		return
	}
	defer input.Close()
	output, err := cmd.StdoutPipe()
	if err != nil {
		_ = incoming.Reject(ssh.ConnectionFailed, "could not start workspace forwarding")
		return
	}
	defer output.Close()
	if err := cmd.Start(); err != nil {
		_ = incoming.Reject(ssh.ConnectionFailed, "could not start workspace forwarding")
		return
	}
	defer func() {
		cancel()
		_ = cmd.Wait()
	}()
	var ready [1]byte
	setup := time.AfterFunc(sshSetupTimeout, cancel)
	_, err = io.ReadFull(output, ready[:])
	setup.Stop()
	if err != nil || ready[0] != 1 {
		_ = incoming.Reject(ssh.ConnectionFailed, "could not reach primary-container loopback")
		return
	}
	channel, requests, err := incoming.Accept()
	if err != nil {
		return
	}
	defer channel.Close()
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case request, open := <-requests:
				if !open {
					cancel()
					return
				}
				_ = request.Reply(false, nil)
			}
		}
	}()
	go func() {
		defer input.Close()
		_, _ = io.Copy(input, channel)
	}()
	_, _ = io.Copy(channel, output)
}

// RunWorkspaceSSHCommand serves editor transfer or loopback forwarding over
// stdio. The guest starts it through Dev Container CLI in the developer context.
func RunWorkspaceSSHCommand(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer) error {
	switch {
	case len(args) == 1 && args[0] == "ssh-sftp":
		path, err := os.Getwd()
		if err != nil {
			return err
		}
		stream := workspaceSSHStdio{Reader: stdin, Writer: stdout}
		server, err := sftp.NewServer(stream, sftp.WithServerWorkingDirectory(path))
		if err != nil {
			return err
		}
		defer server.Close()
		interrupt := context.AfterFunc(ctx, func() { _ = stream.Close() })
		defer interrupt()
		if err := server.Serve(); err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		return nil
	case len(args) == 3 && args[0] == "ssh-forward":
		port, err := strconv.ParseUint(args[2], 10, 16)
		address := workspaceSSHForwardAddress(args[1], uint32(port))
		if err == nil && address != "" {
			return workspaceSSHForward(ctx, address, stdin, stdout)
		}
	}
	return errors.New("invalid workspace SSH command")
}

type workspaceSSHStdio struct {
	io.Reader
	io.Writer
}

func (s workspaceSSHStdio) Close() error {
	if input, ok := s.Reader.(io.Closer); ok {
		return input.Close()
	}
	return nil
}

func workspaceSSHForward(ctx context.Context, address string, stdin io.Reader, stdout io.Writer) error {
	dialer := net.Dialer{Timeout: 5 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return err
	}
	defer conn.Close()
	interrupt := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer interrupt()
	if _, err := stdout.Write([]byte{1}); err != nil {
		return err
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = io.Copy(conn, stdin)
		if socket, ok := conn.(interface{ CloseWrite() error }); ok {
			_ = socket.CloseWrite()
		}
	}()
	_, err = io.Copy(stdout, conn)
	_ = conn.Close()
	if input, ok := stdin.(io.Closer); ok {
		_ = input.Close()
		<-done
	}
	return err
}
