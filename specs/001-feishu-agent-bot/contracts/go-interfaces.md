# Internal Go Contracts

**Date**: 2026-04-30
**Audience**: Go developers implementing `internal/...`. These are the package-level interfaces that form the seams of the system. Implementations MUST match these signatures exactly; tests pin to these contracts.

All pseudocode is Go. Imports elided. Error wrapping via `fmt.Errorf("%w", err)` is assumed throughout.

---

## `internal/llm` — Provider

```go
package llm

type Role string
const (
    RoleUser      Role = "user"
    RoleAssistant Role = "assistant"
    RoleTool      Role = "tool"
)

type Message struct {
    Role       Role
    Content    string
    Thinking   string                // assistant only
    ToolCalls  []ToolCall            // assistant only
    ToolCallID string                // tool only
    Name       string                // tool only
    IsError    bool                  // tool only
}

type ToolCall struct {
    ID        string
    Name      string
    Arguments json.RawMessage
}

type ChatRequest struct {
    System         string
    Messages       []Message
    Tools          []ToolSpec        // NOT tool.Tool; see ToolSpec below
    Model          string
    Temperature    *float32
    MaxTokens      int
    EnableThinking bool
}

type ToolSpec struct {
    Name        string
    Description string
    InputSchema json.RawMessage
}

type Usage struct {
    InputTokens  int
    OutputTokens int
}

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

type Provider interface {
    Name() string
    // Stream returns a channel that will be closed after EventMessageEnd or EventError.
    // Caller MUST drain the channel to avoid goroutine leaks.
    Stream(ctx context.Context, req ChatRequest) (<-chan StreamEvent, error)
}
```

**Contract invariants**:
- Exactly one of `EventMessageEnd` or `EventError` is emitted per stream; after either, the channel is closed.
- `EventToolCallStart` is emitted once per call ID, `EventToolCallArgs` zero-or-more times, then `EventToolCallEnd` once, before `EventMessageEnd`.
- `EventThinkingDelta` and `EventTextDelta` MUST NOT interleave within the same "block": all thinking deltas for a given reasoning block precede all text deltas (matches Anthropic/DeepSeek streaming). Caller may assume this to drive the renderer's "first text delta clears thinking region" rule (FR-032).

---

## `internal/tool` — Tool

```go
package tool

type Source string
const (
    SourceBuiltin Source = "builtin"
    SourceMCP     Source = "mcp"
)

type Result struct {
    Content string                 // markdown or plain text; fed back into LLM
    Meta    map[string]any         // UI-only; durations, URLs, etc.
}

type Tool interface {
    Name() string                  // unique; MCP tools use "mcp__<server>__<name>"
    Description() string
    InputSchema() json.RawMessage  // JSON Schema draft-07
    Source() Source
    Available() bool               // false when backing service is disconnected
    Call(ctx context.Context, args json.RawMessage) (Result, error)
}

type Registry interface {
    Register(t Tool) error         // error on name collision
    Get(name string) (Tool, bool)
    List() []Tool                  // stable ordering: builtins first by name, then MCP grouped by server
    Available() []Tool             // List() filtered by t.Available()
}
```

**Invariants**:
- `Registry.Register` MUST reject duplicate names without silently overwriting.
- `Tool.Call` MUST return within `30s` (agent enforces via context cancellation). Tools must honor `ctx.Done()`.
- Tools MUST NOT panic — but the agent runner wraps every invocation in a `recover` (FR-042).

---

## `internal/session` — Store + Lock

```go
package session

type Session struct {
    Key        string
    UserOpenID string
    ChatID     string
    ChatType   string          // "p2p" | "group"
    Provider   string          // ModelProfile alias
    Messages   []llm.Message
    CreatedAt  time.Time
    UpdatedAt  time.Time
    TraceID    string
}

type Store interface {
    // Get returns (nil, nil) when the key does not exist (not an error).
    Get(ctx context.Context, key string) (*Session, error)
    Save(ctx context.Context, key string, sess *Session) error   // resets 24h TTL
    Delete(ctx context.Context, key string) error                 // idempotent
}

type Locker interface {
    // TryAcquire returns (token, true, nil) on success; ("", false, nil) when
    // another holder is active (NOT an error: caller replies "上一条还在处理中").
    // token is the random value the caller must present to Release.
    TryAcquire(ctx context.Context, key string, ttl time.Duration) (token string, ok bool, err error)
    Release(ctx context.Context, key, token string) error        // idempotent; no-op if already expired
}
```

