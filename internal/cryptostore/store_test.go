package cryptostore_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/jflatten/ShiftBox/internal/cryptostore"
)

// TestSealBindsRecord prevents database-only attackers moving a secret to another owner.
func TestSealBindsRecord(t *testing.T) {
	store, err := cryptostore.New(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	context := cryptostore.Context{KeyVersion: 1, RecordType: "secret", Scope: "user", OwnerID: "alice", RecordID: "one", Name: "TOKEN"}
	first, err := store.Seal(context, []byte("private-token"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Seal(context, []byte("private-token"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(first.Nonce, second.Nonce) {
		t.Error("Seal reused a nonce")
	}
	if bytes.Contains(first.Ciphertext, []byte("private-token")) {
		t.Error("Seal stored plaintext")
	}
	plain, err := store.Open(context, first)
	if err != nil || string(plain) != "private-token" {
		t.Fatalf("Open(original record) = %q, %v", plain, err)
	}
	for _, mutate := range []func(*cryptostore.Context){
		func(c *cryptostore.Context) { c.KeyVersion++ },
		func(c *cryptostore.Context) { c.RecordType = "oauth" },
		func(c *cryptostore.Context) { c.Scope = "organization" },
		func(c *cryptostore.Context) { c.OwnerID = "bob" },
		func(c *cryptostore.Context) { c.RecordID = "two" },
		func(c *cryptostore.Context) { c.Name = "OTHER" },
	} {
		other := context
		mutate(&other)
		if value, err := store.Open(other, first); err == nil || len(value) != 0 {
			t.Error("Open(moved ciphertext) returned plaintext")
		}
	}
	first.Ciphertext[0] ^= 1
	if value, err := store.Open(context, first); err == nil || len(value) != 0 {
		t.Error("Open(tampered ciphertext) returned plaintext")
	}
}

// TestKeyValidation rejects short keys and readable key files without generating replacements.
func TestKeyValidation(t *testing.T) {
	for _, size := range []int{0, 16, 24, 31, 33} {
		if _, err := cryptostore.New(make([]byte, size)); err == nil {
			t.Errorf("New(%d bytes) succeeded, want AES-256 key only", size)
		}
	}
	path := filepath.Join(t.TempDir(), "key")
	if _, err := cryptostore.LoadKey(path); err == nil {
		t.Error("LoadKey(missing) succeeded")
	}
	if err := os.WriteFile(path, bytes.Repeat([]byte{7}, 32), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := cryptostore.LoadKey(path); err == nil {
		t.Error("LoadKey(world readable) succeeded")
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	key, err := cryptostore.LoadKey(path)
	if err != nil || len(key) != 32 {
		t.Fatalf("LoadKey(protected) = %d bytes, %v", len(key), err)
	}
}

// TestEmptySecretRoundTrip preserves an intentional empty override through encryption.
func TestEmptySecretRoundTrip(t *testing.T) {
	store, err := cryptostore.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	context := cryptostore.Context{KeyVersion: 1, RecordType: "secret", Scope: "user", OwnerID: "alice", RecordID: "one", Name: "TOKEN"}
	sealed, err := store.Seal(context, nil)
	if err != nil {
		t.Fatal(err)
	}
	value, err := store.Open(context, sealed)
	if err != nil || len(value) != 0 {
		t.Fatalf("Open(empty override) = %q, %v", value, err)
	}
}
