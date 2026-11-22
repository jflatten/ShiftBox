package cryptostore

import (
	"errors"
	"io"
	"os"
)

// LoadKey reads an existing protected key; a missing key never creates a new identity.
func LoadKey(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.New("cannot open installation key")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("installation key requires a private regular file")
	}
	key, err := io.ReadAll(io.LimitReader(file, 33))
	if err != nil || len(key) != 32 {
		return nil, errors.New("installation key must contain 32 bytes")
	}
	return key, nil
}
