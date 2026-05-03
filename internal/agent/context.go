package agent

import (
	"strings"

	"github.com/anomalyco/ssh-bot/internal/llm"
	"github.com/anomalyco/ssh-bot/internal/session"
	"github.com/anomalyco/ssh-bot/internal/tool"
)

// buildRequest assembles the llm.ChatRequest for one loop step.
//
// The system prompt is composed fresh every call from a fixed preamble plus
// the live tool catalog. Nothing is persisted as a "system" message in the
// session history (data-model.md §2 note).
func buildRequest(sess *session.Session, reg *tool.Registry, preamble string, enableThinking bool) llm.ChatRequest {
	system := composeSystem(preamble, reg)
	return llm.ChatRequest{
		System:         system,
		Messages:       append([]llm.Message(nil), sess.Messages...),
		Tools:          specsFromRegistry(reg),
		EnableThinking: enableThinking,
	}
}

// composeSystem combines a fixed preamble with a dynamic tool-catalog note.
// Keep this terse — the model reads this every single turn.
func composeSystem(preamble string, reg *tool.Registry) string {
	var b strings.Builder
	if preamble != "" {
		b.WriteString(preamble)
	} else {
		b.WriteString("You are an AI assistant running inside a Feishu (Lark) chat. Answer in the user's language. Prefer concise, well-formatted markdown. When a tool is appropriate, call it; do not guess time-sensitive facts.")
	}
	b.WriteString("\n\nFormatting constraints for Feishu cards: output only Feishu-compatible markdown (lark_md subset). Do not use markdown headings that start with #; use bold section titles like **结论** instead. Prefer short paragraphs and bullet lists. Avoid HTML tags and markdown tables.")
	avail := reg.Available()
	if len(avail) > 0 {
		b.WriteString("\n\nAvailable tools:\n")
		for _, t := range avail {
			b.WriteString("- ")
			b.WriteString(t.Name())
			b.WriteString(": ")
			b.WriteString(firstLine(t.Description()))
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// specsFromRegistry flattens the registry into the []llm.ToolSpec shape the
// provider accepts. Disabled tools are excluded (FR-062).
func specsFromRegistry(reg *tool.Registry) []llm.ToolSpec {
	tools := reg.Available()
	specs := make([]llm.ToolSpec, 0, len(tools))
	for _, t := range tools {
		specs = append(specs, llm.ToolSpec{
			Name:        t.Name(),
			Description: t.Description(),
			InputSchema: t.InputSchema(),
		})
	}
	return specs
}