**Invariants**:
- `Store.Get` returning `(nil, nil)` is the ONLY signal for "no session yet"; a fresh session is created in-memory by the caller.
- `Locker.Release` uses Lua `GET+DEL if match` to prevent releasing a lock owned by a later attempt.
- TTL for a lock is 60 s (D6). Agent turns that exceed 60 s lose their lock; the next TryAcquire from another message may succeed. This is accepted: truly runaway turns are bounded by `MaxSteps` anyway.

---

## `internal/mcp` — Manager

```go
package mcp

type ServerConfig struct {
    Name              string
    Enabled           bool
    Transport         string            // "stdio" | "http" | "sse"
    Command           string
    Args              []string
    Env               map[string]string
    URL               string
    Headers           map[string]string
    InitializeTimeout time.Duration     // default 10s
}

type ServerStatus struct {
    Name      string
    Connected bool
    LastError string
    Tools     []string                  // names exposed by this server
}

type Manager interface {
    // LoadAll starts all enabled servers. Returns nil if at least the manager
    // initialized; per-server failures are reported via Status() and do not
    // return error (FR-062).
    LoadAll(ctx context.Context, configs []ServerConfig) error
    // Tools returns tool.Tool adapters for every currently-connected server.
    Tools() []tool.Tool
    Status() []ServerStatus
    Close() error                       // stops stdio children, closes transports
}
```

---

## `internal/agent` — Agent

```go
package agent

type Agent struct {
    Provider llm.Provider
    Tools    tool.Registry
    Store    session.Store
    MaxSteps int                        // default 12
}

// Run drives the loop. `emit` is called for every llm.StreamEvent AND for
// synthetic tool-lifecycle events the agent generates itself (see below).
// Run MUST flush (append-and-Save) the session at least once per step.
func (a *Agent) Run(
    ctx context.Context,
    sess *session.Session,
    userInput string,
    emit func(llm.StreamEvent),
) error
```

**Synthetic events emitted by the agent** (in addition to provider events):
- `EventToolCallStart` — re-emitted after args are fully accumulated, with `Arguments` encoded into `Text` as JSON (so the renderer can show a preview).
- `EventToolCallEnd` — with `Text` = `"success"` or `"error: <truncated>"`, `StopReason` carrying elapsed duration in ms as a string.

This dual-emission pattern lets `internal/render` treat provider output and tool lifecycle uniformly.

---

## `internal/render` — Renderer (interface)

```go
package render

type Renderer interface {
    // Start opens a card in the given chat, returns the message_id.
    Start(ctx context.Context, chatID string) (messageID string, err error)
    // Feed consumes events from an agent run. Runs a 250ms ticker goroutine
    // internally to batch PATCH updates. Must be called from exactly one
    // goroutine per run.
    Feed(ctx context.Context, messageID string, events <-chan llm.StreamEvent) error
    // Stop flushes the final state and writes the terminal card snapshot.
    Stop(ctx context.Context, messageID string) error
}
```

**Invariants**:
- Minimum flush interval: **250 ms** (D4). Deadline-sensitive chunks may force-flush at `Stop`.
- On 430020 "frequency limit" error from Feishu, the Feeder MUST back off to **500 ms** for the remainder of the run.

---

## `internal/lark` — Handler

```go
package lark

type MessageEvent struct {
    ChatID       string
    ChatType     string   // "p2p" | "group"
    SenderOpenID string
    MessageID    string
    Text         string   // post-mention-stripping
    MentionedBot bool
    Raw          *larkim.P2MessageReceiveV1   // provider-native struct for debugging
}

type Handler interface {
    Handle(ctx context.Context, ev MessageEvent) error
}
```

No explicit "command" contract: commands are dispatched by string match inside `Handle`. They consume `ev.Text`, short-circuit, and reply directly via `internal/lark/sender`.

---

## Config

```go
package config

type Config struct {
    Server struct {
        LogLevel      string
        OAuthHTTPAddr string     // e.g. "127.0.0.1:8080"; reverse-proxied externally
        PublicBaseURL string     // e.g. "https://bot.example.com"; used to construct redirect_uri
    }
    Lark struct {
        AppID, AppSecret   string
        VerificationToken  string
        EncryptKey         string
        BotOpenID          string
    }
    Redis struct {
        Addr, Password string
        DB             int
        SessionTTL     time.Duration
    }
    LLM struct {
        DefaultProvider string
        MaxSteps        int
        Providers       map[string]ModelProfile
    }
    Tools       map[string]any          // per-tool sub-configs
    MCPServers  []mcp.ServerConfig
    OAuth struct {
        EncryptionKeyEnv string          // name of env var containing AES-GCM key
        Scopes           []string         // scopes to request at authorize time
    }
}

// Load honours env-var interpolation (${VAR}).
func Load(path string) (*Config, error)
```
