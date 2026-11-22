package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jflatten/ShiftBox/internal/backup"
	"github.com/jflatten/ShiftBox/internal/operation"
	"github.com/jflatten/ShiftBox/internal/web"
)

type localBackupListener struct {
	network net.Listener
	server  *http.Server
}

// The controller holds its installation lock while binding the socket.
func listenLocalBackup(dir string, service *backup.Service,
	capture func(context.Context, string) (map[string]string, error)) (*localBackupListener, error) {
	if !privateBackupDirectory(dir) {
		return nil, errors.New("backup listener requires a private installation directory")
	}
	path := filepath.Join(dir, backupSocketName)
	if err := prepareBackupSocket(path); err != nil {
		return nil, errors.New("cannot prepare private backup listener")
	}
	network, err := net.Listen("unix", path)
	if err != nil {
		return nil, errors.New("cannot bind the private backup listener")
	}
	if err := os.Chmod(path, 0600); err != nil {
		network.Close()
		return nil, errors.New("cannot protect the backup listener")
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /backup", func(w http.ResponseWriter, r *http.Request) {
		var input localBackupInput
		if err := web.DecodeJSON(w, r, &input, 8192); err != nil ||
			utf8.RuneCountInString(input.Password) < 12 || len(input.Password) > 1024 || !filepath.IsAbs(input.Destination) ||
			filepath.Clean(input.Destination) != input.Destination || input.Destination == "/" ||
			len(input.Destination) > 4096 || strings.ContainsRune(input.Destination, 0) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if err := backup.CheckArchiveDestination(input.Destination); err != nil {
			w.WriteHeader(http.StatusConflict)
			return
		}
		op, err := service.QueueLocal(r.Context(), uuid.NewString())
		if errors.Is(err, operation.ErrConflict) {
			w.WriteHeader(http.StatusConflict)
			return
		}
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		evidence, err := service.Run(r.Context(), op.ID, input.Destination, input.Password, capture)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(backupResult{OperationID: op.ID, Archive: input.Destination, ManifestDigest: evidence.ManifestDigest})
	})
	return &localBackupListener{network: network, server: &http.Server{Handler: mux, ReadTimeout: 10 * time.Second, IdleTimeout: time.Minute}}, nil
}

// Serve handles backup requests over the private socket for the controller lifetime.
func (s *localBackupListener) Serve(ctx context.Context) error {
	lifetime, cancel := context.WithCancel(ctx)
	defer cancel()
	s.server.BaseContext = func(net.Listener) context.Context { return lifetime }
	shutdown := make(chan struct{})
	go func() {
		defer close(shutdown)
		<-lifetime.Done()
		grace, stop := context.WithTimeout(context.WithoutCancel(ctx), 6*time.Second)
		defer stop()
		if err := s.server.Shutdown(grace); err != nil {
			_ = s.server.Close()
		}
	}()
	err := s.server.Serve(s.network)
	cancel()
	<-shutdown
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

// Close stops accepting requests on the private backup socket.
func (s *localBackupListener) Close() { _ = s.network.Close() }
