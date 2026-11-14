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
	if len(args) > 0 {
		switch args[0] {
		case "setup":
			return runSetup(ctx, args[1:], out, status)
		case "serve":
			return runServe(ctx, args[1:])
		case "recover-admin":
			return runRecoverAdmin(ctx, args[1:], out)
		}
	}
	return errors.New("usage: shiftbox setup | shiftbox serve | shiftbox recover-admin")
}
