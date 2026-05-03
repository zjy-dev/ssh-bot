package lark

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/anomalyco/ssh-bot/internal/agent"
	"github.com/anomalyco/ssh-bot/internal/llm"
	"github.com/anomalyco/ssh-bot/internal/log"
	"github.com/anomalyco/ssh-bot/internal/render"
	"github.com/anomalyco/ssh-bot/internal/session"
	"github.com/anomalyco/ssh-bot/internal/tool"
)

// LockTTL is the TTL on the per-user lock while a turn is in progress (D6).
const LockTTL = 60 * time.Second

// MessageSender is the subset of the Feishu sender that the handler and
// renderer need. It is intentionally interface-shaped so contract tests can
// drive Handler with a fake sender.
type MessageSender interface {
	SendInitialCard(ctx context.Context, chatID string) (string, error)
	SendMessage(ctx context.Context, chatID, text string) error
	Patch(ctx context.Context, messageID string, cardJSON []byte) error
	ReplyInThread(ctx context.Context, rootMessageID, text string) error
}

type plainCardSender interface {
	SendPlainCard(ctx context.Context, chatID, text string) error
}

// Handler is the entry point for every inbound Feishu message event.
// It:
//   - ignores non-@-messages in groups (FR-001),
//   - intercepts / commands before the agent loop (FR-020, FR-021),
//   - acquires a per-user lock (FR-012),
//   - sends an initial card, drives the agent loop, feeds the renderer,
//   - persists session via the store (FR-010, FR-072).
type Handler struct {
	Store      session.Store
	Locker     session.Locker
	Sender     MessageSender
	Registry   *tool.Registry
	LLMs       *llm.Registry
	BotOpenID  string
	DefaultMdl string
	Logger     *slog.Logger

	// Agent factory: handler builds a fresh agent per turn so that the
	// Provider (which depends on session.Provider override) is resolved
	// at dispatch time and not at startup.
	newAgent func(prov llm.Provider) *agent.Agent
}

// NewHandler wires the handler with its dependencies.
func NewHandler(
	store session.Store,
	locker session.Locker,
	sender MessageSender,
	registry *tool.Registry,
	llms *llm.Registry,
	botOpenID string,
	defaultModel string,
	logger *slog.Logger,
) *Handler {
	h := &Handler{
		Store:      store,
		Locker:     locker,
		Sender:     sender,
		Registry:   registry,
		LLMs:       llms,
		BotOpenID:  botOpenID,
		DefaultMdl: defaultModel,
		Logger:     logger,
	}
	h.newAgent = func(prov llm.Provider) *agent.Agent {
		return agent.NewAgent(prov, registry, store)
	}
	return h
}

