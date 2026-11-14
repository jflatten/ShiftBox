package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"

	"github.com/jflatten/ShiftBox/internal/guest"
	"github.com/jflatten/ShiftBox/internal/secret"
)

func secretCommand(ctx context.Context, args []string, stdout, stderr io.Writer, resolve func(context.Context, string) (string, error)) int {
	if len(args) != 3 || args[1] != "get-secret" || !secret.ValidName(args[2]) {
		fmt.Fprintln(stderr, "usage: shiftbox secrets get-secret <name>")
		return 2
	}
	value, err := resolve(ctx, args[2])
	switch {
	case errors.Is(err, sql.ErrNoRows):
		fmt.Fprintln(stderr, "No effective secret was found.")
		return 3
	case errors.Is(err, guest.ErrGuestUnauthorized):
		fmt.Fprintln(stderr, "Workspace secret access is denied.")
		return 4
	case err != nil:
		fmt.Fprintln(stderr, "The workspace secret service is unavailable.")
		return 5
	}
	if n, err := io.WriteString(stdout, value); err != nil || n != len(value) {
		fmt.Fprintln(stderr, "Could not write the secret.")
		return 1
	}
	return 0
}
