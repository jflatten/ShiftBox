package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/jflatten/ShiftBox/internal/guest"
)

const (
	stateDirectory = "/var/lib/shiftbox-agent"
	seedFile       = "/run/shiftbox-enrollment/ENROLLMENT.JSON"
	platformAgent  = "/usr/local/bin/shiftbox-agent"
)

var (
	errUsage     = errors.New("usage: shiftbox-agent serve")
	agentRelease = "development"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "supervisor-select" && os.Geteuid() == 0 {
		path, err := guest.SelectGuestAgent(stateDirectory, platformAgent)
		if err != nil {
			fmt.Fprintln(os.Stderr, "Could not verify the guest agent for startup.")
			os.Exit(1)
		}
		fmt.Fprintln(os.Stdout, path)
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	code := workspaceCommand(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr, workspaceCommands{
		serve: func(ctx context.Context) error { return run(ctx, []string{"serve"}) },
		secret: func(ctx context.Context, key string) (string, error) {
			return guest.ReadLocalSecret(ctx, guest.SecretSocketPath, key)
		},
		keys: func(ctx context.Context) ([]guest.SecretKey, error) {
			return guest.ListLocalSecrets(ctx, guest.SecretSocketPath)
		},
		git: func(ctx context.Context, repository string) (string, error) {
			return guest.ReadLocalGitToken(ctx, guest.SecretSocketPath, repository)
		},
		watch: func(ctx context.Context, pid int) error {
			return guest.WatchProcess(ctx, guest.ActivitySocketPath, pid)
		},
	})
	if code != 0 {
		os.Exit(code)
	}
}

func run(ctx context.Context, args []string) error {
	if len(args) != 1 || args[0] != "serve" {
		return errUsage
	}
	if os.Geteuid() == 0 && os.Getenv("SHIFTBOX_EC2_ENROLLMENT") == "1" {
		return guest.ServeEC2Agent(ctx, stateDirectory, "1", agentRelease, guest.SecretSocketPath)
	}
	return guest.ServeAgent(ctx, stateDirectory, seedFile, "1", agentRelease, guest.SecretSocketPath)
}
