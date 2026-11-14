package main

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/jflatten/ShiftBox/internal/guest"
)

// TestSecretCommandExit checks the documented exit statuses and stdout bytes.
func TestSecretCommandExit(t *testing.T) {
	for _, tc := range []struct {
		name, value string
		err         error
		code        int
	}{
		{"value", "exact value\n", nil, 0},
		{"empty", "", nil, 0},
		{"missing", "", sql.ErrNoRows, 3},
		{"denied", "", guest.ErrGuestUnauthorized, 4},
		{"offline", "", errors.New("fixture failure"), 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := secretCommand(t.Context(), []string{"secrets", "get-secret", "TOKEN"}, &stdout, &stderr,
				func(_ context.Context, name string) (string, error) {
					if name != "TOKEN" {
						t.Errorf("requested %q, want TOKEN", name)
					}
					return tc.value, tc.err
				})
			if code != tc.code || stdout.String() != tc.value || (stderr.Len() == 0) != (code == 0) {
				t.Fatalf("secret command = %d, stdout %q and stderr %q, want %d", code, &stdout, &stderr, tc.code)
			}
		})
	}
}

// TestGitCredentialHelper checks Git protocol output, rejects foreign hosts,
// and verifies that store and erase never read or persist a credential.
func TestGitCredentialHelper(t *testing.T) {
	const input = "protocol=https\nhost=github.com\npath=owner/project.git\n\n"
	for _, tc := range []struct {
		action, input string
		wantCall      bool
	}{
		{"get", input, true},
		{"get", "protocol=https\nhost=elsewhere.test\npath=owner/project.git\n\n", false},
		{"get", "protocol=https\nhost=github.com\n\n", false},
		{"store", input, false},
		{"erase", input, false},
	} {
		var out, diagnostics bytes.Buffer
		called := false
		code := gitCredentialCommand(t.Context(), []string{"git", "credential", tc.action},
			strings.NewReader(tc.input), &out, &diagnostics, func(_ context.Context, repo string) (string, error) {
				called = true
				if repo != "https://github.com/owner/project.git" {
					t.Errorf("helper requested %q, want the HTTPS repository", repo)
				}
				return "fixture-token", nil
			})
		want := ""
		if tc.wantCall {
			want = "username=x-access-token\npassword=fixture-token\n\n"
		}
		if code != 0 || called != tc.wantCall || out.String() != want || diagnostics.Len() != 0 {
			t.Errorf("helper %s: code %d, called %t, output %q, want 0, %t, %q", tc.action, code, called, out.String(), tc.wantCall, want)
		}
	}
}
