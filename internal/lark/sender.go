package lark

import (
	"context"
	"errors"
	"fmt"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

// ErrRateLimited is returned by Sender.Patch when Feishu responds with
// 230020 (per-message frequency limit). Renderer subscribes to this sentinel.
var ErrRateLimited = errors.New("lark: card update rate-limited (230020)")

// Sender is the thin wrapper around the Lark SDK that the bot actually uses
// outward-facing. It implements the render.Sender interface.
type Sender struct {
	client *lark.Client
}

// NewSender constructs a Sender from a pre-built *lark.Client.
func NewSender(client *lark.Client) *Sender { return &Sender{client: client} }

// SendInitialCard posts a fresh interactive card to chatID and returns the
// new message_id (used for subsequent Patch calls).
func (s *Sender) SendInitialCard(ctx context.Context, chatID string) (string, error) {
	cardJSON := string(InitialCardJSON())
	req := larkim.NewCreateMessageReqBuilder().
		ReceiveIdType(larkim.ReceiveIdTypeChatId).
		Body(larkim.NewCreateMessageReqBodyBuilder().
			ReceiveId(chatID).
			MsgType(larkim.MsgTypeInteractive).
			Content(cardJSON).
			Build()).
		Build()
	resp, err := s.client.Im.Message.Create(ctx, req)
	if err != nil {
		return "", fmt.Errorf("lark create message: %w", err)
	}
	if !resp.Success() {
		return "", fmt.Errorf("lark create message: code=%d msg=%s", resp.Code, resp.Msg)
	}
	if resp.Data == nil || resp.Data.MessageId == nil {
		return "", fmt.Errorf("lark create message: no message_id in response")
	}
	return *resp.Data.MessageId, nil
}

// Patch replaces the card on an existing message. Returns ErrRateLimited on
// code 230020 so the caller (renderer) can back off.
func (s *Sender) Patch(ctx context.Context, messageID string, cardJSON []byte) error {
	content := string(cardJSON)
	req := larkim.NewPatchMessageReqBuilder().
		MessageId(messageID).
		Body(larkim.NewPatchMessageReqBodyBuilder().
			Content(content).
			Build()).
		Build()
	resp, err := s.client.Im.Message.Patch(ctx, req)
	if err != nil {
		return fmt.Errorf("lark patch message: %w", err)
	}
	if !resp.Success() {
		if resp.Code == 230020 {
			return ErrRateLimited
		}
		return fmt.Errorf("lark patch message: code=%d msg=%s", resp.Code, resp.Msg)
	}
	return nil
}

// ReplyInThread posts a follow-up text message in the same thread as
// rootMessageID. Used for splitting long bot answers across multiple messages
// (FR-034).
func (s *Sender) ReplyInThread(ctx context.Context, rootMessageID, text string) error {
	// Wrap as Feishu "text" content.
	content, _ := jsonMarshalIndirect(map[string]string{"text": text})
	req := larkim.NewReplyMessageReqBuilder().
		MessageId(rootMessageID).
		Body(larkim.NewReplyMessageReqBodyBuilder().
			Content(string(content)).
			MsgType(larkim.MsgTypeText).
			Build()).
		Build()
	resp, err := s.client.Im.Message.Reply(ctx, req)
	if err != nil {
		return fmt.Errorf("lark reply message: %w", err)
	}
	if !resp.Success() {
		return fmt.Errorf("lark reply message: code=%d msg=%s", resp.Code, resp.Msg)
	}
	return nil
}

// SendPlainCard posts a one-shot card carrying a single markdown text. Used
// by command handlers that don't need streaming updates.
func (s *Sender) SendPlainCard(ctx context.Context, chatID, text string) error {
	return s.sendPlainCard(ctx, larkim.ReceiveIdTypeChatId, chatID, text)
}

// SendPlainCardByOpenID posts a one-shot card to the user's p2p chat with the
// bot (using ReceiveIdType=open_id). Used by /oauth/callback confirmations.
func (s *Sender) SendPlainCardByOpenID(ctx context.Context, openID, text string) error {
	return s.sendPlainCard(ctx, larkim.ReceiveIdTypeOpenId, openID, text)
}

func (s *Sender) sendPlainCard(ctx context.Context, idType, id, text string) error {
	req := larkim.NewCreateMessageReqBuilder().
		ReceiveIdType(idType).
		Body(larkim.NewCreateMessageReqBodyBuilder().
			ReceiveId(id).
			MsgType(larkim.MsgTypeInteractive).
			Content(string(PlainTextCardJSON(text))).
			Build()).
		Build()
	resp, err := s.client.Im.Message.Create(ctx, req)
	if err != nil {
		return fmt.Errorf("lark create message: %w", err)
	}
	if !resp.Success() {
		return fmt.Errorf("lark create message: code=%d msg=%s", resp.Code, resp.Msg)
	}
	return nil
}
