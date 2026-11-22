// Command sign-guest-update publishes a detached ShiftBox guest release signature.
package main

import (
	"context"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/jflatten/ShiftBox/internal/release"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: go run ./platform/packer/sign-guest-update <release-catalog> <release-id> < publisher-key.pem")
		os.Exit(2)
	}
	if err := signUpdate(ctx, os.Args[1], os.Args[2], os.Stdin); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func signUpdate(ctx context.Context, root, id string, input io.Reader) error {
	const maxKey = 8 << 10
	invalid := errors.New("expected one Ed25519 PKCS#8 private key on standard input")
	raw, err := io.ReadAll(io.LimitReader(input, maxKey+1))
	defer clear(raw)
	if err != nil || len(raw) > maxKey {
		return invalid
	}
	block, rest := pem.Decode(raw)
	if block == nil || block.Type != "PRIVATE KEY" || len(rest) != 0 {
		return invalid
	}
	defer clear(block.Bytes)
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	private, ok := key.(ed25519.PrivateKey)
	if err != nil || !ok {
		return invalid
	}
	defer clear(private)
	return release.SignLocalGuestUpdate(ctx, root, id, private)
}
