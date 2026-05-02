// Package render converts agent StreamEvents into Feishu card JSON, batching
// updates behind a 250ms ticker (D4). See contracts/feishu-card.md.
package render

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/anomalyco/ssh-bot/internal/llm"
)

// Phase is the current top-level state of the card.
type Phase int

const (
	PhaseIdle Phase = iota
	PhaseThinking
	PhaseText
	PhaseToolExecuting
	PhaseDone
	PhaseError
)

// ToolEntry is one row in the card's 🔧 tool-call list.
type ToolEntry struct {
	ID        string
	Name      string
	ArgsPrev  string // truncated args preview
	StartedAt time.Time
	EndedAt   time.Time
	Success   bool
	ErrorMsg  string
}

func (t ToolEntry) Status() string {
	if t.EndedAt.IsZero() {
		return "⏳"
	}
	dur := t.EndedAt.Sub(t.StartedAt)
	if t.Success {
		return fmt.Sprintf("✅ %.1fs", dur.Seconds())
	}
	return "❌ " + truncate(t.ErrorMsg, 60)
}

// State is the renderer's three-phase state machine plus accumulators.
// All mutation is guarded by the outer Renderer's mutex.
type State struct {
	Phase         Phase
	Thinking      strings.Builder // live thinking text
	ThinkingDone  bool            // set when first text delta arrives
	ThinkingStart time.Time
	Text          strings.Builder
	Tools         []*ToolEntry
	toolByID      map[string]*ToolEntry
	StartedAt     time.Time

	// Error message when PhaseError is entered; preserves prior text.
	ErrorText string

	TraceID string
	Model   string
}

// NewState returns a fresh, empty state in PhaseIdle.
func NewState() *State {
	return &State{
		Phase:     PhaseIdle,
		toolByID:  map[string]*ToolEntry{},
		StartedAt: time.Now(),
	}
}

// Apply updates state based on one StreamEvent. Returns true if there was a
// visible change (renderer uses this as the "dirty" flag before flushing).
func (s *State) Apply(ev llm.StreamEvent) bool {
	switch ev.Type {
	case llm.EventThinkingDelta:
		if s.Phase == PhaseIdle {
			s.Phase = PhaseThinking
			s.ThinkingStart = time.Now()
		}
		if !s.ThinkingDone {
			s.Thinking.WriteString(ev.Text)
			return true
		}
		return false
	case llm.EventTextDelta:
		// Transition: collapse thinking at first text (FR-032).
		if !s.ThinkingDone {
			s.ThinkingDone = true
		}
		if s.Phase == PhaseIdle || s.Phase == PhaseThinking {
			s.Phase = PhaseText
		}
		s.Text.WriteString(ev.Text)
		return true
	case llm.EventToolCallStart:
		s.Phase = PhaseToolExecuting
		if _, dup := s.toolByID[ev.ToolCallID]; dup {
			return false
		}
		entry := &ToolEntry{
			ID:        ev.ToolCallID,
			Name:      ev.ToolName,
			ArgsPrev:  truncate(ev.Text, 60),
			StartedAt: time.Now(),
		}
		s.toolByID[ev.ToolCallID] = entry
		s.Tools = append(s.Tools, entry)
		return true
	case llm.EventToolCallEnd:
		entry, ok := s.toolByID[ev.ToolCallID]
		if !ok {
			return false
		}
		entry.EndedAt = time.Now()
		entry.Success = !strings.HasPrefix(ev.Text, "error:")
		if !entry.Success {
			entry.ErrorMsg = strings.TrimPrefix(ev.Text, "error:")
			entry.ErrorMsg = strings.TrimSpace(entry.ErrorMsg)
		}
		return true
	case llm.EventMessageEnd:
		// If the turn contained tool calls, the agent will loop; otherwise
		// we're done. The renderer marks Done only on its caller's Stop().
		return false
	case llm.EventError:
		s.Phase = PhaseError
		if ev.Err != nil {
			s.ErrorText = ev.Err.Error()
		} else {
			s.ErrorText = "unknown error"
		}
		return true
	}
	return false
}

// MarkDone flips to the terminal state used by final flush.
func (s *State) MarkDone() {
	if s.Phase == PhaseError {
		return
	}
	s.Phase = PhaseDone
}

// Snapshot returns a deep-copied snapshot suitable for non-blocking render.
// Only fields used by render.go#buildCardJSON are copied.
func (s *State) Snapshot() *StateSnapshot {
	cp := &StateSnapshot{
		Phase:        s.Phase,
		Thinking:     s.Thinking.String(),
		ThinkingDone: s.ThinkingDone,
		Text:         s.Text.String(),
		ErrorText:    s.ErrorText,
		TraceID:      s.TraceID,
		Model:        s.Model,
	}
	cp.Tools = make([]ToolEntry, len(s.Tools))
	for i, t := range s.Tools {
		cp.Tools[i] = *t
	}
	return cp
}

// StateSnapshot is an immutable view used by render.go.
type StateSnapshot struct {
	Phase        Phase
	Thinking     string
	ThinkingDone bool
	Text         string
	Tools        []ToolEntry
	ErrorText    string
	TraceID      string
	Model        string
}

