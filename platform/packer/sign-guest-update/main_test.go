package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"testing"

	"github.com/jflatten/ShiftBox/internal/release"
)

// TestSignUpdateInput distinguishes a valid publisher key from malformed
// input without echoing the input in a diagnostic.
func TestSignUpdateInput(t *testing.T) {
	_, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		t.Fatal(err)
	}
	data := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	if err := signUpdate(t.Context(), t.TempDir(), "candidate", bytes.NewReader(data)); !errors.Is(err, release.ErrGuestUpdateUnavailable) {
		t.Errorf("valid publisher key with missing release = %v, want release unavailable", err)
	}
	if err := signUpdate(t.Context(), t.TempDir(), "candidate", bytes.NewReader(append(data, data...))); err == nil ||
		err.Error() != "expected one Ed25519 PKCS#8 private key on standard input" {
		t.Errorf("multiple publisher keys = %v, want a sanitized input error", err)
	}
}
