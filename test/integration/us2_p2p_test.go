package integration_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/anomalyco/ssh-bot/internal/lark"
	"github.com/anomalyco/ssh-bot/internal/llm"
	"github.com/anomalyco/ssh-bot/internal/session"
	"github.com/anomalyco/ssh-bot/internal/tool"
	"github.com/anomalyco/ssh-bot/internal/tool/builtin"
)

type integrationProvider struct {
	calls int
}

func (p *integrationProvider) Name() string { return "claude" }

func (p *integrationProvider) Stream(ctx context.Context, _ llm.ChatRequest) (<-chan llm.StreamEvent, error) {
	p.calls++
	ch := make(chan llm.StreamEvent, 2)
	go func() {
		defer close(ch)
		select {
		case <-ctx.Done():
			ch <- llm.StreamEvent{Type: llm.EventError, Err: ctx.Err()}
		case ch <- llm.StreamEvent{Type: llm.EventTextDelta, Text: "integration reply"}:
		}
		ch <- llm.StreamEvent{Type: llm.EventMessageEnd}
	}()
	return ch, nil
}

type integrationSender struct {
	initials int
	patches  [][]byte
}

func (s *integrationSender) SendInitialCard(context.Context, string) (string, error) {
	s.initials++
	return "mid_int", nil
}

func (s *integrationSender) SendMessage(context.Context, string, string) error { return nil }

func (s *integrationSender) Patch(_ context.Context, _ string, body []byte) error {
	cp := make([]byte, len(body))
	copy(cp, body)
	s.patches = append(s.patches, cp)
	return nil
}

func (s *integrationSender) ReplyInThread(context.Context, string, string) error { return nil }

type integrationLocker struct{}

func (integrationLocker) TryAcquire(context.Context, string, time.Duration) (string, bool, error) {
	return "tok", true, nil
}

func (integrationLocker) Release(context.Context, string, string) error { return nil }

func TestUS2_P2PHandlerFlow(t *testing.T) {
	store := session.NewMemoryStore()
	provider := &integrationProvider{}
	llms, err := llm.NewRegistry(context.Background(), "claude", []llm.ModelProfile{{Alias: "claude", Type: "fake"}}, func(context.Context, llm.ModelProfile) (llm.Provider, error) {
		return provider, nil
	}, nil)
	require.NoError(t, err)
	reg := tool.NewRegistry()
	require.NoError(t, reg.Register(builtin.NewDatetime()))
	sender := &integrationSender{}
	h := lark.NewHandler(store, integrationLocker{}, sender, reg, llms, "ou_bot", "claude", nil)

	ev := &lark.MessageEvent{ChatID: "oc_p2p", ChatType: "p2p", SenderOpenID: "ou_user", MessageID: "m1", Text: "你好"}
	require.NoError(t, h.Handle(context.Background(), ev))
	require.Equal(t, 1, provider.calls)
	require.Equal(t, 1, sender.initials)
	require.NotEmpty(t, sender.patches)

	last := sender.patches[len(sender.patches)-1]
	require.Contains(t, string(last), "integration reply")
	require.Contains(t, string(last), "trace:")

	sess, err := store.Get(context.Background(), "p2p:ou_user")
	require.NoError(t, err)
	require.NotNil(t, sess)
	require.Len(t, sess.Messages, 2)

	var parsed map[string]any
	require.NoError(t, json.Unmarshal(last, &parsed))
}
