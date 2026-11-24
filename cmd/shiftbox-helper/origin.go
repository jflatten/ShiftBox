package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func helperOrigin(ctx context.Context, configured, executable, dataDir string, prompt func(context.Context) (string, error)) (string, error) {
	if configured != "" {
		return canonicalSSHOrigin(configured)
	}
	resource := filepath.Join(filepath.Dir(executable), "..", "Resources", "shiftbox-helper.json")
	f, err := os.Open(resource)
	if err != nil {
		return "", errors.New("the app has no controller URL; download it from ShiftBox or run with --origin")
	}
	defer f.Close()
	var settings struct {
		Origin   string `json:"origin"`
		Portable bool   `json:"portable"`
	}
	if err := readHelperResponse(f, &settings, 8<<10); err != nil {
		return "", errors.New("the bundled ShiftBox controller configuration is invalid")
	}
	if !settings.Portable {
		return canonicalSSHOrigin(settings.Origin)
	}
	file := filepath.Join(dataDir, "controller-origin")
	previous, err := os.ReadFile(file)
	if err == nil {
		return canonicalSSHOrigin(strings.TrimSpace(string(previous)))
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	value, err := prompt(ctx)
	if err != nil {
		return "", err
	}
	origin, err := canonicalSSHOrigin(strings.TrimSpace(value))
	if err != nil {
		return "", err
	}
	temp, err := os.CreateTemp(dataDir, ".controller-origin-")
	if err != nil {
		return "", err
	}
	defer os.Remove(temp.Name())
	_, writeErr := temp.WriteString(origin + "\n")
	if err := errors.Join(writeErr, temp.Close()); err != nil {
		return "", err
	}
	if err := os.Rename(temp.Name(), file); err != nil {
		return "", err
	}
	return origin, nil
}

func promptHelperOrigin(ctx context.Context) (string, error) {
	// The dialog only collects the management URL. Pairing still happens in the
	// browser and the helper verifies TLS when it connects to the chosen origin.
	output, err := exec.CommandContext(ctx, "/usr/bin/osascript",
		"-e", "try",
		"-e", `return text returned of (display dialog "Enter your ShiftBox HTTPS URL." default answer "" with title "ShiftBox Local Helper" buttons {"Cancel", "Continue"} default button "Continue" cancel button "Cancel")`,
		"-e", "on error number -128",
		"-e", `return ""`,
		"-e", "end try").Output()
	if err != nil {
		return "", err
	}
	if value := strings.TrimSpace(string(output)); value != "" {
		return value, nil
	}
	return "", context.Canceled
}
