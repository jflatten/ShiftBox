package guest_test

import (
	"bytes"
	"crypto/x509"
	"encoding/pem"
	"testing"
	"time"

	"github.com/jflatten/ShiftBox/internal/guest"
)

// TestInternalServerLeaf verifies that replacements use fresh keys, bind the
// configured name, and cannot outlive the installation CA.
func TestInternalServerLeaf(t *testing.T) {
	raw, key, _, issuer := authorityFixture(t)
	now := time.Now()
	ca, err := guest.LoadCA(raw, key, now)
	if err != nil {
		t.Fatal(err)
	}
	first, err := ca.ServerLeaf([]string{"controller.test", "127.0.0.1"}, now)
	if err != nil {
		t.Fatal(err)
	}
	next, err := ca.ServerLeaf([]string{"controller.test", "127.0.0.1"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(first.Leaf.RawSubjectPublicKeyInfo, next.Leaf.RawSubjectPublicKeyInfo) {
		t.Error("replacement reused the server private key")
	}
	root := x509.NewCertPool()
	if block, _ := pem.Decode(raw); block == nil || !root.AppendCertsFromPEM(raw) {
		t.Fatal("invalid fixture CA")
	}
	for _, name := range []string{"controller.test", "127.0.0.1"} {
		if _, err := first.Leaf.Verify(x509.VerifyOptions{Roots: root, DNSName: name, CurrentTime: now}); err != nil {
			t.Errorf("verify server name %s: %v", name, err)
		}
	}
	if !first.Leaf.NotAfter.Equal(issuer.NotAfter) {
		t.Errorf("leaf expiry = %v, want issuer boundary", first.Leaf.NotAfter)
	}
	if _, err := ca.ServerLeaf([]string{"*.test"}, now); err == nil {
		t.Error("wildcard server name accepted")
	}
	if _, err := ca.ServerLeaf([]string{"controller.test"}, issuer.NotAfter); err == nil {
		t.Error("issued after CA expiry")
	}
}
