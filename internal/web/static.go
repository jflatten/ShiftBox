package web

import (
	"errors"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// NewStaticHandler serves files from a built Next.js export. Mount it on the
// management origin below the API and sign-in routes.
func NewStaticHandler(files fs.FS) (http.Handler, error) {
	if files == nil {
		return nil, errors.New("browser UI export is missing")
	}
	index, err := fs.Stat(files, "index.html")
	if err != nil || !index.Mode().IsRegular() {
		return nil, errors.New("browser UI export requires index.html")
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		name := strings.TrimPrefix(strings.TrimSuffix(r.URL.Path, "/"), "/")
		if name == "" && r.URL.Path == "/" {
			name = "index.html"
		}
		if !staticPath(name) {
			http.NotFound(w, r)
			return
		}
		entry, err := fs.Stat(files, name)
		if err == nil && entry.IsDir() {
			name = path.Join(name, "index.html")
			entry, err = fs.Stat(files, name)
		}
		if err != nil || !entry.Mode().IsRegular() {
			http.NotFound(w, r)
			return
		}
		cache := "no-cache"
		if strings.HasPrefix(name, "_next/static/") {
			cache = "public, max-age=31536000, immutable"
		}
		w.Header().Set("Cache-Control", cache)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		http.ServeFileFS(w, r, files, name)
	}), nil
}

func staticPath(name string) bool {
	if !fs.ValidPath(name) || strings.Contains(name, "\\") {
		return false
	}
	switch head, _, _ := strings.Cut(name, "/"); head {
	case "api", "auth", "dex", "internal", "_shiftbox":
		return false
	}
	for component := range strings.SplitSeq(name, "/") {
		if strings.HasPrefix(component, ".") {
			return false
		}
	}
	return true
}
