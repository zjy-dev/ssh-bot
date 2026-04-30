// Package oauth implements the per-user Feishu OAuth flow (Q1=B):
//   - /oauth/start → redirect to accounts.feishu.cn/open-apis/authen/v1/authorize
//   - /oauth/callback → exchange code at authen/v2/oauth/token, store tokens
//
// Tokens are AES-GCM encrypted at rest (FR-047) with a 32-byte key loaded
// from env at startup (fail-closed on missing key).
//
// See contracts/http-endpoints.md.
package oauth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
)

// ErrInvalidState is returned when HMAC verification fails on an inbound
// /oauth/callback request.
var ErrInvalidState = errors.New("invalid state")

// StateSigner signs and verifies opaque state tokens used in the OAuth
// authorize → callback handshake.
//
// Layout: base64url(open_id || 0x00 || nonce16 || hmac_sha256(key, above)[:16]).
// Nonce prevents replay with the same open_id; HMAC prevents user-controlled
// state from bypassing the flow.
type StateSigner struct{ key []byte }

// NewStateSigner reads the key material from the given 32-byte (after base64
// decode) secret. Fail-closed on bad length.
func NewStateSigner(base64Key string) (*StateSigner, error) {
	if base64Key == "" {
		return nil, errors.New("state key env is empty")
	}
	k, err := base64.StdEncoding.DecodeString(base64Key)
	if err != nil {
		return nil, fmt.Errorf("state key base64: %w", err)
	}
	if len(k) != 32 {
		return nil, fmt.Errorf("state key must be 32 bytes after base64 decode (got %d)", len(k))
	}
	return &StateSigner{key: k}, nil
}

// Sign returns an opaque state token embedding openID.
func (s *StateSigner) Sign(openID string) (string, error) {
	if openID == "" {
		return "", errors.New("sign: empty open_id")
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	payload := make([]byte, 0, len(openID)+1+16)
	payload = append(payload, openID...)
	payload = append(payload, 0)
	payload = append(payload, nonce[:]...)
	mac := hmac.New(sha256.New, s.key)
	mac.Write(payload)
	tag := mac.Sum(nil)[:16]
	full := append(payload, tag...)
	return base64.RawURLEncoding.EncodeToString(full), nil
}

// Verify validates state and returns the embedded open_id.
func (s *StateSigner) Verify(state string) (string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(state)
	if err != nil {
		return "", ErrInvalidState
	}
	if len(raw) < 1+16+16 {
		return "", ErrInvalidState
	}
	tag := raw[len(raw)-16:]
	payload := raw[:len(raw)-16]
	mac := hmac.New(sha256.New, s.key)
	mac.Write(payload)
	expected := mac.Sum(nil)[:16]
	if !hmac.Equal(tag, expected) {
		return "", ErrInvalidState
	}
	// payload = open_id || 0x00 || nonce16
	nul := -1
	for i := 0; i < len(payload); i++ {
		if payload[i] == 0 {
			nul = i
			break
		}
	}
	if nul <= 0 {
		return "", ErrInvalidState
	}
	openID := string(payload[:nul])
	return openID, nil
}
