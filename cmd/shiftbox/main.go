package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, out, status io.Writer) error {
	if len(args) == 0 || args[0] != "setup" {
		return errors.New("usage: shiftbox setup --installation-dir <path> --administrator-email <email>")
	}
	return runSetup(ctx, args[1:], out, status)
}
