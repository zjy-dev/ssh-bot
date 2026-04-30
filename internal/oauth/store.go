package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/redis/go-redis/v9"
)

// Store persists encrypted Credentials keyed by open_id (data-model.md §6).
type Store struct {
	rdb *redis.Client
	enc *Encryptor
}

// NewStore constructs a Store.
func NewStore(rdb *redis.Client, enc *Encryptor) *Store {
	return &Store{rdb: rdb, enc: enc}
}

func credKey(openID string) string { return "bot:oauth:" + openID }

// Get returns (nil, nil) if no credential is stored for openID.
func (s *Store) Get(ctx context.Context, openID string) (*Credential, error) {
	raw, err := s.rdb.Get(ctx, credKey(openID)).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("redis get oauth: %w", err)
	}
	pt, err := s.enc.Open(raw)
	if err != nil {
		return nil, fmt.Errorf("oauth credential auth: %w", err)
	}
	var c Credential
	if err := json.Unmarshal(pt, &c); err != nil {
		return nil, fmt.Errorf("oauth credential decode: %w", err)
	}
	return &c, nil
}

// Save writes the credential. TTL aligns with RefreshExpiresAt so the record
// auto-cleans once refresh is impossible.
func (s *Store) Save(ctx context.Context, c *Credential) error {
	if c == nil || c.OpenID == "" {
		return errors.New("credential has no open_id")
	}
	pt, err := json.Marshal(c)
	if err != nil {
		return err
	}
	envelope, err := s.enc.Seal(pt)
	if err != nil {
		return err
	}
	if err := s.rdb.Set(ctx, credKey(c.OpenID), envelope, 0).Err(); err != nil {
		return fmt.Errorf("redis set oauth: %w", err)
	}
	if !c.RefreshExpiresAt.IsZero() {
		// EXPIREAT aligns TTL with refresh expiry.
		if err := s.rdb.ExpireAt(ctx, credKey(c.OpenID), c.RefreshExpiresAt).Err(); err != nil {
			return fmt.Errorf("redis expireat oauth: %w", err)
		}
	}
	return nil
}

// Delete removes a credential; idempotent.
func (s *Store) Delete(ctx context.Context, openID string) error {
	return s.rdb.Del(ctx, credKey(openID)).Err()
}
