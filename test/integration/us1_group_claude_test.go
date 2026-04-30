//go:build integration

package integration_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/anomalyco/ssh-bot/internal/agent"
	"github.com/anomalyco/ssh-bot/internal/llm"
	"github.com/anomalyco/ssh-bot/internal/llm/providers"
	"github.com/anomalyco/ssh-bot/internal/session"
	"github.com/anomalyco/ssh-bot/internal/tool"
	"github.com/anomalyco/ssh-bot/internal/tool/builtin"
)

// Real-Claude end-to-end test. Skipped unless ANTHROPIC_API_KEY is set.
// Requires a local Redis at localhost:6379.
func TestUS1_RealClaudeAnswer(t *testing.T) {
	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		t.Skip("ANTHROPIC_API_KEY not set; skipping real-Claude integration test")
	}
	rdb := redis.NewClient(&redis.Options{Addr: envOr("REDIS_ADDR", "localhost:6379")})
	defer rdb.Close()
	if err := rdb.Ping(context.Background()).Err(); err != nil {
		t.Skipf("redis not reachable: %v", err)
	}

	store := session.NewRedisStore(rdb, time.Hour)
	reg := tool.NewRegistry()
	require.NoError(t, reg.Register(builtin.NewDatetime()))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	prov, err := providers.NewClaude(ctx, llm.ModelProfile{
		Alias:          "claude",
		Type:           "claude",
		Model:          envOr("ANTHROPIC_MODEL", "claude-sonnet-4-5-20250929"),
		APIKeyEnv:      "ANTHROPIC_API_KEY",
		EnableThinking: false,
		MaxTokens:      1024,
	})
	require.NoError(t, err)

	a := agent.NewAgent(prov, reg, store)
	sess := &session.Session{
		Key:        "group:test:ou_integrationuser",
		UserOpenID: "ou_integrationuser",
		ChatID:     "test",
		ChatType:   "group",
		Provider:   "claude",
	}
	require.NoError(t, a.Run(ctx, sess, "Say 'ready' and nothing else.", nil))

	// Last message should be an assistant with non-empty content.
	require.GreaterOrEqual(t, len(sess.Messages), 2)
	last := sess.Messages[len(sess.Messages)-1]
	require.Equal(t, llm.RoleAssistant, last.Role)
	require.NotEmpty(t, last.Content)
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
