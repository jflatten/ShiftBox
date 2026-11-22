package cryptostore

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
)

// Context binds a protected value to its logical record and authorization scope.
type Context struct {
	KeyVersion int    `json:"keyVersion"`
	RecordType string `json:"recordType"`
	Scope      string `json:"scope"`
	OwnerID    string `json:"ownerId"`
	RecordID   string `json:"recordId"`
	Name       string `json:"name"`
}

// Value contains storage-safe authenticated ciphertext, never plaintext.
type Value struct {
	Nonce      []byte `json:"nonce"`
	Ciphertext []byte `json:"ciphertext"`
}

// Store encrypts application values with the installation's AES-256 key.
type Store struct{ aead cipher.AEAD }

// New rejects keys of any size other than the approved 256 bits.
func New(key []byte) (*Store, error) {
	if len(key) != 32 {
		return nil, errors.New("installation key must contain 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Store{aead: aead}, nil
}

// Seal creates a fresh random nonce for every write, including empty overrides.
func (s *Store) Seal(context Context, plaintext []byte) (Value, error) {
	aad, err := json.Marshal(context)
	if err != nil {
		return Value{}, err
	}
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return Value{}, err
	}
	return Value{Nonce: nonce, Ciphertext: s.aead.Seal(nil, nonce, plaintext, aad)}, nil
}

// Open returns no plaintext when record binding or authentication fails.
func (s *Store) Open(context Context, value Value) ([]byte, error) {
	if len(value.Nonce) != s.aead.NonceSize() {
		return nil, errors.New("invalid protected value")
	}
	aad, err := json.Marshal(context)
	if err != nil {
		return nil, err
	}
	plaintext, err := s.aead.Open(nil, value.Nonce, value.Ciphertext, aad)
	if err != nil {
		return nil, errors.New("protected value authentication failed")
	}
	return plaintext, nil
}
