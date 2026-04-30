package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisStore implements Store backed by Redis.
type RedisStore struct {
	rdb *redis.Client
	ttl time.Duration
}

// NewRedisStore creates a RedisStore. ttl is applied on every Save (sliding TTL).
func NewRedisStore(rdb *redis.Client, ttl time.Duration) *RedisStore {
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	return &RedisStore{rdb: rdb, ttl: ttl}
}

func sessionRedisKey(key string) string { return "bot:sess:" + key }

func (s *RedisStore) Get(ctx context.Context, key string) (*Session, error) {
	raw, err := s.rdb.Get(ctx, sessionRedisKey(key)).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("redis get session: %w", err)
	}
	var sess Session
	if err := json.Unmarshal(raw, &sess); err != nil {
		return nil, fmt.Errorf("decode session: %w", err)
	}
	return &sess, nil
}

func (s *RedisStore) Save(ctx context.Context, key string, sess *Session) error {
	if sess == nil {
		return fmt.Errorf("nil session")
	}
	// Enforce caps before serializing (data-model C1).
	sess.Messages = TrimMessages(sess.Messages)
	sess.UpdatedAt = time.Now().UTC()
	if sess.CreatedAt.IsZero() {
		sess.CreatedAt = sess.UpdatedAt
	}
	raw, err := json.Marshal(sess)
	if err != nil {
		return fmt.Errorf("encode session: %w", err)
	}
	if err := s.rdb.Set(ctx, sessionRedisKey(key), raw, s.ttl).Err(); err != nil {
		return fmt.Errorf("redis set session: %w", err)
	}
	return nil
}

func (s *RedisStore) Delete(ctx context.Context, key string) error {
	if err := s.rdb.Del(ctx, sessionRedisKey(key)).Err(); err != nil {
		return fmt.Errorf("redis del session: %w", err)
	}
	return nil
}
