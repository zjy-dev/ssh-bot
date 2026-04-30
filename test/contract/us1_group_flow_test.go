package contract_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"

	"github.com/anomalyco/ssh-bot/internal/lark"
	"github.com/anomalyco/ssh-bot/internal/llm"
	"github.com/anomalyco/ssh-bot/internal/session"
)

// Parser-level assertions for US1.

func TestParse_GroupMentionedBot(t *testing.T) {
	raw := buildMessageEvent(t, "group", "oc_group1", "ou_user1", "@_user_1 hello", []string{"ou_bot"})
	ev, ok := lark.Parse(raw, "ou_bot")
	require.True(t, ok)
	require.Equal(t, "group", ev.ChatType)
	require.Equal(t, "oc_group1", ev.ChatID)
	require.Equal(t, "ou_user1", ev.SenderOpenID)
	require.True(t, ev.MentionedBot)
	require.Equal(t, "hello", ev.Text, "mention token should be stripped")
}

func TestParse_GroupNotMentioned(t *testing.T) {
	raw := buildMessageEvent(t, "group", "oc_g", "ou_u", "random message", []string{"ou_someone_else"})
	ev, ok := lark.Parse(raw, "ou_bot")
	require.True(t, ok)
	require.False(t, ev.MentionedBot)
}

func TestParse_P2P(t *testing.T) {
	raw := buildMessageEvent(t, "p2p", "oc_p2p", "ou_u", "hi", nil)
	ev, ok := lark.Parse(raw, "ou_bot")
	require.True(t, ok)
	require.Equal(t, "p2p", ev.ChatType)
	require.False(t, ev.MentionedBot) // p2p doesn't require mention; handler bypasses anyway.
	require.Equal(t, "hi", ev.Text)
}

func TestParse_NonTextSkipped(t *testing.T) {
	raw := &larkim.P2MessageReceiveV1{
		Event: &larkim.P2MessageReceiveV1Data{
			Sender:  &larkim.EventSender{SenderId: &larkim.UserId{OpenId: sptr("ou_u")}},
			Message: &larkim.EventMessage{MessageType: sptr("image"), ChatId: sptr("oc_p2p"), ChatType: sptr("p2p"), MessageId: sptr("m1")},
		},
	}
	ev, ok := lark.Parse(raw, "ou_bot")
	require.True(t, ok)
	require.Equal(t, "", ev.Text, "non-text message should yield empty Text")
}

func TestSessionKey_IsolationPerUserInGroup(t *testing.T) {
	// The same group, two users: keys must differ (data-model C1).
	a := &lark.MessageEvent{ChatType: "group", ChatID: "oc_g", SenderOpenID: "ou_a"}
	b := &lark.MessageEvent{ChatType: "group", ChatID: "oc_g", SenderOpenID: "ou_b"}
	require.NotEqual(t, lark.SessionKey(a), lark.SessionKey(b))

	// And same user in group vs. p2p: keys must differ.
	p := &lark.MessageEvent{ChatType: "p2p", ChatID: "oc_p2p", SenderOpenID: "ou_a"}
	require.NotEqual(t, lark.SessionKey(a), lark.SessionKey(p))
}

func TestIsCommand(t *testing.T) {
	require.True(t, lark.IsCommand("/clear"))
	require.True(t, lark.IsCommand("/model claude"))
	require.False(t, lark.IsCommand("clear"))
	require.False(t, lark.IsCommand(""))
}

// Session-append behavior (US1 FR-010): two users in the same group MUST
// have independent histories.
func TestUS1_PerUserSessionIsolation(t *testing.T) {
	store := session.NewMemoryStore()
	ctx := context.Background()

	a := &session.Session{Key: "group:oc_g:ou_a", UserOpenID: "ou_a", ChatID: "oc_g", ChatType: "group"}
	b := &session.Session{Key: "group:oc_g:ou_b", UserOpenID: "ou_b", ChatID: "oc_g", ChatType: "group"}

	a.Messages = append(a.Messages, llm.Message{Role: llm.RoleUser, Content: "user-A-question"})
	b.Messages = append(b.Messages, llm.Message{Role: llm.RoleUser, Content: "user-B-question"})

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); require.NoError(t, store.Save(ctx, a.Key, a)) }()
	go func() { defer wg.Done(); require.NoError(t, store.Save(ctx, b.Key, b)) }()
	wg.Wait()

	gotA, err := store.Get(ctx, a.Key)
	require.NoError(t, err)
	require.NotNil(t, gotA)
	require.Len(t, gotA.Messages, 1)
	require.True(t, strings.Contains(gotA.Messages[0].Content, "A"))

	gotB, err := store.Get(ctx, b.Key)
	require.NoError(t, err)
	require.NotNil(t, gotB)
	require.True(t, strings.Contains(gotB.Messages[0].Content, "B"))

	// Verify cross-contamination is impossible via API: no key would pull the
	// other user's data.
	cross, err := store.Get(ctx, "group:oc_g:ou_unknown")
	require.NoError(t, err)
	require.Nil(t, cross)
}

// ---- helpers ----

func buildMessageEvent(t *testing.T, chatType, chatID, senderOpenID, text string, mentionOpenIDs []string) *larkim.P2MessageReceiveV1 {
	t.Helper()
	content, _ := json.Marshal(map[string]string{"text": text})
	msg := &larkim.EventMessage{
		MessageId:   sptr("m1"),
		ChatId:      sptr(chatID),
		ChatType:    sptr(chatType),
		MessageType: sptr("text"),
		Content:     sptr(string(content)),
	}
	for i, oid := range mentionOpenIDs {
		_ = i
		oid := oid
		msg.Mentions = append(msg.Mentions, &larkim.MentionEvent{
			Key: sptr("@_user_1"),
			Id:  &larkim.UserId{OpenId: &oid},
		})
	}
	return &larkim.P2MessageReceiveV1{
		Event: &larkim.P2MessageReceiveV1Data{
			Sender:  &larkim.EventSender{SenderId: &larkim.UserId{OpenId: sptr(senderOpenID)}},
			Message: msg,
		},
	}
}

func sptr(s string) *string { return &s }
