package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/jflatten/ShiftBox/internal/guest"
	"github.com/jflatten/ShiftBox/internal/secret"
)

func secretCommand(ctx context.Context, args []string, stdout, stderr io.Writer, resolve func(context.Context, string) (string, error)) int {
	if len(args) != 3 || (args[1] != "get" && args[1] != "get-secret") || !secret.ValidName(args[2]) {
		fmt.Fprintln(stderr, "usage: shiftbox secrets get <key>")
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

func gitCredentialCommand(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer,
	resolve func(context.Context, string) (string, error),
) int {
	if len(args) != 3 || args[1] != "credential" {
		return 2
	}
	switch args[2] {
	case "store", "erase":
		return 0
	case "get":
	default:
		return 2
	}
	repository, err := gitRepository(stdin)
	if err != nil {
		fmt.Fprintln(stderr, "Invalid Git credential request.")
		return 2
	}
	if repository == "" {
		return 0
	}
	token, err := resolve(ctx, repository)
	switch {
	case errors.Is(err, guest.ErrGuestUnauthorized):
		fmt.Fprintln(stderr, "Git credential access is denied for this repository.")
		return 4
	case errors.Is(err, guest.ErrGitReconnect):
		fmt.Fprintln(stderr, "Reconnect your GitHub account in ShiftBox.")
		return 4
	case err != nil || !guest.ValidGitAccessToken(token):
		fmt.Fprintln(stderr, "The Git credential service is unavailable.")
		return 5
	}
	if _, err := fmt.Fprintf(stdout, "username=x-access-token\npassword=%s\n\n", token); err != nil {
		return 1
	}
	return 0
}

func gitRepository(stdin io.Reader) (string, error) {
	raw, err := io.ReadAll(io.LimitReader(stdin, (16<<10)+1))
	defer clear(raw)
	if err != nil || len(raw) > 16<<10 {
		return "", errors.New("invalid Git credential request")
	}
	fields := map[string]string{}
	for line := range strings.SplitSeq(string(raw), "\n") {
		if line == "" {
			break
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return "", errors.New("invalid Git credential field")
		}
		switch key {
		case "protocol", "host", "path":
			if _, duplicate := fields[key]; duplicate {
				return "", errors.New("duplicate Git credential field")
			}
			fields[key] = value
		}
	}
	if fields["protocol"] != "https" || !strings.EqualFold(fields["host"], "github.com") || fields["path"] == "" {
		return "", nil
	}
	repository := "https://github.com/" + fields["path"]
	if !guest.ValidGitRepository(repository) {
		return "", errors.New("invalid Git repository")
	}
	return repository, nil
}
