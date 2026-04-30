package session

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisLocker implements Locker using Redis SET NX EX + Lua-guarded DEL.
type RedisLocker struct {
	rdb *redis.Client
}

// NewRedisLocker creates a RedisLocker.
func NewRedisLocker(rdb *redis.Client) *RedisLocker {
	return &RedisLocker{rdb: rdb}
}

func lockRedisKey(key string) string { return "bot:lock:" + key }

// releaseScript atomically checks the token match and deletes.
var releaseScript = redis.NewScript(`
if redis.call("GET", KEYS[1]) == ARGV[1] then
  return redis.call("DEL", KEYS[1])
else
  return 0
end
`)

// TryAcquire attempts SET NX EX <ttl>. Returns (token, true, nil) on acquire,
// ("", false, nil) when another holder owns it.
func (l *RedisLocker) TryAcquire(ctx context.Context, key string, ttl time.Duration) (string, bool, error) {
	if ttl <= 0 {
		ttl = 60 * time.Second
	}
	token, err := randomToken()
	if err != nil {
		return "", false, err
	}
	ok, err := l.rdb.SetNX(ctx, lockRedisKey(key), token, ttl).Result()
	if err != nil {
		return "", false, fmt.Errorf("redis setnx: %w", err)
	}
	if !ok {
		return "", false, nil
	}
	return token, true, nil
}

// Release runs the Lua script; a non-matching token is a no-op.
func (l *RedisLocker) Release(ctx context.Context, key, token string) error {
	if token == "" {
		return nil
	}
	_, err := releaseScript.Run(ctx, l.rdb, []string{lockRedisKey(key)}, token).Result()
	if err != nil && !errors.Is(err, redis.Nil) {
		return fmt.Errorf("redis release: %w", err)
	}
	return nil
}

func randomToken() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
