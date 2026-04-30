package oauth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"time"
)

// Credential is the unencrypted in-memory form of a UserOAuthCredential
// (data-model.md §6).
type Credential struct {
	OpenID           string    `json:"open_id"`
	AccessToken      string    `json:"access_token"`
	RefreshToken     string    `json:"refresh_token"`
	AccessExpiresAt  time.Time `json:"access_expires_at"`
	RefreshExpiresAt time.Time `json:"refresh_expires_at"`
	Scopes           []string  `json:"scopes,omitempty"`
	GrantedAt        time.Time `json:"granted_at"`
	LastUsedAt       time.Time `json:"last_used_at,omitempty"`
}

// Encryptor seals and opens JSON-serialized Credentials.
type Encryptor struct{ aead cipher.AEAD }

// NewEncryptor constructs an AES-GCM AEAD from the given base64-encoded
// 32-byte key. Fail-closed on malformed/missing key (FR-047 invariant).
func NewEncryptor(base64Key string) (*Encryptor, error) {
	if base64Key == "" {
		return nil, errors.New("encryption key env is empty")
	}
	k, err := base64.StdEncoding.DecodeString(base64Key)
	if err != nil {
		return nil, fmt.Errorf("encryption key base64: %w", err)
	}
	if len(k) != 32 {
		return nil, fmt.Errorf("encryption key must be 32 bytes after base64 decode (got %d)", len(k))
	}
	block, err := aes.NewCipher(k)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Encryptor{aead: aead}, nil
}

// Seal returns nonce||ciphertext bytes.
func (e *Encryptor) Seal(plaintext []byte) ([]byte, error) {
	nonce := make([]byte, e.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return e.aead.Seal(nonce, nonce, plaintext, nil), nil
}

// Open reverses Seal. Returns error on auth failure.
func (e *Encryptor) Open(envelope []byte) ([]byte, error) {
	ns := e.aead.NonceSize()
	if len(envelope) < ns {
		return nil, errors.New("ciphertext too short")
	}
	nonce, ct := envelope[:ns], envelope[ns:]
	return e.aead.Open(nil, nonce, ct, nil)
}