// Handle processes one inbound event.
func (h *Handler) Handle(ctx context.Context, ev *MessageEvent) error {
	if ev == nil {
		return errors.New("nil event")
	}
	traceID := log.NewTraceID()
	ctx = log.WithTrace(ctx, traceID)
	ctx = tool.WithCallerOpenID(ctx, ev.SenderOpenID)
	logger := log.FromContext(ctx, h.Logger)

	logger.Info("lark: event received",
		"chat_id", ev.ChatID, "chat_type", ev.ChatType,
		"sender", ev.SenderOpenID, "message_id", ev.MessageID)

	// Gate 1: group messages must @ the bot. DMs bypass this.
	if ev.ChatType != "p2p" && !ev.MentionedBot {
		logger.Debug("lark: group message without @bot; ignoring")
		return nil
	}

	// Gate 2: empty or non-text after stripping.
	if ev.Text == "" {
		logger.Debug("lark: empty or non-text content; ignoring")
		return nil
	}

	// Gate 3: command interception (FR-020, FR-021). Commands execute before
	// lock acquisition and before any session mutation.
	if res, handled := h.dispatchCommand(ctx, ev); handled {
		if res == nil || res.Text == "" {
			return nil
		}
		if sender, ok := h.Sender.(plainCardSender); ok {
			return sender.SendPlainCard(ctx, ev.ChatID, res.Text)
		}
		return h.Sender.SendMessage(ctx, ev.ChatID, res.Text)
	}

	// Gate 4: acquire per-user lock (FR-012).
	key := SessionKey(ev)
	token, ok, err := h.Locker.TryAcquire(ctx, key, LockTTL)
	if err != nil {
		logger.Error("lark: lock error", "err", err.Error())
		_ = h.Sender.SendMessage(ctx, ev.ChatID, "⚠️ 服务暂时不可用，请稍后重试。")
		return err
	}
	if !ok {
		logger.Info("lark: concurrent message rejected (lock held)")
		_ = h.Sender.SendMessage(ctx, ev.ChatID, "⏳ 上一条还在处理中，请稍候再发。")
		return nil
	}
	defer func() {
		if rerr := h.Locker.Release(context.Background(), key, token); rerr != nil {
			logger.Warn("lark: lock release failed", "err", rerr.Error())
		}
	}()

	// Gate 5: load or create session (FR-010, FR-011).
	sess, err := h.Store.Get(ctx, key)
	if err != nil {
		logger.Error("lark: session get", "err", err.Error())
		_ = h.Sender.SendMessage(ctx, ev.ChatID, "⚠️ 服务暂时不可用，请稍后重试。")
		return err
	}
	if sess == nil {
		sess = &session.Session{
			Key:        key,
			UserOpenID: ev.SenderOpenID,
			ChatID:     ev.ChatID,
			ChatType:   ev.ChatType,
			Provider:   h.DefaultMdl,
			CreatedAt:  time.Now().UTC(),
		}
	}
	sess.TraceID = traceID

	// Resolve provider (may be overridden via /model).
	prov, ok := h.LLMs.Resolve(sess.Provider)
	if !ok || prov == nil {
		logger.Error("lark: no provider available", "alias", sess.Provider)
		_ = h.Sender.SendMessage(ctx, ev.ChatID, "⚠️ 当前没有可用的 AI 模型，请联系管理员。")
		return errors.New("no provider available")
	}

	// Send initial card to get a message_id for streaming updates.
	mid, err := h.Sender.SendInitialCard(ctx, ev.ChatID)
	if err != nil {
		logger.Error("lark: initial card send", "err", err.Error())
		return err
	}

	// Build renderer and feed it from the agent's emit callback.
	r := render.New(h.Sender, logger)
	r.State().TraceID = traceID
	r.State().Model = prov.Name()

	// Buffered channel decouples agent from renderer flush cadence.
	events := make(chan llm.StreamEvent, 64)
	emit := func(e llm.StreamEvent) {
		events <- e
	}

	// Feed-goroutine consumes events and issues PATCHes.
	feedDone := make(chan error, 1)
	go func() { feedDone <- r.Feed(ctx, mid, events) }()

	ag := h.newAgent(prov)
	ag.Thinking = true
	runErr := ag.Run(ctx, sess, ev.Text, emit)
	close(events)
	// Wait for renderer to drain.
	if ferr := <-feedDone; ferr != nil && !errors.Is(ferr, context.Canceled) {
		logger.Warn("lark: renderer feed error", "err", ferr.Error())
	}

	// Attach ErrMaxStepsReached as a terminal error card.
	if runErr != nil {
		userMsg := userFacingRunError(runErr)
		// Final flush with error state.
		r.State().MarkError(userMsg)
		_ = r.Stop(ctx, mid)
		// If the agent produced nothing, also drop a plain reply so the user
		// isn't looking at a stale "思考中" card.
		return runErr
	}
	// Normal termination: final flush.
	if err := r.Stop(ctx, mid); err != nil {
		logger.Warn("lark: final card stop", "err", err.Error())
	}

	// Long-body threaded continuation (FR-034).
	_, threaded := r.SplitLongBody()
	for _, chunk := range threaded {
		if err := h.Sender.ReplyInThread(ctx, mid, chunk); err != nil {
			logger.Warn("lark: thread reply failed", "err", err.Error())
			break
		}
	}

	return nil
}

func userFacingRunError(err error) string {
	if errors.Is(err, agent.ErrMaxStepsReached) {
		return "已达到推理步数上限（12 步），请拆解问题后重试。"
	}
	if errors.Is(err, agent.ErrEmptyAssistantResponse) {
		return "模型返回了空响应，请稍后重试。"
	}
	// Default user-facing wording; internal details stay in logs.
	return "处理过程中出错了，请稍后重试。"
}
