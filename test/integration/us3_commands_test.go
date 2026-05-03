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

type noopProvider struct{ calls int }

func (p *noopProvider) Name() string { return "claude" }

func (p *noopProvider) Stream(context.Context, llm.ChatRequest) (<-chan llm.StreamEvent, error) {
	p.calls++
	ch := make(chan llm.StreamEvent)
	close(ch)
	return ch, nil
}

type commandSender struct {
	messages   []string
	plainCards []string
}

func (s *commandSender) SendInitialCard(context.Context, string) (string, error) { return "", nil }
func (s *commandSender) SendMessage(_ context.Context, _ string, text string) error {
	s.messages = append(s.messages, text)
	return nil
}
func (s *commandSender) SendPlainCard(_ context.Context, _ string, text string) error {
	s.plainCards = append(s.plainCards, text)
	return nil
}
func (s *commandSender) Patch(context.Context, string, []byte) error         { return nil }
func (s *commandSender) ReplyInThread(context.Context, string, string) error { return nil }

func TestUS3_CommandsNeverInvokeProvider(t *testing.T) {
	store := session.NewMemoryStore()
	provider := &noopProvider{}
	llms, err := llm.NewRegistry(context.Background(), "claude", []llm.ModelProfile{{Alias: "claude", Type: "fake"}}, func(context.Context, llm.ModelProfile) (llm.Provider, error) {
		return provider, nil
	}, nil)
	require.NoError(t, err)

	reg := tool.NewRegistry()
	require.NoError(t, reg.Register(builtin.NewDatetime()))
	require.NoError(t, reg.Register(integrationListTool{name: "web_search"}))
	sender := &commandSender{}
	h := lark.NewHandler(store, commandLocker{}, sender, reg, llms, "ou_bot", "claude", nil)

	commands := []string{"/help", "/tools", "/whoami", "/model claude", "/clear"}
	for _, cmd := range commands {
		ev := &lark.MessageEvent{ChatID: "oc_p2p", ChatType: "p2p", SenderOpenID: "ou_user", Text: cmd}
		require.NoError(t, h.Handle(context.Background(), ev), cmd)
	}

	require.Equal(t, 0, provider.calls)
	require.Len(t, sender.plainCards, len(commands))
	require.Empty(t, sender.messages)
	require.Contains(t, sender.plainCards[0], "可用指令")
	require.Contains(t, sender.plainCards[1], "web_search")
	require.Contains(t, sender.plainCards[2], "session_key")
	require.Contains(t, sender.plainCards[3], "claude")
	require.Contains(t, sender.plainCards[4], "已清空上下文")
}

type integrationListTool struct{ name string }

func (t integrationListTool) Name() string        { return t.name }
func (t integrationListTool) Description() string { return "list tool" }
func (t integrationListTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object"}`)
}
func (t integrationListTool) Source() tool.Source { return tool.SourceBuiltin }
func (t integrationListTool) Available() bool     { return true }
func (t integrationListTool) Call(context.Context, json.RawMessage) (tool.Result, error) {
	return tool.Result{Content: "ok"}, nil
}

type commandLocker struct{}

func (commandLocker) TryAcquire(context.Context, string, time.Duration) (string, bool, error) {
	return "", false, nil
}

func (commandLocker) Release(context.Context, string, string) error { return nil }
