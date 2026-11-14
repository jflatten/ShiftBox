package main

import (
	"context"
	"crypto/x509"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"github.com/jflatten/ShiftBox/internal/app"
	"github.com/jflatten/ShiftBox/internal/cryptostore"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/operation"
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

func resumeRetiredSetup(ctx context.Context, dir string, cipher *cryptostore.Store, roots *x509.CertPool) (bool, error) {
	path := filepath.Join(dir, "bootstrap.db")
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil || !info.Mode().IsRegular() {
		return false, errors.New("cannot open the retained local setup database")
	}
	db, err := database.Open(ctx, database.Config{Profile: "sqlite", SQLitePath: path})
	if err != nil {
		return false, err
	}
	defer db.Close()
	var exists bool
	err = db.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM sqlite_master WHERE type='table' AND name='installations')").Scan(&exists)
	if err != nil || !exists {
		return false, err
	}
	var id string
	err = db.QueryRowContext(ctx, "SELECT id FROM installations WHERE state='blocked' AND data_profile='sqlite'").Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	setup := app.SetupHTTP{DB: db, InstallationID: id, Cipher: cipher, CertificateRoots: roots,
		Journal: operation.NewJournal(db, time.Now, uuid.NewString), Now: time.Now, NewID: uuid.NewString}
	_, err = setup.ResumeRetiredPostgresSetup(ctx)
	if err != nil {
		return false, errors.New("cannot resume PostgreSQL activation; check the selected database and trust")
	}
	return true, nil
}