// ----------------------------------------------------------------------------
// Card JSON serialization (contracts/feishu-card.md).
// ----------------------------------------------------------------------------

// MaxCardJSONBytes is the Feishu hard cap we enforce before sending.
// The realistic body cap is 30 KB; we leave a 5 KB envelope/safety margin.
const MaxCardJSONBytes = 25 * 1024

// BuildCardJSON returns the full Feishu card body JSON for the given state.
func BuildCardJSON(s *StateSnapshot) ([]byte, error) {
	card := map[string]any{
		"config": map[string]any{
			"update_multi":     true,
			"wide_screen_mode": true,
		},
		"header": map[string]any{
			"template": templateForPhase(s.Phase),
			"title": map[string]any{
				"tag":     "plain_text",
				"content": headerForPhase(s.Phase),
			},
		},
	}

	elements := make([]any, 0, 8)

	if s.Phase == PhaseError {
		// Terminal error card per contract.
		elements = append(elements, map[string]any{
			"tag": "div",
			"text": map[string]any{
				"tag":     "lark_md",
				"content": fmt.Sprintf("❌ **%s**\n\n如问题持续，请联系管理员。\n```\ntrace: %s\n```", userFacingError(s.ErrorText), s.TraceID),
			},
		})
	} else {
		// Thinking region (only while still reasoning).
		if s.Thinking != "" && !s.ThinkingDone {
			elements = append(elements, map[string]any{
				"tag": "div",
				"text": map[string]any{
					"tag":     "lark_md",
					"content": fmt.Sprintf("💭 **思考中...**\n> %s", truncate(s.Thinking, 300)),
				},
			})
			elements = append(elements, map[string]any{"tag": "hr"})
		} else if s.Thinking != "" && s.ThinkingDone {
			// Collapsed "thinking complete" note (small, non-intrusive).
			elements = append(elements, map[string]any{
				"tag": "note",
				"elements": []any{
					map[string]any{
						"tag":     "plain_text",
						"content": "💭 思考完成",
					},
				},
			})
		}

		// Text region.
		if s.Text != "" {
			elements = append(elements, map[string]any{
				"tag": "div",
				"text": map[string]any{
					"tag":     "lark_md",
					"content": s.Text,
				},
			})
		} else if s.Phase == PhaseIdle {
			elements = append(elements, map[string]any{
				"tag": "div",
				"text": map[string]any{
					"tag":     "lark_md",
					"content": "💭 初始化中…",
				},
			})
		}

		// Tool-call region.
		if len(s.Tools) > 0 {
			elements = append(elements, map[string]any{"tag": "hr"})
			var sb strings.Builder
			sb.WriteString("🔧 **工具调用**\n")
			for _, t := range s.Tools {
				fmt.Fprintf(&sb, "- `%s(%s)` %s\n", t.Name, t.ArgsPrev, t.Status())
			}
			elements = append(elements, map[string]any{
				"tag": "div",
				"text": map[string]any{
					"tag":     "lark_md",
					"content": sb.String(),
				},
			})
		}

		// Footer on terminal phases.
		if s.Phase == PhaseDone && (s.TraceID != "" || s.Model != "") {
			elements = append(elements, map[string]any{"tag": "hr"})
			var parts []string
			if s.TraceID != "" {
				parts = append(parts, "trace: "+s.TraceID)
			}
			if s.Model != "" {
				parts = append(parts, "model: "+s.Model)
			}
			elements = append(elements, map[string]any{
				"tag": "note",
				"elements": []any{
					map[string]any{
						"tag":     "plain_text",
						"content": strings.Join(parts, " · "),
					},
				},
			})
		}
	}

	card["elements"] = elements

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(card); err != nil {
		return nil, err
	}
	raw := bytes.TrimRight(buf.Bytes(), "\n")
	if len(raw) > MaxCardJSONBytes {
		// Truncate Text field and retry once.
		if snap := trimTextForSize(s); snap != nil {
			return BuildCardJSON(snap)
		}
	}
	return raw, nil
}

func headerForPhase(p Phase) string {
	if p == PhaseError {
		return "AI 助手（遇到问题）"
	}
	return "AI 助手"
}

func templateForPhase(p Phase) string {
	if p == PhaseError {
		return "red"
	}
	return "blue"
}

func userFacingError(raw string) string {
	// The caller MUST keep internal details out; but as a belt-and-braces
	// fallback: if this slipped in we truncate heavily.
	return truncate(raw, 120)
}

// trimTextForSize returns a new snapshot with the Text body truncated so that
// the final card fits within MaxCardJSONBytes. Returns nil if no safe
// truncation is possible (implausible in practice).
func trimTextForSize(s *StateSnapshot) *StateSnapshot {
	if s.Text == "" {
		return nil
	}
	cp := *s
	// Hard cut: keep the first 20000 bytes and suffix a marker.
	const keep = 20_000
	if len(cp.Text) > keep {
		cp.Text = cp.Text[:keep] + "\n\n*[本卡片过长，已截断；完整内容通过线程回复继续]*"
		return &cp
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// ----------------------------------------------------------------------------
// Guard against bogus use of sync.Mutex outside this file.
// ----------------------------------------------------------------------------

var _ sync.Locker = (*sync.Mutex)(nil)
