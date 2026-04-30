// Package lark is the Feishu/Lark integration layer. It:
//   - parses inbound im.message.receive_v1 events,
//   - exposes a long-connection ws dispatcher,
//   - sends + PATCHes card messages,
//   - intercepts short-circuit commands before the agent loop runs.
//
// See contracts/go-interfaces.md#internal-lark and spec FR-001..FR-023.
package lark

import (
	"encoding/json"
	"regexp"
	"strings"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

// MessageEvent is the handler-level view of an inbound message after parsing.
type MessageEvent struct {
	ChatID       string
	ChatType     string // "p2p" | "group" | "topic_group"
	SenderOpenID string
	MessageID    string
	// Text is the plain text the user actually wrote, with @-mentions stripped.
	Text string
	// MentionedBot is true if this message mentions THIS bot (matches
	// configured bot_open_id). Always false in p2p chats (we auto-accept).
	MentionedBot bool
	// Raw retains the provider-native struct for advanced debugging.
	Raw *larkim.P2MessageReceiveV1
}

// textContent is the JSON shape Feishu uses for message_type="text".
type textContent struct {
	Text string `json:"text"`
}

// Parse turns a raw P2MessageReceiveV1 into a MessageEvent.
// botOpenID is the configured bot's open_id; used to detect @-mentions.
//
// Only message_type="text" is handled for v1. Other types yield an event with
// Text = "" — the caller (handler.go) can ignore or respond with "不支持" per
// spec Edge Cases.
func Parse(raw *larkim.P2MessageReceiveV1, botOpenID string) (*MessageEvent, bool) {
	if raw == nil || raw.Event == nil || raw.Event.Message == nil || raw.Event.Sender == nil {
		return nil, false
	}
	msg := raw.Event.Message

	ev := &MessageEvent{
		ChatID:    str(msg.ChatId),
		ChatType:  str(msg.ChatType),
		MessageID: str(msg.MessageId),
		Raw:       raw,
	}
	// Sender open_id
	if raw.Event.Sender != nil && raw.Event.Sender.SenderId != nil {
		ev.SenderOpenID = str(raw.Event.Sender.SenderId.OpenId)
	}

	// Detect @-mention of THIS bot.
	if botOpenID != "" {
		for _, m := range msg.Mentions {
			if m == nil || m.Id == nil {
				continue
			}
			if m.Id.OpenId != nil && *m.Id.OpenId == botOpenID {
				ev.MentionedBot = true
				break
			}
		}
	}

	// Extract text. Feishu sends text messages as {"text":"..."}.
	text := extractText(msg.MessageType, msg.Content)
	// Strip @-mentions ("@_user_1" keys) from the text.
	text = stripMentions(text)
	ev.Text = strings.TrimSpace(text)
	return ev, true
}

var mentionKeyRE = regexp.MustCompile(`@_user_\d+\s*`)

// stripMentions removes Feishu's @-mention placeholder tokens from text.
func stripMentions(s string) string {
	return mentionKeyRE.ReplaceAllString(s, "")
}

// extractText returns the plain text of a text-type message; empty string for
// other types.
func extractText(msgType, content *string) string {
	if msgType == nil || content == nil {
		return ""
	}
	if *msgType != "text" {
		return ""
	}
	var tc textContent
	if err := json.Unmarshal([]byte(*content), &tc); err != nil {
		return ""
	}
	return tc.Text
}

// SessionKey returns the Redis session key per FR-010:
//
//	p2p:<open_id> for DMs, group:<chat_id>:<open_id> for groups.
func SessionKey(ev *MessageEvent) string {
	if ev.ChatType == "p2p" {
		return "p2p:" + ev.SenderOpenID
	}
	return "group:" + ev.ChatID + ":" + ev.SenderOpenID
}

// IsCommand reports whether text is a `/` command the handler should intercept.
func IsCommand(text string) bool {
	return strings.HasPrefix(text, "/")
}

// str safely dereferences a *string.
func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
