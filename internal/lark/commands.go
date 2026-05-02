package lark

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/anomalyco/ssh-bot/internal/llm"
	"github.com/anomalyco/ssh-bot/internal/log"
	"github.com/anomalyco/ssh-bot/internal/session"
	"github.com/anomalyco/ssh-bot/internal/tool"
)

// CommandResult is the structured output of a short-circuit command.
type CommandResult struct {
	Text string
}

// dispatchCommand handles /clear, /help, /model, /tools, /whoami.
// It never calls the LLM and is invoked before lock acquisition.
func (h *Handler) dispatchCommand(ctx context.Context, ev *MessageEvent) (*CommandResult, bool) {
	cmd := strings.TrimSpace(ev.Text)
	if !IsCommand(cmd) {
		return nil, false
	}
	logger := log.FromContext(ctx, h.Logger)

	switch {
	case cmd == "/clear":
		if err := h.Store.Delete(ctx, SessionKey(ev)); err != nil {
			logger.Warn("command /clear delete failed", "err", err.Error())
			return &CommandResult{Text: "⚠️ 清空失败，请稍后重试。"}, true
		}
		return &CommandResult{Text: "✅ 已清空上下文。"}, true

	case cmd == "/help":
		return &CommandResult{Text: helpText(h.Registry, h.LLMs)}, true

	case strings.HasPrefix(cmd, "/model"):
		arg := strings.TrimSpace(strings.TrimPrefix(cmd, "/model"))
		if arg == "" {
			return &CommandResult{Text: modelListText(h.LLMs)}, true
		}
		if _, ok := h.LLMs.Get(arg); !ok {
			msg := fmt.Sprintf("⚠️ 未知模型 `%s`。\n\n%s", arg, modelListText(h.LLMs))
			return &CommandResult{Text: msg}, true
		}
		sess, err := h.Store.Get(ctx, SessionKey(ev))
		if err != nil {
			logger.Warn("command /model get failed", "err", err.Error())
			return &CommandResult{Text: "⚠️ 模型切换失败，请稍后重试。"}, true
		}
		if sess == nil {
			sess = &session.Session{
				Key:        SessionKey(ev),
				UserOpenID: ev.SenderOpenID,
				ChatID:     ev.ChatID,
				ChatType:   ev.ChatType,
				CreatedAt:  time.Now().UTC(),
			}
		}
		sess.Provider = arg
		if err := h.Store.Save(ctx, sess.Key, sess); err != nil {
			logger.Warn("command /model save failed", "err", err.Error())
			return &CommandResult{Text: "⚠️ 模型切换失败，请稍后重试。"}, true
		}
		return &CommandResult{Text: fmt.Sprintf("✅ 已切换模型为 `%s`。", arg)}, true

	case cmd == "/tools":
		return &CommandResult{Text: toolsListText(h.Registry)}, true

	case cmd == "/whoami":
		msg := fmt.Sprintf("session_key: `%s`\nopen_id: `%s`\nchat_type: `%s`",
			SessionKey(ev), ev.SenderOpenID, ev.ChatType)
		return &CommandResult{Text: msg}, true

	default:
		return &CommandResult{Text: fmt.Sprintf("未知命令 `%s`。输入 `/help` 查看可用命令。", cmd)}, true
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
	return "**可用模型**：`" + strings.Join(names, "`、`") + "`"
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
