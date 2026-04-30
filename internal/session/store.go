// Package session manages per-user conversation context and per-user locks.
// See contracts/go-interfaces.md#internal-session and data-model.md.
package session

import (
	"context"
	"time"

	"github.com/anomalyco/ssh-bot/internal/llm"
)

// Session is a user's conversation context. See data-model.md §1.
type Session struct {
	Key        string        `json:"key"`
	UserOpenID string        `json:"user_open_id"`
	ChatID     string        `json:"chat_id"`
	ChatType   string        `json:"chat_type"`
	Provider   string        `json:"provider"`
	Messages   []llm.Message `json:"messages"`
	CreatedAt  time.Time     `json:"created_at"`
	UpdatedAt  time.Time     `json:"updated_at"`
	TraceID    string        `json:"trace_id_last"`
}

// MaxMessages caps the number of stored messages per session (data-model C1).
const MaxMessages = 40

// MaxMessageContent caps the length of any single stored message's content.
// Longer content is truncated pre-storage. (data-model.md §1 validation)
const MaxMessageContent = 20 * 1024

// Store persists sessions.
type Store interface {
	// Get returns (nil, nil) when the key does not exist. That is the only
	// signal for "no session yet"; callers construct a fresh Session in memory.
	Get(ctx context.Context, key string) (*Session, error)
	Save(ctx context.Context, key string, sess *Session) error
	Delete(ctx context.Context, key string) error
}

// Locker provides per-key mutually-exclusive locks for short-term serialization
// of the same user's concurrent messages (FR-012).
type Locker interface {
	// TryAcquire returns (token, true, nil) on success;
	// ("", false, nil) when another holder is active (NOT an error: caller
	// replies with "上一条还在处理中").
	// Any infrastructure error is returned as (_, _, error).
	TryAcquire(ctx context.Context, key string, ttl time.Duration) (token string, ok bool, err error)
	// Release releases the lock iff the caller's token matches the stored one.
	// Idempotent: no-op if the lock has already expired or is owned by someone else.
	Release(ctx context.Context, key, token string) error
}

// TrimMessages enforces MaxMessages and MaxMessageContent in-place.
// The oldest messages are dropped first. Over-length content is truncated with
// a marker suffix.
func TrimMessages(msgs []llm.Message) []llm.Message {
	for i := range msgs {
		if len(msgs[i].Content) > MaxMessageContent {
			truncated := len(msgs[i].Content) - MaxMessageContent
			msgs[i].Content = msgs[i].Content[:MaxMessageContent] +
				"\n[… truncated " + itoa(truncated) + " chars]"
		}
	}
	if len(msgs) > MaxMessages {
		msgs = msgs[len(msgs)-MaxMessages:]
	}
	return msgs
}

// itoa avoids importing strconv in this hot path.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	buf := make([]byte, 0, 12)
	for n > 0 {
		buf = append(buf, byte('0'+n%10))
		n /= 10
	}
	// reverse
	for i, j := 0, len(buf)-1; i < j; i, j = i+1, j-1 {
		buf[i], buf[j] = buf[j], buf[i]
	}
	return string(buf)
}
