package lark

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
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
	Sender     *Sender
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
	sender *Sender,
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

	// Gate 3: command interception (FR-020, FR-021).
	if IsCommand(ev.Text) {
		return h.handleCommand(ctx, ev)
	}

	// Gate 4: acquire per-user lock (FR-012).
	key := SessionKey(ev)
	token, ok, err := h.Locker.TryAcquire(ctx, key, LockTTL)
	if err != nil {
		logger.Error("lark: lock error", "err", err.Error())
		_ = h.Sender.SendPlainCard(ctx, ev.ChatID, "⚠️ 服务暂时不可用，请稍后重试。")
		return err
	}
	if !ok {
		logger.Info("lark: concurrent message rejected (lock held)")
		_ = h.Sender.SendPlainCard(ctx, ev.ChatID, "⏳ 上一条还在处理中，请稍候再发。")
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
		_ = h.Sender.SendPlainCard(ctx, ev.ChatID, "⚠️ 服务暂时不可用，请稍后重试。")
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
		_ = h.Sender.SendPlainCard(ctx, ev.ChatID, "⚠️ 当前没有可用的 AI 模型，请联系管理员。")
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
		// Non-blocking best-effort; dropped events fall back to the ticker.
		select {
		case events <- e:
		default:
			// Buffer overflow: drop non-terminal events. Terminals must not
			// be dropped, so block briefly for those.
			if e.Type == llm.EventMessageEnd || e.Type == llm.EventError {
				events <- e
			}
		}
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
		r.State().ErrorText = userMsg
		r.State().MarkDone()
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

// handleCommand handles /clear, /help, /model, /tools, /whoami. No LLM calls,
// no session history mutation (the commands either wipe state or just read).
func (h *Handler) handleCommand(ctx context.Context, ev *MessageEvent) error {
	cmd := strings.TrimSpace(ev.Text)
	logger := log.FromContext(ctx, h.Logger)

	switch {
	case cmd == "/clear":
		if err := h.Store.Delete(ctx, SessionKey(ev)); err != nil {
			logger.Warn("command /clear delete failed", "err", err.Error())
			return h.Sender.SendPlainCard(ctx, ev.ChatID, "⚠️ 清空失败，请稍后重试。")
		}
		return h.Sender.SendPlainCard(ctx, ev.ChatID, "✅ 已清空上下文。")

	case cmd == "/help":
		return h.Sender.SendPlainCard(ctx, ev.ChatID, helpText(h.Registry, h.LLMs))

	case strings.HasPrefix(cmd, "/model"):
		arg := strings.TrimSpace(strings.TrimPrefix(cmd, "/model"))
		if arg == "" {
			return h.Sender.SendPlainCard(ctx, ev.ChatID, modelListText(h.LLMs))
		}
		if _, ok := h.LLMs.Get(arg); !ok {
			msg := fmt.Sprintf("⚠️ 未知模型 `%s`。\n\n%s", arg, modelListText(h.LLMs))
			return h.Sender.SendPlainCard(ctx, ev.ChatID, msg)
		}
		sess, _ := h.Store.Get(ctx, SessionKey(ev))
		if sess == nil {
			sess = &session.Session{
				Key:        SessionKey(ev),
				UserOpenID: ev.SenderOpenID,
				ChatID:     ev.ChatID,
				ChatType:   ev.ChatType,
			}
		}
		sess.Provider = arg
		if err := h.Store.Save(ctx, sess.Key, sess); err != nil {
			return err
		}
		return h.Sender.SendPlainCard(ctx, ev.ChatID, fmt.Sprintf("✅ 已切换模型为 `%s`。", arg))

	case cmd == "/tools":
		return h.Sender.SendPlainCard(ctx, ev.ChatID, toolsListText(h.Registry))

	case cmd == "/whoami":
		msg := fmt.Sprintf("session_key: `%s`\nopen_id: `%s`\nchat_type: `%s`",
			SessionKey(ev), ev.SenderOpenID, ev.ChatType)
		return h.Sender.SendPlainCard(ctx, ev.ChatID, msg)

	default:
		return h.Sender.SendPlainCard(ctx, ev.ChatID,
			fmt.Sprintf("未知命令 `%s`。输入 `/help` 查看可用命令。", cmd))
	}
}

func helpText(reg *tool.Registry, llms *llm.Registry) string {
	var sb strings.Builder
	sb.WriteString("**可用指令**\n")
	sb.WriteString("- `/clear` 清空当前对话上下文\n")
	sb.WriteString("- `/help` 显示此帮助\n")
	sb.WriteString("- `/model <别名>` 切换 AI 模型\n")
	sb.WriteString("- `/tools` 列出可用工具\n")
	sb.WriteString("- `/whoami` 显示当前会话标识（调试用）\n\n")
	sb.WriteString(modelListText(llms))
	sb.WriteString("\n\n")
	sb.WriteString(toolsListText(reg))
	return sb.String()
}

func modelListText(llms *llm.Registry) string {
	names := llms.Names()
	if len(names) == 0 {
		return "**可用模型**：_无_"
	}
	return "**可用模型**：" + "`" + strings.Join(names, "`、`") + "`"
}

func toolsListText(reg *tool.Registry) string {
	tools := reg.List()
	if len(tools) == 0 {
		return "**可用工具**：_无_"
	}
	var sb strings.Builder
	sb.WriteString("**可用工具**\n")
	for _, t := range tools {
		mark := "✅"
		if !t.Available() {
			mark = "⚠️"
		}
		fmt.Fprintf(&sb, "- %s `%s` (%s)\n", mark, t.Name(), t.Source())
	}
	return sb.String()
}

func userFacingRunError(err error) string {
	if errors.Is(err, agent.ErrMaxStepsReached) {
		return "已达到推理步数上限（12 步），请拆解问题后重试。"
	}
	// Default user-facing wording; internal details stay in logs.
	return "处理过程中出错了，请稍后重试。"
}
