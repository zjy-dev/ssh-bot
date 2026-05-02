package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/anomalyco/ssh-bot/internal/llm"
	"github.com/anomalyco/ssh-bot/internal/session"
	"github.com/anomalyco/ssh-bot/internal/tool"
)

// Defaults per plan §4 and research.md D9.
const (
	DefaultMaxSteps    = 12
	DefaultToolTimeout = 30 * time.Second
	MaxArgumentsLen    = 100 * 1024 // reject malformed stream that balloons args
)

// ErrMaxStepsReached is returned by Run when the agent fails to terminate
// within MaxSteps. The renderer surfaces this to the user as
// "已达到推理步数上限（12 步），请拆解问题后重试".
var ErrMaxStepsReached = errors.New("agent: reached max steps")

// ErrEmptyAssistantResponse is returned when the provider ends a turn without
// any visible assistant text and without requesting any tools.
var ErrEmptyAssistantResponse = errors.New("agent: empty assistant response")

// EmitFn is the event sink passed by the caller (typically the renderer).
//
// The loop forwards every provider StreamEvent and ALSO synthesizes events
// for tool lifecycle (see contracts/go-interfaces.md#internal-agent):
//   - After accumulating a tool call's args fully, emits EventToolCallStart
//     with Text = args JSON preview.
//   - After a tool finishes, emits EventToolCallEnd with Text = "success" or
//     "error: <msg>" and StopReason = "<elapsed ms>".
type EmitFn func(ev llm.StreamEvent)

// Agent drives the loop.
type Agent struct {
	Provider    llm.Provider
	Registry    *tool.Registry
	Store       session.Store
	MaxSteps    int
	ToolTimeout time.Duration
	Preamble    string
	Thinking    bool
}

// NewAgent returns a ready-to-use Agent with sensible defaults.
func NewAgent(p llm.Provider, reg *tool.Registry, store session.Store) *Agent {
	return &Agent{
		Provider:    p,
		Registry:    reg,
		Store:       store,
		MaxSteps:    DefaultMaxSteps,
		ToolTimeout: DefaultToolTimeout,
	}
}

// Run drives the loop for one user turn. It:
//   - appends the user input to sess.Messages,
//   - asks the provider for a streaming response,
//   - forwards provider events to emit AND synthesizes tool-lifecycle events,
//   - on assistant-with-tool-calls: executes tools (concurrently, per-call
//     timeout, panic-guarded), appends tool results, loops,
//   - on assistant-without-tool-calls: returns nil,
//   - persists sess after every assistant + every tool-result append,
//   - returns ErrMaxStepsReached if MaxSteps is hit.
//
// emit may be nil (useful for tests).
func (a *Agent) Run(ctx context.Context, sess *session.Session, userInput string, emit EmitFn) error {
	if emit == nil {
		emit = func(llm.StreamEvent) {}
	}
	if a.MaxSteps <= 0 {
		a.MaxSteps = DefaultMaxSteps
	}
	if a.ToolTimeout <= 0 {
		a.ToolTimeout = DefaultToolTimeout
	}

	sess.Messages = append(sess.Messages, llm.Message{Role: llm.RoleUser, Content: userInput})
	if err := a.saveSession(ctx, sess); err != nil {
		return fmt.Errorf("save session (pre-turn): %w", err)
	}

	for step := 0; step < a.MaxSteps; step++ {
		req := buildRequest(sess, a.Registry, a.Preamble, a.Thinking)
		events, err := a.Provider.Stream(ctx, req)
		if err != nil {
			return fmt.Errorf("provider stream: %w", err)
		}

		asst := llm.Message{Role: llm.RoleAssistant}
		var thinkingBuf, textBuf []byte
		// Track partial tool-call args by id.
		type partialCall struct {
			name string
			args []byte
		}
		partials := map[string]*partialCall{}
		order := []string{} // preserve call order

		for ev := range events {
			switch ev.Type {
			case llm.EventThinkingDelta:
				thinkingBuf = append(thinkingBuf, ev.Text...)
				emit(ev)
			case llm.EventTextDelta:
				textBuf = append(textBuf, ev.Text...)
				emit(ev)
			case llm.EventToolCallStart:
				if _, ok := partials[ev.ToolCallID]; !ok {
					partials[ev.ToolCallID] = &partialCall{name: ev.ToolName}
					order = append(order, ev.ToolCallID)
				}
				// Intentionally not forwarded yet; we emit our synthetic Start
				// only after args are fully accumulated (see below).
			case llm.EventToolCallArgs:
				pc, ok := partials[ev.ToolCallID]
				if !ok {
					pc = &partialCall{name: ev.ToolName}
					partials[ev.ToolCallID] = pc
					order = append(order, ev.ToolCallID)
				}
				if len(pc.args)+len(ev.ArgsDelta) > MaxArgumentsLen {
					// Hostile or malformed stream; abort this turn cleanly.
					emit(llm.StreamEvent{Type: llm.EventError, Err: fmt.Errorf("tool arguments too large")})
					return fmt.Errorf("tool arguments exceeded cap")
				}
				pc.args = append(pc.args, ev.ArgsDelta...)
			case llm.EventToolCallEnd:
				// Ignore provider end; our synthetic end after execution is
				// what the renderer actually consumes.
			case llm.EventMessageEnd:
				asst.Thinking = string(thinkingBuf)
				asst.Content = string(textBuf)
				for _, id := range order {
					pc := partials[id]
					asst.ToolCalls = append(asst.ToolCalls, llm.ToolCall{
						ID:        id,
						Name:      pc.name,
						Arguments: json.RawMessage(pc.args),
					})
				}
				if len(asst.ToolCalls) == 0 && strings.TrimSpace(asst.Content) == "" {
					emptyErr := ErrEmptyAssistantResponse
					emit(llm.StreamEvent{Type: llm.EventError, Err: emptyErr})
					return emptyErr
				}
				sess.Messages = append(sess.Messages, asst)
				if err := a.saveSession(ctx, sess); err != nil {
					return fmt.Errorf("save session (assistant): %w", err)
				}
				emit(ev)

				if len(asst.ToolCalls) == 0 {
					return nil
				}

				// Emit synthetic Start for each now-complete call so the
				// renderer can show them with a best-effort arg preview.
				for _, tc := range asst.ToolCalls {
					emit(llm.StreamEvent{
						Type:       llm.EventToolCallStart,
						ToolCallID: tc.ID,
						ToolName:   tc.Name,
						Text:       previewArgs(tc.Arguments),
					})
				}

				// Execute tools concurrently.
				results := a.execTools(ctx, asst.ToolCalls, emit)

				// Feed results back into the session, in original call order
				// so the model sees a deterministic transcript.
				for _, r := range results {
					sess.Messages = append(sess.Messages, llm.Message{
						Role:       llm.RoleTool,
						Content:    r.Content,
						ToolCallID: r.ID,
						Name:       r.Name,
						IsError:    r.IsError,
					})
				}
				if err := a.saveSession(ctx, sess); err != nil {
					return fmt.Errorf("save session (tools): %w", err)
				}
				// Go back to top of outer for — next step calls Stream again.
			case llm.EventError:
				// Forward and bail.
				emit(ev)
				return ev.Err
			}
		}
		// If we fell out of the inner range without seeing MessageEnd, something
		// went wrong with the stream; treat as error.
		if asst.Role == "" || (asst.Content == "" && asst.Thinking == "" && len(asst.ToolCalls) == 0) {
			return fmt.Errorf("provider stream ended without message_end")
		}
	}
	return ErrMaxStepsReached
}

