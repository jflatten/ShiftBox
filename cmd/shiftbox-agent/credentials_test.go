package main

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
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
