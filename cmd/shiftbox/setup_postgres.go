package main

import (
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/jflatten/ShiftBox/internal/app"
)

func readSetupPostgresProfile(path string) (*app.PostgresProfileSetup, error) {
	if path == "" {
		return nil, nil
	}
	invalid := errors.New("PostgreSQL setup requires a private identity configuration with a scoped Dex URI")
	info, err := os.Lstat(path)
	const limit = 32 << 10
	if err != nil || !filepath.IsAbs(path) || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > limit {
		return nil, invalid
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, invalid
	}
	defer file.Close()
	var input struct {
		DexURI string `json:"dexUri"`
		CAFile string `json:"caFile"`
	}
	decoder := json.NewDecoder(io.LimitReader(file, limit+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil || input.DexURI == "" {
		return nil, invalid
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, invalid
	}
	roots, err := x509.SystemCertPool()
	if err != nil {
		return nil, errors.New("cannot load PostgreSQL system trust")
	}
	if input.CAFile != "" {
		if !filepath.IsAbs(input.CAFile) {
			return nil, invalid
		}
		roots = x509.NewCertPool()
		pem, err := os.ReadFile(input.CAFile)
		if err != nil || !roots.AppendCertsFromPEM(pem) {
			return nil, errors.New("cannot load the PostgreSQL certificate authority")
		}
	}
	return &app.PostgresProfileSetup{DexURI: input.DexURI, CAFile: input.CAFile, Roots: roots}, nil
}
