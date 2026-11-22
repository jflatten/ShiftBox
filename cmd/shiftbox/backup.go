package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/jflatten/ShiftBox/internal/config"
	"github.com/jflatten/ShiftBox/internal/web"
)

const backupSocketName = "backup.sock"

type localBackupInput struct {
	Destination string `json:"destination"`
	Password    string `json:"password"`
}

type backupResult struct {
	OperationID    string `json:"operationId"`
	Archive        string `json:"archive"`
	ManifestDigest string `json:"manifestDigest"`
}

func runBackup(ctx context.Context, args []string, in io.Reader, out, status io.Writer) error {
	flags := flag.NewFlagSet("backup", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", "", "")
	output := flags.String("output", "", "")
	stdin := flags.Bool("password-stdin", false, "")
	asJSON := flags.Bool("json", false, "")
	fail := func(exit int, code, title string) error {
		return backupOutput(out, status, *asJSON, nil, exit, code, title)
	}
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || *configPath == "" || *output == "" {
		return fail(2, "backup.invalid_input", "Provide the controller configuration and archive output path.")
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return fail(2, "backup.invalid_input", "Open a valid controller configuration.")
	}
	path, err := filepath.Abs(*output)
	if err != nil || path == "/" {
		return fail(2, "backup.invalid_input", "Provide a valid archive output path.")
	}
	socket := filepath.Join(cfg.InstallationDir, backupSocketName)
	if !privateBackupDirectory(cfg.InstallationDir) {
		return fail(4, "backup.operator_denied", "Use the controller's private installation directory.")
	}
	info, err := os.Lstat(socket)
	if err != nil || info.Mode()&os.ModeSocket == 0 || info.Mode().Perm()&0077 != 0 {
		return fail(5, "backup.controller_unavailable", "Start the controller before requesting a backup.")
	}
	passphrase, err := readRecoveryPassphrase(in, status, *stdin, true)
	if err != nil {
		return fail(2, "backup.password_input", "Provide the same passphrase twice using the terminal or explicit stdin.")
	}
	data, err := json.Marshal(localBackupInput{Destination: path, Password: passphrase})
	if err != nil {
		return fail(1, "backup.request_failed", "Could not prepare the backup request.")
	}
	defer clear(data)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://shiftbox-local/backup", bytes.NewReader(data))
	if err != nil {
		return fail(1, "backup.request_failed", "Could not prepare the backup request.")
	}
	request.Header.Set("Content-Type", "application/json")
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "unix", socket)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 31 * time.Minute,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return fail(5, "backup.controller_unavailable", "The controller did not return verified backup evidence.")
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case http.StatusConflict:
		return fail(6, "backup.conflict", "A backup is in progress or the archive path already exists.")
	case http.StatusBadRequest:
		return fail(2, "backup.invalid_input", "Use a new archive path and a passphrase of at least 12 characters.")
	case http.StatusOK:
		var result backupResult
		if err := json.NewDecoder(io.LimitReader(response.Body, 8192)).Decode(&result); err == nil &&
			web.ValidUUID(result.OperationID) && result.Archive == path && len(result.ManifestDigest) == 64 {
			return backupOutput(out, status, *asJSON, &result, 0, "", "")
		}
	}
	return fail(1, "backup.failed", "The controller could not verify the backup. Check the operation before retrying.")
}

func backupOutput(out, status io.Writer, asJSON bool, result *backupResult, exit int, code, title string) error {
	if asJSON {
		envelope := struct {
			APIVersion string        `json:"apiVersion"`
			Command    string        `json:"command"`
			Status     string        `json:"status"`
			Result     *backupResult `json:"result"`
			Problem    *web.Problem  `json:"problem"`
		}{APIVersion: "shiftbox.io/cli/v1", Command: "backup", Status: "succeeded", Result: result}
		if exit != 0 {
			envelope.Status = "failed"
			envelope.Problem = &web.Problem{Type: "about:blank", Status: map[int]int{1: 500, 2: 400, 4: 403, 5: 503, 6: 409}[exit], Code: code, Title: title}
		}
		if err := json.NewEncoder(out).Encode(envelope); err != nil {
			return cliExit{1, "Cannot write backup result."}
		}
	} else if result != nil {
		fmt.Fprintln(status, "Encrypted management archive verified.")
		fmt.Fprintln(out, result.Archive)
	}
	if exit != 0 {
		return cliExit{exit, title}
	}
	return nil
}

func privateBackupDirectory(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.IsDir() && info.Mode().Perm()&0077 == 0
}

func prepareBackupSocket(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || info.Mode()&os.ModeSocket == 0 {
		return errors.New("backup socket path is occupied")
	}
	return os.Remove(path)
}
