package contract_test

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/anomalyco/ssh-bot/internal/llm"
	"github.com/anomalyco/ssh-bot/internal/session"
)

func newTestRedis(t *testing.T) *redis.Client {
	t.Helper()
	s := miniredis.RunT(t)
	return redis.NewClient(&redis.Options{Addr: s.Addr()})
}

func TestSessionStore_GetMissingReturnsNilNil(t *testing.T) {
	store := session.NewRedisStore(newTestRedis(t), time.Hour)
	got, err := store.Get(context.Background(), "no-such-key")
	require.NoError(t, err)
	require.Nil(t, got)
}

func TestSessionStore_SaveGetDelete(t *testing.T) {
	store := session.NewRedisStore(newTestRedis(t), time.Hour)
	ctx := context.Background()

	sess := &session.Session{
		Key:        "p2p:ou_abc",
		UserOpenID: "ou_abc",
		ChatID:     "chat_abc",
		ChatType:   "p2p",
		Provider:   "claude",
		Messages: []llm.Message{
			{Role: llm.RoleUser, Content: "hello"},
		},
	}
	require.NoError(t, store.Save(ctx, sess.Key, sess))
	require.False(t, sess.UpdatedAt.IsZero(), "Save should stamp UpdatedAt")

	got, err := store.Get(ctx, sess.Key)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, sess.Key, got.Key)
	require.Len(t, got.Messages, 1)
	require.Equal(t, "hello", got.Messages[0].Content)

	require.NoError(t, store.Delete(ctx, sess.Key))
	// idempotent
	require.NoError(t, store.Delete(ctx, sess.Key))
	got, err = store.Get(ctx, sess.Key)
	require.NoError(t, err)
	require.Nil(t, got)
}

func TestSessionStore_TTLRefresh(t *testing.T) {
	s := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: s.Addr()})
	store := session.NewRedisStore(rdb, 10*time.Second)
	ctx := context.Background()

	require.NoError(t, store.Save(ctx, "k", &session.Session{Key: "k"}))
	ttl1 := s.TTL("bot:sess:k")
	require.InDelta(t, 10*time.Second, ttl1, float64(time.Second))

	// Fast-forward 5s, then Save again; TTL must reset to 10s (sliding).
	s.FastForward(5 * time.Second)
	require.NoError(t, store.Save(ctx, "k", &session.Session{Key: "k"}))
	ttl2 := s.TTL("bot:sess:k")
	require.InDelta(t, 10*time.Second, ttl2, float64(time.Second), "save should reset TTL")
}

func TestSessionStore_TruncatesLongContent(t *testing.T) {
	store := session.NewRedisStore(newTestRedis(t), time.Hour)
	ctx := context.Background()

	long := make([]byte, session.MaxMessageContent+5000)
	for i := range long {
		long[i] = 'A'
	}
	sess := &session.Session{
		Key:      "k",
		Messages: []llm.Message{{Role: llm.RoleAssistant, Content: string(long)}},
	}
	require.NoError(t, store.Save(ctx, sess.Key, sess))
	got, err := store.Get(ctx, sess.Key)
	require.NoError(t, err)
	require.Less(t, len(got.Messages[0].Content), session.MaxMessageContent+200)
	require.Contains(t, got.Messages[0].Content, "[… truncated")
}

func TestLocker_AcquireReleaseReacquire(t *testing.T) {
	rdb := newTestRedis(t)
	locker := session.NewRedisLocker(rdb)
	ctx := context.Background()

	tok1, ok, err := locker.TryAcquire(ctx, "k", time.Minute)
	require.NoError(t, err)
	require.True(t, ok)
	require.NotEmpty(t, tok1)

	// Second acquire while held: ok=false, err=nil.
	_, ok2, err := locker.TryAcquire(ctx, "k", time.Minute)
	require.NoError(t, err)
	require.False(t, ok2)

	require.NoError(t, locker.Release(ctx, "k", tok1))
	// Release with stale/empty token is a no-op.
	require.NoError(t, locker.Release(ctx, "k", ""))
	require.NoError(t, locker.Release(ctx, "k", "bogus"))

	// After release, re-acquire succeeds with a new token.
	tok2, ok, err := locker.TryAcquire(ctx, "k", time.Minute)
	require.NoError(t, err)
	require.True(t, ok)
	require.NotEqual(t, tok1, tok2)
}

func TestLocker_ForeignTokenDoesNotRelease(t *testing.T) {
	s := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: s.Addr()})
	locker := session.NewRedisLocker(rdb)
	ctx := context.Background()

	tokA, ok, err := locker.TryAcquire(ctx, "k", time.Minute)
	require.NoError(t, err)
	require.True(t, ok)

	// Attempt release with a wrong token: must NOT delete.
	require.NoError(t, locker.Release(ctx, "k", "wrong-token"))
	// The real owner can still release.
	require.True(t, s.Exists("bot:lock:k"))
	require.NoError(t, locker.Release(ctx, "k", tokA))
	require.False(t, s.Exists("bot:lock:k"))
}
