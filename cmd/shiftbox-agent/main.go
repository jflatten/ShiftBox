package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/jflatten/ShiftBox/internal/guest"
)

const (
	stateDirectory   = "/var/lib/shiftbox-agent"
	seedFile         = "/run/shiftbox-enrollment/ENROLLMENT.JSON"
	pollEndpointFile = "/etc/shiftbox/guest-poll-endpoint"
)

var (
	errUsage     = errors.New("usage: shiftbox-agent serve")
	agentRelease = "development"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
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
	endpoint, err := os.ReadFile(pollEndpointFile)
	if err != nil || len(endpoint) > 4096 {
		return errors.New("guest controller endpoint is unavailable")
	}
	return guest.ServeAgent(ctx, stateDirectory, seedFile, strings.TrimSpace(string(endpoint)), "1", agentRelease)
}
