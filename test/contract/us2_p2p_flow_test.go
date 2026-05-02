package contract_test

import (
	"context"
	"testing"

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
