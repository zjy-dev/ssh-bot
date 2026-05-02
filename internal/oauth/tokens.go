package oauth

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
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

// ReauthorizeError asks the caller to restart the OAuth flow for this user.
type ReauthorizeError struct {
	URL string
}

func (e *ReauthorizeError) Error() string {
	if e == nil || e.URL == "" {
		return "reauthorization required"
	}
	return "reauthorization required: " + e.URL
}

// TokenRefresher refreshes user tokens before use and persists rotations.
type TokenRefresher struct {
	AppID     string
	AppSecret string
	Store     *Store
	StartURL  func(openID string) (string, error)
	Client    *http.Client
}

func NewTokenRefresher(appID, appSecret string, store *Store, startURL func(string) (string, error)) *TokenRefresher {
	return &TokenRefresher{
		AppID:     appID,
		AppSecret: appSecret,
		Store:     store,
		StartURL:  startURL,
		Client:    &http.Client{Timeout: 10 * time.Second},
	}
}

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

// RefreshIfNeeded refreshes cred if it is within 60s of expiry. Any 4xx-style
// refresh failure invalidates the stored credential and asks the caller to
// restart OAuth.
func (r *TokenRefresher) RefreshIfNeeded(ctx context.Context, cred *Credential) (*Credential, error) {
	if cred == nil {
		return nil, nil
	}
	if cred.AccessExpiresAt.After(time.Now().UTC().Add(60 * time.Second)) {
		return cred, nil
	}
	if cred.RefreshToken == "" {
		return nil, r.reauthorize(ctx, cred.OpenID)
	}

	body := map[string]string{
		"grant_type":    "refresh_token",
		"client_id":     r.AppID,
		"client_secret": r.AppSecret,
		"refresh_token": cred.RefreshToken,
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, FeishuTokenEndpoint, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")

	cli := r.Client
	if cli == nil {
		cli = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := cli.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	var tok struct {
		AccessToken           string `json:"access_token"`
		ExpiresIn             int    `json:"expires_in"`
		RefreshToken          string `json:"refresh_token"`
		RefreshTokenExpiresIn int    `json:"refresh_token_expires_in"`
		Scope                 string `json:"scope"`
		Code                  int    `json:"code"`
		Error                 string `json:"error,omitempty"`
		ErrorDesc             string `json:"error_description,omitempty"`
	}
	_ = json.Unmarshal(respBody, &tok)

	if resp.StatusCode >= 400 || tok.Code != 0 || tok.Error != "" {
		if resp.StatusCode >= 400 && resp.StatusCode < 500 || tok.Code != 0 || tok.Error != "" {
			if r.Store != nil && cred.OpenID != "" {
				_ = r.Store.Delete(ctx, cred.OpenID)
			}
			return nil, r.reauthorize(ctx, cred.OpenID)
		}
		return nil, fmt.Errorf("refresh token endpoint status=%d code=%d", resp.StatusCode, tok.Code)
	}
	if tok.AccessToken == "" {
		return nil, errors.New("refresh returned empty access_token")
	}

	now := time.Now().UTC()
	updated := *cred
	updated.AccessToken = tok.AccessToken
	if tok.RefreshToken != "" {
		updated.RefreshToken = tok.RefreshToken
	}
	updated.AccessExpiresAt = now.Add(time.Duration(tok.ExpiresIn) * time.Second)
	if tok.RefreshTokenExpiresIn > 0 {
		updated.RefreshExpiresAt = now.Add(time.Duration(tok.RefreshTokenExpiresIn) * time.Second)
	}
	if tok.Scope != "" {
		updated.Scopes = strings.Fields(tok.Scope)
	}
	if r.Store != nil {
		if err := r.Store.Save(ctx, &updated); err != nil {
			return nil, err
		}
	}
	return &updated, nil
}

func (r *TokenRefresher) reauthorize(ctx context.Context, openID string) error {
	if r.Store != nil && openID != "" {
		_ = r.Store.Delete(ctx, openID)
	}
	if r.StartURL == nil {
		return &ReauthorizeError{}
	}
	url, err := r.StartURL(openID)
	if err != nil {
		return err
	}
	return &ReauthorizeError{URL: url}
}
