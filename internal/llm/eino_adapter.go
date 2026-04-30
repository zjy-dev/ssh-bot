package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/eino-contrib/jsonschema"
)

// EinoAdapter wraps an eino BaseChatModel (or ToolCallingChatModel) and
// implements Provider by translating eino's schema.Message stream chunks into
// our unified StreamEvent enum.
//
// It satisfies the contracts listed in contracts/go-interfaces.md#internal-llm,
// in particular the ordering invariant between thinking / text / tool deltas.
type EinoAdapter struct {
	name           string
	model          model.BaseChatModel
	enableThinking bool
}

// NewEinoAdapter constructs an EinoAdapter. name is the provider alias.
func NewEinoAdapter(name string, m model.BaseChatModel, enableThinking bool) *EinoAdapter {
	return &EinoAdapter{name: name, model: m, enableThinking: enableThinking}
}

// Name implements Provider.
func (a *EinoAdapter) Name() string { return a.name }

// Stream implements Provider. It blocks on model.Stream, then kicks off a
// goroutine that translates each schema.Message chunk into one or more
// StreamEvents, closing the output channel after exactly one MessageEnd/Error.
func (a *EinoAdapter) Stream(ctx context.Context, req ChatRequest) (<-chan StreamEvent, error) {
	messages, err := toEinoMessages(req)
	if err != nil {
		return nil, fmt.Errorf("convert messages: %w", err)
	}

	opts := []model.Option{}
	if req.Model != "" {
		opts = append(opts, model.WithModel(req.Model))
	}
	if req.Temperature != nil {
		opts = append(opts, model.WithTemperature(*req.Temperature))
	}
	if req.MaxTokens > 0 {
		opts = append(opts, model.WithMaxTokens(req.MaxTokens))
	}
	if len(req.Tools) > 0 {
		toolInfos, terr := toEinoTools(req.Tools)
		if terr != nil {
			return nil, fmt.Errorf("convert tools: %w", terr)
		}
		opts = append(opts, model.WithTools(toolInfos))
	}

	reader, err := a.model.Stream(ctx, messages, opts...)
	if err != nil {
		return nil, fmt.Errorf("eino stream: %w", err)
	}

	out := make(chan StreamEvent, 16)
	go translateStream(reader, out)
	return out, nil
}

// translateStream reads every schema.Message chunk from reader, translates it,
// and writes to out. On io.EOF it emits EventMessageEnd; on any other error it
// emits EventError. Exactly one terminator is emitted; out is closed afterward.
func translateStream(reader *schema.StreamReader[*schema.Message], out chan<- StreamEvent) {
	defer close(out)
	defer reader.Close()

	// Track which tool-call IDs we've already emitted a Start for, so we can
	// turn subsequent chunks for the same ID into Args deltas.
	seenTool := make(map[string]bool)
	// Tool call args accumulator keyed by index (streaming often uses Index not ID).
	type partialTC struct {
		id       string
		name     string
		argsSeen string
	}
	byIndex := make(map[int]*partialTC)

	var (
		finalUsage  Usage
		finalReason string
	)

	for {
		chunk, err := reader.Recv()
		if errors.Is(err, io.EOF) {
			// End-of-stream tool closures
			for _, tc := range byIndex {
				out <- StreamEvent{Type: EventToolCallEnd, ToolCallID: tc.id, ToolName: tc.name}
			}
			out <- StreamEvent{Type: EventMessageEnd, Usage: finalUsage, StopReason: finalReason}
			return
		}
		if err != nil {
			out <- StreamEvent{Type: EventError, Err: err}
			return
		}
		if chunk == nil {
			continue
		}

		if chunk.ReasoningContent != "" {
			out <- StreamEvent{Type: EventThinkingDelta, Text: chunk.ReasoningContent}
		}
		if chunk.Content != "" {
			out <- StreamEvent{Type: EventTextDelta, Text: chunk.Content}
		}
		for _, tc := range chunk.ToolCalls {
			idx := 0
			if tc.Index != nil {
				idx = *tc.Index
			}
			cur, ok := byIndex[idx]
			if !ok {
				cur = &partialTC{}
				byIndex[idx] = cur
			}
			// Populate ID/Name once we see them.
			if tc.ID != "" && cur.id == "" {
				cur.id = tc.ID
			}
			if tc.Function.Name != "" && cur.name == "" {
				cur.name = tc.Function.Name
			}
			// Fire Start on first time we have both id + name.
			if cur.id != "" && cur.name != "" && !seenTool[cur.id] {
				seenTool[cur.id] = true
				out <- StreamEvent{
					Type:       EventToolCallStart,
					ToolCallID: cur.id,
					ToolName:   cur.name,
				}
			}
			if tc.Function.Arguments != "" {
				cur.argsSeen += tc.Function.Arguments
				if cur.id != "" {
					out <- StreamEvent{
						Type:       EventToolCallArgs,
						ToolCallID: cur.id,
						ToolName:   cur.name,
						ArgsDelta:  tc.Function.Arguments,
					}
				}
			}
		}

		if chunk.ResponseMeta != nil {
			if chunk.ResponseMeta.Usage != nil {
				finalUsage = Usage{
					InputTokens:  chunk.ResponseMeta.Usage.PromptTokens,
					OutputTokens: chunk.ResponseMeta.Usage.CompletionTokens,
				}
			}
			if chunk.ResponseMeta.FinishReason != "" {
				finalReason = chunk.ResponseMeta.FinishReason
			}
		}
	}
}

