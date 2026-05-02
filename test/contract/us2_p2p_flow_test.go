package contract_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/anomalyco/ssh-bot/internal/lark"
	"github.com/anomalyco/ssh-bot/internal/llm"
	"github.com/anomalyco/ssh-bot/internal/session"
	"github.com/anomalyco/ssh-bot/internal/tool/builtin"
)

func TestUS2_P2PAcceptsWithoutMentionAndUsesP2PSessionKey(t *testing.T) {
	store := session.NewMemoryStore()
	sender := newFakeSender()
	provider := &countingProvider{
		name: "claude",
		events: []llm.StreamEvent{
			{Type: llm.EventTextDelta, Text: "你好，我在。"},
			{Type: llm.EventMessageEnd},
		},
	}
	h := newHandlerForTest(store, unlockedLocker{}, sender, mustRegistry(builtin.NewDatetime()), newLLMRegistry(provider))

	ev := &lark.MessageEvent{
		ChatID:       "oc_p2p_1",
		ChatType:     "p2p",
		SenderOpenID: "ou_user_dm",
		MessageID:    "m_p2p_1",
		Text:         "你好",
		MentionedBot: false,
	}
	require.NoError(t, h.Handle(context.Background(), ev))
	require.Equal(t, 1, provider.Calls(), "p2p message should reach the LLM without @ mention")

	sess, err := store.Get(context.Background(), "p2p:ou_user_dm")
	require.NoError(t, err)
	require.NotNil(t, sess)
	require.Equal(t, "p2p:ou_user_dm", sess.Key)
	require.Equal(t, "p2p", sess.ChatType)
	require.Len(t, sess.Messages, 2)
	require.Equal(t, llm.RoleUser, sess.Messages[0].Role)
	require.Equal(t, llm.RoleAssistant, sess.Messages[1].Role)
	require.Contains(t, sess.Messages[1].Content, "你好")
	require.NotEmpty(t, sender.initialCards)
	require.NotEmpty(t, sender.patchedBodies)
}

type slowPatchSender struct {
	*fakeSender
	delay time.Duration
	mu    sync.Mutex
	count int
}

func (s *slowPatchSender) Patch(ctx context.Context, messageID string, cardJSON []byte) error {
	time.Sleep(s.delay)
	s.mu.Lock()
	s.count++
	s.mu.Unlock()
	return s.fakeSender.Patch(ctx, messageID, cardJSON)
}

func (s *slowPatchSender) PatchCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.count
}

func TestUS2_P2PLongStreamDoesNotDropTextUnderBackpressure(t *testing.T) {
	store := session.NewMemoryStore()
	sender := &slowPatchSender{fakeSender: newFakeSender(), delay: 20 * time.Millisecond}

	var chunks []llm.StreamEvent
	var want strings.Builder
	for i := 0; i < 300; i++ {
		chunk := "abcdefghij"
		want.WriteString(chunk)
		chunks = append(chunks, llm.StreamEvent{Type: llm.EventTextDelta, Text: chunk})
	}
	chunks = append(chunks, llm.StreamEvent{Type: llm.EventMessageEnd})

	provider := &countingProvider{name: "claude", events: chunks}
	h := newHandlerForTest(store, unlockedLocker{}, sender, mustRegistry(builtin.NewDatetime()), newLLMRegistry(provider))

	ev := &lark.MessageEvent{
		ChatID:       "oc_p2p_backpressure",
		ChatType:     "p2p",
		SenderOpenID: "ou_user_backpressure",
		MessageID:    "m_backpressure",
		Text:         "请长一点回答",
	}
	require.NoError(t, h.Handle(context.Background(), ev))

	sess, err := store.Get(context.Background(), "p2p:ou_user_backpressure")
	require.NoError(t, err)
	require.NotNil(t, sess)
	require.Len(t, sess.Messages, 2)
	require.Equal(t, want.String(), sess.Messages[1].Content)

	sender.mu.Lock()
	require.NotEmpty(t, sender.patchedBodies)
	last := sender.patchedBodies[len(sender.patchedBodies)-1]
	sender.mu.Unlock()
	require.Contains(t, string(last), want.String())
	require.Less(t, sender.PatchCount(), 300, "batched rendering should avoid patching once per chunk")
}
