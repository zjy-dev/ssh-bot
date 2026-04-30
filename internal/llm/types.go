// Package llm defines the bot's LLM provider abstraction and unified stream
// event model. See contracts/go-interfaces.md#internal-llm.
//
// This package sits on top of cloudwego/eino's ChatModel abstraction but does
// NOT expose eino types to callers. Callers deal only with the types in this
// file.
package llm

import (
	"context"
	"encoding/json"
)

// Role identifies the author of a Message.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Message is one item in the conversation history.
//
// Fields are populated conditionally based on Role; see contracts for details.
type Message struct {
	Role       Role       `json:"role"`
	Content    string     `json:"content,omitempty"`
	Thinking   string     `json:"thinking,omitempty"`     // assistant only
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`   // assistant only
	ToolCallID string     `json:"tool_call_id,omitempty"` // tool only
	Name       string     `json:"name,omitempty"`         // tool only
	IsError    bool       `json:"is_error,omitempty"`     // tool only
}

// ToolCall is a model-requested tool invocation.
type ToolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// ToolSpec describes a tool exposed to the model. This avoids importing the
// tool package here and thereby prevents an import cycle.
type ToolSpec struct {
	Name        string
	Description string
	InputSchema json.RawMessage
}

// Usage captures input/output token counts reported by the provider.
type Usage struct {
	InputTokens  int
	OutputTokens int
}

// ChatRequest is the high-level request sent to Provider.Stream.
type ChatRequest struct {
	System         string
	Messages       []Message
	Tools          []ToolSpec
	Model          string
	Temperature    *float32
	MaxTokens      int
	EnableThinking bool
}

// StreamEventType enumerates the unified stream event kinds.
type StreamEventType string

const (
	EventThinkingDelta StreamEventType = "thinking_delta"
	EventTextDelta     StreamEventType = "text_delta"
	EventToolCallStart StreamEventType = "tool_call_start"
	EventToolCallArgs  StreamEventType = "tool_call_args"
	EventToolCallEnd   StreamEventType = "tool_call_end"
	EventMessageEnd    StreamEventType = "message_end"
	EventError         StreamEventType = "error"
)

// StreamEvent is one event on the provider's event channel.
//
// Contract invariants (see contracts/go-interfaces.md#internal-llm):
//   - Exactly one of EventMessageEnd / EventError is emitted per stream.
//   - After either, the channel is closed.
//   - ToolCall events arrive in the order Start → Args* → End, before
//     EventMessageEnd.
//   - All thinking deltas for a given block precede any text delta (Claude/
//     DeepSeek streaming contract).
type StreamEvent struct {
	Type       StreamEventType
	Text       string
	ToolCallID string
	ToolName   string
	ArgsDelta  string
	StopReason string
	Usage      Usage
	Err        error
}

// Provider is the unified LLM provider interface.
type Provider interface {
	Name() string
	// Stream starts a streaming turn. The returned channel is closed by the
	// provider after exactly one EventMessageEnd or EventError is delivered.
	// Callers MUST drain the channel to avoid goroutine leaks.
	Stream(ctx context.Context, req ChatRequest) (<-chan StreamEvent, error)
}