// toolResult is an internal aggregate: the fields the agent needs to append
// a "tool" role message back into the conversation.
type toolResult struct {
	ID      string
	Name    string
	Content string
	IsError bool
}

// execTools runs each tool call concurrently, each with its own timeout +
// recover. Results are returned in the original order of calls.
func (a *Agent) execTools(parent context.Context, calls []llm.ToolCall, emit EmitFn) []toolResult {
	out := make([]toolResult, len(calls))
	var wg sync.WaitGroup
	wg.Add(len(calls))
	for i, c := range calls {
		i, c := i, c
		go func() {
			defer wg.Done()
			start := time.Now()
			out[i] = a.execOne(parent, c)
			elapsed := time.Since(start)
			// Synthetic End event for renderer.
			endText := "success"
			if out[i].IsError {
				endText = "error: " + truncate(out[i].Content, 80)
			}
			emit(llm.StreamEvent{
				Type:       llm.EventToolCallEnd,
				ToolCallID: c.ID,
				ToolName:   c.Name,
				Text:       endText,
				StopReason: fmt.Sprintf("%d", elapsed.Milliseconds()),
			})
		}()
	}
	wg.Wait()
	return out
}

// execOne runs a single tool with a fresh timeout-scoped context, catches
// panics, translates errors into tool-result content (FR-042).
func (a *Agent) execOne(parent context.Context, c llm.ToolCall) (res toolResult) {
	res.ID = c.ID
	res.Name = c.Name

	t, ok := a.Registry.Get(c.Name)
	if !ok {
		res.IsError = true
		res.Content = fmt.Sprintf("unknown tool %q", c.Name)
		return
	}

	// Validate arguments are JSON (contracts/tools.md#validation-flow).
	if len(c.Arguments) > 0 {
		var tmp any
		if err := json.Unmarshal(c.Arguments, &tmp); err != nil {
			res.IsError = true
			res.Content = "invalid JSON arguments from model: " + err.Error()
			return
		}
	}

	ctx, cancel := context.WithTimeout(parent, a.ToolTimeout)
	defer cancel()

	defer func() {
		if rec := recover(); rec != nil {
			res.IsError = true
			res.Content = fmt.Sprintf("internal error: %v", rec)
			// Log stacktrace via debug; the caller handles logging.
			_ = debug.Stack()
		}
	}()

	r, err := t.Call(ctx, c.Arguments)
	if err != nil {
		res.IsError = true
		if uerr, ok := err.(tool.UserError); ok {
			res.Content = uerr.UserMessage()
		} else {
			res.Content = "tool error: " + err.Error()
		}
		return
	}
	res.Content = r.Content
	return
}

func (a *Agent) saveSession(ctx context.Context, sess *session.Session) error {
	if a.Store == nil {
		return nil
	}
	return a.Store.Save(ctx, sess.Key, sess)
}

func previewArgs(raw json.RawMessage) string {
	s := string(raw)
	if len(s) > 200 {
		return s[:200] + "…"
	}
	return s
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
