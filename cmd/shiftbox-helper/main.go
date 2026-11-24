// ShiftBox Local Helper pairs a Mac with one controller and keeps its eligible
// workspace aliases available to native OpenSSH.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := helperCommand(ctx, os.Args[1:]); err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, flag.ErrHelp) {
		fmt.Fprintln(os.Stderr, "ShiftBox Local Helper:", err)
		if errors.Is(err, errSSHSignInRequired) {
			fmt.Fprintln(os.Stderr, "Open ShiftBox Local Helper to complete browser sign-in.")
		}
		os.Exit(1)
	}
}

func helperCommand(ctx context.Context, args []string) error {
	if len(args) > 0 && args[0] == "proxy" {
		return helperProxyCommand(ctx, args[1:])
	}
	if len(args) > 0 && args[0] == "run" {
		args = args[1:]
	}
	flags := flag.NewFlagSet("shiftbox-helper run", flag.ContinueOnError)
	configuredOrigin := flags.String("origin", "", "ShiftBox controller HTTPS origin")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("usage: shiftbox-helper run [--origin HTTPS_ORIGIN]")
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return err
	}
	paths, dataDir, err := helperPaths(executable)
	if err != nil {
		return err
	}
	// A second launch must not open another browser approval or replace the
	// first process's authorized alias snapshot.
	startup, err := os.OpenFile(filepath.Join(dataDir, "runner.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer startup.Close()
	if err := unix.Flock(int(startup.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil
		}
		return err
	}
	origin, err := helperOrigin(ctx, *configuredOrigin, executable, dataDir, promptHelperOrigin)
	if err != nil {
		return err
	}
	if err := registerHelperLogin(); err != nil {
		fmt.Fprintln(os.Stderr, "ShiftBox login item:", err)
	}
	store := nativeHelperStore{}
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp4", helperBrowserAddress)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ShiftBox browser status:", err)
	} else {
		server := &http.Server{
			Handler:           helperBrowserStatus{origin: origin, paths: paths, store: store, now: time.Now},
			ReadHeaderTimeout: 2 * time.Second,
			WriteTimeout:      10 * time.Second,
			IdleTimeout:       15 * time.Second,
		}
		defer server.Close()
		go func() {
			if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
				fmt.Fprintln(os.Stderr, "ShiftBox browser status:", err)
			}
		}()
	}
	return runHelper(ctx, nil, paths, origin, store, time.Now, openHelperBrowser, os.Stderr)
}

func helperProxyCommand(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("shiftbox-helper proxy", flag.ContinueOnError)
	controller := flags.String("origin", "", "ShiftBox controller HTTPS origin")
	account := flags.String("account", "", "Paired ShiftBox account")
	workspace := flags.String("workspace", "", "ShiftBox workspace")
	pins := flags.String("pin-dir", "", "ShiftBox SSH pin directory")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *account == "" || *workspace == "" || *pins == "" {
		return errors.New("invalid ShiftBox SSH proxy command")
	}
	origin, err := canonicalSSHOrigin(*controller)
	if err != nil {
		return err
	}
	session, err := loadProxySession(nativeHelperStore{}, origin, *account, time.Now())
	if err != nil {
		return err
	}
	return runSSHProxy(ctx, nil, origin, *workspace, session.Credential, *pins, os.Stdin, os.Stdout, os.Stderr)
}

func helperPaths(executable string) (sshConfigPaths, string, error) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return sshConfigPaths{}, "", err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return sshConfigPaths{}, "", err
	}
	dataDir := filepath.Join(configDir, "ShiftBox", "Local Helper")
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return sshConfigPaths{}, "", err
	}
	return sshConfigPaths{
		ConfigFile:       filepath.Join(home, ".ssh", "config"),
		AliasStateFile:   filepath.Join(dataDir, "aliases.json"),
		HelperExecutable: executable,
		PinDir:           filepath.Join(dataDir, "pins"),
	}, dataDir, nil
}

func openHelperBrowser(ctx context.Context, target string) error {
	return exec.CommandContext(ctx, "/usr/bin/open", target).Run()
}
