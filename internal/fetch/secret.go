package fetch

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// A Sealer encrypts mailbox passwords before they go into the database.
// The key lives in a file beside the database, not in it, so a copy of
// the database alone (a backup, a PostgreSQL dump) does not give the
// passwords away. Whoever can read both can read the passwords: the
// server has to be able to use them.
type Sealer struct{ aead cipher.AEAD }

// LoadSealer reads the key from dir/fetch.key, creating it on first use.
func LoadSealer(dir string) (*Sealer, error) {
	path := filepath.Join(dir, "fetch.key")
	key, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		key = make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return nil, err
		}
		err = os.WriteFile(path, key, 0o600)
	}
	if err != nil {
		return nil, err
	}
	return NewSealer(key)
}

func NewSealer(key []byte) (*Sealer, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("fetch key must be 32 bytes, found %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Sealer{aead}, nil
}

func (s *Sealer) Seal(secret string) ([]byte, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return s.aead.Seal(nonce, nonce, []byte(secret), nil), nil
}

func (s *Sealer) Open(sealed []byte) (string, error) {
	n := s.aead.NonceSize()
	if len(sealed) < n {
		return "", errors.New("stored password is damaged")
	}
	plain, err := s.aead.Open(nil, sealed[:n], sealed[n:], nil)
	if err != nil {
		return "", errors.New("stored password cannot be read with this server's key")
	}
	return string(plain), nil
}
