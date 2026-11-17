package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/jflatten/ShiftBox/internal/guest"
	"github.com/jflatten/ShiftBox/internal/secret"
)

const workspaceHelp = `Usage: shiftbox <command>

Workspace commands:
  watch <PID>                Keep this workspace alive while the original process exists.
                             Runs in the foreground; Ctrl+C ends the keep-alive.
  secrets list              List effective secret keys and their user, project, or organization scope.
  secrets get <key>          Print the effective secret value exactly, without adding a newline.
  secrets get-secret <key>   Compatibility alias for secrets get.
  help, --help               Show this help.

Internal service commands:
  git credential <action>    Git's workspace credential helper.
  serve                     Run the supervised VM guest agent.
  supervisor-select         Select the verified guest agent for startup (root only).
`

type workspaceCommands struct {
	serve  func(context.Context) error
	secret func(context.Context, string) (string, error)
	keys   func(context.Context) ([]guest.SecretKey, error)
	git    func(context.Context, string) (string, error)
	watch  func(context.Context, int) error
}

func workspaceCommand(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, services workspaceCommands) int {
	if len(args) == 0 || (len(args) == 1 && (args[0] == "help" || args[0] == "--help" || args[0] == "-h")) {
		_, err := io.WriteString(stdout, workspaceHelp)
		if err != nil {
			return 1
		}
		return 0
	}
	switch args[0] {
	case "serve":
		if len(args) != 1 || services.serve == nil {
			break
		}
		if err := services.serve(ctx); err != nil && !errors.Is(err, context.Canceled) {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	case "git":
		if len(args) != 3 || args[1] != "credential" || services.git == nil ||
			(args[2] != "get" && args[2] != "store" && args[2] != "erase") {
			break
		}
		return gitCredentialCommand(ctx, args, stdin, stdout, stderr, services.git)
	case "secrets":
		if len(args) == 2 && args[1] == "list" && services.keys != nil {
			keys, err := services.keys(ctx)
			if err != nil {
				fmt.Fprintln(stderr, "The workspace secret service is unavailable or access was denied.")
				return 5
			}
			for _, key := range keys {
				if _, err := fmt.Fprintf(stdout, "%s (%s)\n", key.Name, key.Scope); err != nil {
					return 1
				}
			}
			return 0
		}
		if len(args) == 3 && (args[1] == "get" || args[1] == "get-secret") && secret.ValidName(args[2]) && services.secret != nil {
			return secretCommand(ctx, args, stdout, stderr, services.secret)
		}
	case "watch":
		if len(args) != 2 {
			break
		}
		pid, err := strconv.Atoi(args[1])
		if err != nil || pid <= 0 || strings.IndexFunc(args[1], func(r rune) bool { return r < '0' || r > '9' }) >= 0 || services.watch == nil {
			break
		}
		fmt.Fprintf(stdout, "Watching process %d. Press Ctrl+C to stop keeping this workspace alive.\n", pid)
		if err := services.watch(ctx, pid); err != nil && !errors.Is(err, context.Canceled) {
			fmt.Fprintln(stderr, "Could not keep the workspace alive. Check the PID in this container and the workspace agent connection.")
			return 1
		}
		return 0
	}
	fmt.Fprintln(stderr, "Unknown command or invalid arguments.")
	_, _ = io.WriteString(stdout, workspaceHelp)
	return 2
}
