package imagecache

import (
	"os"
	"path/filepath"
	"testing"
)

// TestCheckStoragePathProtectsInstallationData rejects overlap with the
// installation or its backups and a symlink beneath the managed cache root.
func TestCheckStoragePathProtectsInstallationData(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve test directory: %v", err)
	}
	installation := filepath.Join(base, "installation")
	cacheRoot := filepath.Join(installation, "cache")
	backups := filepath.Join(installation, "backups")
	for _, path := range []string{cacheRoot, backups} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatalf("create path fixture %q: %v", path, err)
		}
	}
	link := filepath.Join(cacheRoot, "redirected")
	if err := os.Symlink(backups, link); err != nil {
		t.Fatalf("create cache path symlink %q: %v", link, err)
	}
	budget := "1024"
	tests := []struct {
		name    string
		path    string
		wantErr bool
	}{
		{name: "default managed directory", path: filepath.Join(cacheRoot, "registry")},
		{name: "installation directory", path: installation, wantErr: true},
		{name: "parent of installation directory", path: base, wantErr: true},
		{name: "backups directory", path: backups, wantErr: true},
		{name: "child of backups directory", path: filepath.Join(backups, "registry"), wantErr: true},
		{name: "symlink to backups directory", path: link, wantErr: true},
		{name: "path through symlink", path: filepath.Join(link, "registry"), wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := Settings{Enabled: true, StoragePath: test.path, MaxBytes: &budget, VolumeHighPercent: 80, VolumeLowPercent: 70}
			err := CheckStoragePath(installation, "", candidate)
			if (err != nil) != test.wantErr {
				t.Errorf("CheckStoragePath(%q, %q) error = %v, want error presence %t", installation, test.path, err, test.wantErr)
			}
		})
	}
}
