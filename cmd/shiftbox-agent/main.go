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
)

var (
	errUsage     = errors.New("usage: shiftbox-agent serve")
	agentRelease = "development"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if len(os.Args) > 1 && os.Args[1] == "git" {
		code := gitCredentialCommand(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr, func(ctx context.Context, repository string) (string, error) {
			return guest.ReadLocalGitToken(ctx, guest.SecretSocketPath, repository)
		})
		if code != 0 {
			os.Exit(code)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "secrets" {
		code := secretCommand(ctx, os.Args[1:], os.Stdout, os.Stderr, func(ctx context.Context, name string) (string, error) {
			return guest.ReadLocalSecret(ctx, guest.SecretSocketPath, name)
		})
		if code != 0 {
			os.Exit(code)
		}
		return
	}
	if err := run(ctx, os.Args[1:]); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(os.Stderr, err)
		if errors.Is(err, errUsage) {
			os.Exit(2)
		}
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	if len(args) != 1 || args[0] != "serve" {
		return errUsage
	}
	return guest.ServeAgent(ctx, stateDirectory, seedFile, "1", agentRelease, guest.SecretSocketPath)
}
