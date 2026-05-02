// Package tool defines the bot's tool-execution abstraction. Tools may be
// built-in (Go code) or sourced from MCP servers via the mcp package.
//
// The LLM layer consumes a ToolSpec (see internal/llm); this package holds the
// richer Tool interface used by the agent loop at invocation time.
//
// See contracts/go-interfaces.md#internal-tool.
package tool

import (
	"context"
	"encoding/json"
)

// Source identifies where a Tool comes from.
type Source string

const (
	SourceBuiltin Source = "builtin"
	SourceMCP     Source = "mcp"
)

// MCPNamePrefix is the required prefix for MCP-sourced tools (FR-061).
const MCPNamePrefix = "mcp__"

// Result is the value returned by Tool.Call.
//
// Content is the markdown (or plain text) that will be fed back into the model
// as the tool-role message. Meta carries data for UI display only (durations,
// source URLs, extracted titles, …) and is NOT sent to the model.
type Result struct {
	Content string
	Meta    map[string]any
}

// UserError is an error whose message is already suitable to surface back to
// the model/user as-is.
type UserError interface {
	error
	UserMessage() string
}

// SimpleUserError is the common one-line implementation of UserError.
type SimpleUserError string

func (e SimpleUserError) Error() string       { return string(e) }
func (e SimpleUserError) UserMessage() string { return string(e) }

// Tool is one invocable capability.
type Tool interface {
	Name() string
	Description() string
	// InputSchema returns a JSON Schema (draft-07) describing the tool's
	// arguments. The bytes are passed through to the LLM adapter.
	InputSchema() json.RawMessage
	Source() Source
	// Available reports whether the tool's backing service is reachable.
	// MCP tools flip this to false when their server disconnects; builtins
	// typically return true unless disabled by config.
	Available() bool
	// Call executes the tool. Implementations MUST honor ctx.Done()
	// (the agent imposes a 30s timeout, D9).
	Call(ctx context.Context, args json.RawMessage) (Result, error)
}
