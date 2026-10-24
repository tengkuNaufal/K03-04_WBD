package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"io"
)

type Box struct {
	aead cipher.AEAD
}

// NewBox derives a 256-bit key from the given secret.
func NewBox(secretKey []byte) (*Box, error) {
	if len(secretKey) < 16 {
		return nil, errors.New("secret: key must be at least 16 bytes")
	}
	k := sha256.Sum256(secretKey)
	block, err := aes.NewCipher(k[:])
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead}, nil
}

// Encrypt returns nonce || ciphertext.
func (b *Box) Encrypt(plain []byte) ([]byte, error) {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return b.aead.Seal(nonce, nonce, plain, nil), nil
}

func (b *Box) Decrypt(data []byte) ([]byte, error) {
	n := b.aead.NonceSize()
	if len(data) < n {
		return nil, errors.New("secret: ciphertext too short")
	}
	return b.aead.Open(nil, data[:n], data[n:], nil)
}