// toEinoMessages converts our Message shape into eino's schema.Message slice.
// A non-empty System string is prepended as a System message.
func toEinoMessages(req ChatRequest) ([]*schema.Message, error) {
	out := make([]*schema.Message, 0, len(req.Messages)+1)
	if strings.TrimSpace(req.System) != "" {
		out = append(out, &schema.Message{Role: schema.System, Content: req.System})
	}
	for _, m := range req.Messages {
		em := &schema.Message{
			Role:    roleToEino(m.Role),
			Content: m.Content,
			Name:    m.Name,
		}
		if m.Thinking != "" && m.Role == RoleAssistant {
			em.ReasoningContent = m.Thinking
		}
		if m.ToolCallID != "" {
			em.ToolCallID = m.ToolCallID
		}
		if len(m.ToolCalls) > 0 {
			em.ToolCalls = make([]schema.ToolCall, 0, len(m.ToolCalls))
			for _, tc := range m.ToolCalls {
				em.ToolCalls = append(em.ToolCalls, schema.ToolCall{
					ID:   tc.ID,
					Type: "function",
					Function: schema.FunctionCall{
						Name:      tc.Name,
						Arguments: string(tc.Arguments),
					},
				})
			}
		}
		out = append(out, em)
	}
	return out, nil
}

func roleToEino(r Role) schema.RoleType {
	switch r {
	case RoleUser:
		return schema.User
	case RoleAssistant:
		return schema.Assistant
	case RoleTool:
		return schema.Tool
	default:
		return schema.User
	}
}

// toEinoTools converts our []ToolSpec into eino's []*schema.ToolInfo.
// Each ToolSpec's InputSchema is JSON-decoded into a *jsonschema.Schema.
func toEinoTools(tools []ToolSpec) ([]*schema.ToolInfo, error) {
	out := make([]*schema.ToolInfo, 0, len(tools))
	for _, t := range tools {
		info := &schema.ToolInfo{Name: t.Name, Desc: t.Description}
		if len(t.InputSchema) > 0 {
			var js jsonschema.Schema
			if err := json.Unmarshal(t.InputSchema, &js); err != nil {
				return nil, fmt.Errorf("tool %q: parse input schema: %w", t.Name, err)
			}
			info.ParamsOneOf = schema.NewParamsOneOfByJSONSchema(&js)
		}
		out = append(out, info)
	}
	return out, nil
}

// ----------------------------------------------------------------------------
// Fake provider for tests (T024 contract test uses it).
// ----------------------------------------------------------------------------

// FakeProvider is a Provider whose behavior is scripted via a slice of events.
// Useful for the agent loop and renderer contract tests. Safe for concurrent
// calls to Stream; scripts are replayed in order across calls.
type FakeProvider struct {
	name   string
	mu     sync.Mutex
	script [][]StreamEvent // one sub-slice per Stream() call
}

// NewFakeProvider constructs a FakeProvider that, on successive Stream calls,
// emits script[0], then script[1], and so on. Each sub-slice should terminate
// with EventMessageEnd or EventError.
func NewFakeProvider(name string, script [][]StreamEvent) *FakeProvider {
	return &FakeProvider{name: name, script: script}
}

// Name implements Provider.
func (f *FakeProvider) Name() string { return f.name }

// Stream implements Provider.
func (f *FakeProvider) Stream(ctx context.Context, _ ChatRequest) (<-chan StreamEvent, error) {
	f.mu.Lock()
	if len(f.script) == 0 {
		f.mu.Unlock()
		return nil, fmt.Errorf("fake provider: script exhausted")
	}
	evs := f.script[0]
	f.script = f.script[1:]
	f.mu.Unlock()

	ch := make(chan StreamEvent, len(evs))
	go func() {
		defer close(ch)
		for _, ev := range evs {
			select {
			case <-ctx.Done():
				ch <- StreamEvent{Type: EventError, Err: ctx.Err()}
				return
			case ch <- ev:
			}
		}
	}()
	return ch, nil
}

// Compile-time check.
var _ Provider = (*FakeProvider)(nil)
var _ Provider = (*EinoAdapter)(nil)
