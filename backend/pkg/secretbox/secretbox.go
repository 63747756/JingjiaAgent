// Package secretbox encrypts deployment secrets with a separately mounted key.
package secretbox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
	"os"
	"runtime"
	"strings"
)

const prefix = "v1:"

var (
	ErrKey           = errors.New("secret encryption key unavailable or invalid")
	ErrCiphertext    = errors.New("invalid encrypted secret")
	ErrUninitialized = errors.New("secret encryption is not initialized")
)

var associatedData = []byte("jingjiaagent:secretbox:v1")

type Box struct {
	aead cipher.AEAD
}

// NewFromFile reads exactly 32 raw key bytes. It never creates or replaces a
// key: installation must provision it independently and preserve it on restart.
func NewFromFile(path string) (*Box, error) {
	if strings.TrimSpace(path) == "" {
		return nil, ErrKey
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, ErrKey
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || (runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0) {
		return nil, ErrKey
	}
	key, err := io.ReadAll(io.LimitReader(f, 33))
	if err != nil || len(key) != 32 {
		return nil, ErrKey
	}
	defer func() {
		for i := range key {
			key[i] = 0
		}
	}()
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, ErrKey
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, ErrKey
	}
	return &Box{aead: aead}, nil
}

func (b *Box) Seal(plain string) (string, error) {
	if b == nil || b.aead == nil {
		return "", ErrUninitialized
	}
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", errors.New("could not generate secret encryption nonce")
	}
	sealed := b.aead.Seal(nonce, nonce, []byte(plain), associatedData)
	return prefix + base64.RawURLEncoding.EncodeToString(sealed), nil
}

func (b *Box) Open(encrypted string) (string, error) {
	if b == nil || b.aead == nil {
		return "", ErrUninitialized
	}
	if !strings.HasPrefix(encrypted, prefix) {
		return "", ErrCiphertext
	}
	data, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(encrypted, prefix))
	if err != nil || len(data) < b.aead.NonceSize()+b.aead.Overhead() {
		return "", ErrCiphertext
	}
	plain, err := b.aead.Open(nil, data[:b.aead.NonceSize()], data[b.aead.NonceSize():], associatedData)
	if err != nil {
		return "", ErrCiphertext
	}
	return string(plain), nil
}
