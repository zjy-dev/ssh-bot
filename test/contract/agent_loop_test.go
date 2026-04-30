package contract_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/anomalyco/ssh-bot/internal/agent"
	"github.com/anomalyco/ssh-bot/internal/llm"
	"github.com/anomalyco/ssh-bot/internal/session"
	"github.com/anomalyco/ssh-bot/internal/tool"
)

// echoTool returns a fixed content; useful for asserting tool result plumbing.
type echoTool struct {
	name     string
	output   string
	err      error
	panicMsg string
}

func (e *echoTool) Name() string                 { return e.name }
func (e *echoTool) Description() string          { return "echo" }
func (e *echoTool) InputSchema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (e *echoTool) Source() tool.Source          { return tool.SourceBuiltin }
func (e *echoTool) Available() bool              { return true }
func (e *echoTool) Call(_ context.Context, _ json.RawMessage) (tool.Result, error) {
	if e.panicMsg != "" {
		panic(e.panicMsg)
	}
	if e.err != nil {
		return tool.Result{}, e.err
	}
	return tool.Result{Content: e.output}, nil
}

func newSession() *session.Session {
	return &session.Session{Key: "test", ChatType: "p2p", UserOpenID: "u1"}
}

func TestAgentLoop_SimpleAnswer(t *testing.T) {
	// One Stream call that returns a plain-text answer then ends.
	prov := llm.NewFakeProvider("fake", [][]llm.StreamEvent{
		{
			{Type: llm.EventTextDelta, Text: "Hello there."},
			{Type: llm.EventMessageEnd},
		},
	})
	reg := tool.NewRegistry()
	store := session.NewMemoryStore()

	a := agent.NewAgent(prov, reg, store)
	sess := newSession()

	require.NoError(t, a.Run(context.Background(), sess, "hi", nil))

	// Session now contains: user, assistant (with "Hello there.").
	require.Len(t, sess.Messages, 2)
	require.Equal(t, llm.RoleUser, sess.Messages[0].Role)
	require.Equal(t, llm.RoleAssistant, sess.Messages[1].Role)
	require.Equal(t, "Hello there.", sess.Messages[1].Content)
	require.Empty(t, sess.Messages[1].ToolCalls)
}

func TestAgentLoop_ToolCallFeedback(t *testing.T) {
	// Turn 1: assistant requests tool "echo" with args {"x":1}; then ends.
	// Turn 2: assistant emits final text and ends.
	prov := llm.NewFakeProvider("fake", [][]llm.StreamEvent{
		{
			{Type: llm.EventToolCallStart, ToolCallID: "tc1", ToolName: "echo"},
			{Type: llm.EventToolCallArgs, ToolCallID: "tc1", ArgsDelta: `{"x":1}`},
			{Type: llm.EventToolCallEnd, ToolCallID: "tc1"},
			{Type: llm.EventMessageEnd, StopReason: "tool_use"},
		},
		{
			{Type: llm.EventTextDelta, Text: "Done."},
			{Type: llm.EventMessageEnd},
		},
	})
	reg := tool.NewRegistry()
	require.NoError(t, reg.Register(&echoTool{name: "echo", output: "tool-saw: ok"}))
	store := session.NewMemoryStore()

	a := agent.NewAgent(prov, reg, store)
	sess := newSession()
	require.NoError(t, a.Run(context.Background(), sess, "please use echo", nil))

	// Expect: user, assistant (with tool_call), tool result, assistant (final)
	require.Len(t, sess.Messages, 4)
	require.Equal(t, llm.RoleUser, sess.Messages[0].Role)

	asst1 := sess.Messages[1]
	require.Equal(t, llm.RoleAssistant, asst1.Role)
	require.Len(t, asst1.ToolCalls, 1)
	require.Equal(t, "tc1", asst1.ToolCalls[0].ID)
	require.Equal(t, "echo", asst1.ToolCalls[0].Name)

	toolMsg := sess.Messages[2]
	require.Equal(t, llm.RoleTool, toolMsg.Role)
	require.Equal(t, "tc1", toolMsg.ToolCallID)
	require.False(t, toolMsg.IsError)
	require.Equal(t, "tool-saw: ok", toolMsg.Content)

	asst2 := sess.Messages[3]
	require.Equal(t, llm.RoleAssistant, asst2.Role)
	require.Equal(t, "Done.", asst2.Content)
}

func TestAgentLoop_ToolPanicBecomesIsError(t *testing.T) {
	prov := llm.NewFakeProvider("fake", [][]llm.StreamEvent{
		{
			{Type: llm.EventToolCallStart, ToolCallID: "tc1", ToolName: "boom"},
			{Type: llm.EventToolCallArgs, ToolCallID: "tc1", ArgsDelta: `{}`},
			{Type: llm.EventToolCallEnd, ToolCallID: "tc1"},
			{Type: llm.EventMessageEnd, StopReason: "tool_use"},
		},
		{
			{Type: llm.EventTextDelta, Text: "Handled."},
			{Type: llm.EventMessageEnd},
		},
	})
	reg := tool.NewRegistry()
	require.NoError(t, reg.Register(&echoTool{name: "boom", panicMsg: "deliberate"}))

	a := agent.NewAgent(prov, reg, session.NewMemoryStore())
	sess := newSession()
	require.NoError(t, a.Run(context.Background(), sess, "go", nil))

	// Tool message must exist with IsError=true, Content mentions "internal error".
	var foundTool bool
	for _, m := range sess.Messages {
		if m.Role == llm.RoleTool {
			foundTool = true
			require.True(t, m.IsError, "panic must surface as IsError=true")
			require.Contains(t, m.Content, "internal error")
		}
	}
	require.True(t, foundTool)
}

func TestAgentLoop_UnknownToolIsError(t *testing.T) {
	prov := llm.NewFakeProvider("fake", [][]llm.StreamEvent{
		{
			{Type: llm.EventToolCallStart, ToolCallID: "tc1", ToolName: "ghost"},
			{Type: llm.EventToolCallArgs, ToolCallID: "tc1", ArgsDelta: `{}`},
			{Type: llm.EventToolCallEnd, ToolCallID: "tc1"},
			{Type: llm.EventMessageEnd, StopReason: "tool_use"},
		},
		{
			{Type: llm.EventTextDelta, Text: "I see, that tool doesn't exist."},
			{Type: llm.EventMessageEnd},
		},
	})
	reg := tool.NewRegistry()
	a := agent.NewAgent(prov, reg, session.NewMemoryStore())
	sess := newSession()
	require.NoError(t, a.Run(context.Background(), sess, "x", nil))

	// Find the tool-role message.
	var got session.Session // zero
	_ = got
	var toolContent string
	for _, m := range sess.Messages {
		if m.Role == llm.RoleTool {
			toolContent = m.Content
		}
	}
	require.Contains(t, toolContent, "unknown tool")
}

func TestAgentLoop_MaxStepsReached(t *testing.T) {
	// Script that always requests a tool call → guarantees the loop runs to
	// MaxSteps. We script MaxSteps turns of "tool request" then the loop
	// gives up.
	script := make([][]llm.StreamEvent, agent.DefaultMaxSteps+1)
	for i := range script {
		script[i] = []llm.StreamEvent{
			{Type: llm.EventToolCallStart, ToolCallID: "tc", ToolName: "echo"},
			{Type: llm.EventToolCallArgs, ToolCallID: "tc", ArgsDelta: `{}`},
			{Type: llm.EventToolCallEnd, ToolCallID: "tc"},
			{Type: llm.EventMessageEnd, StopReason: "tool_use"},
		}
	}
	prov := llm.NewFakeProvider("fake", script)
	reg := tool.NewRegistry()
	require.NoError(t, reg.Register(&echoTool{name: "echo", output: "again"}))
	a := agent.NewAgent(prov, reg, session.NewMemoryStore())
	a.MaxSteps = 3 // keep the test fast

	sess := newSession()
	err := a.Run(context.Background(), sess, "loop!", nil)
	require.ErrorIs(t, err, agent.ErrMaxStepsReached)
}

func TestAgentLoop_EmitsSyntheticToolLifecycle(t *testing.T) {
	prov := llm.NewFakeProvider("fake", [][]llm.StreamEvent{
		{
			{Type: llm.EventToolCallStart, ToolCallID: "tc1", ToolName: "echo"},
			{Type: llm.EventToolCallArgs, ToolCallID: "tc1", ArgsDelta: `{"k":1}`},
			{Type: llm.EventToolCallEnd, ToolCallID: "tc1"},
			{Type: llm.EventMessageEnd, StopReason: "tool_use"},
		},
		{
			{Type: llm.EventTextDelta, Text: "ok"},
			{Type: llm.EventMessageEnd},
		},
	})
	reg := tool.NewRegistry()
	require.NoError(t, reg.Register(&echoTool{name: "echo", output: "res"}))
	a := agent.NewAgent(prov, reg, session.NewMemoryStore())

	var types []llm.StreamEventType
	emit := func(ev llm.StreamEvent) {
		types = append(types, ev.Type)
	}
	require.NoError(t, a.Run(context.Background(), newSession(), "x", emit))

	// We expect at least: MessageEnd (turn1), synthetic Start, synthetic End,
	// TextDelta (turn2), MessageEnd (turn2).
	require.Contains(t, types, llm.EventToolCallStart)
	require.Contains(t, types, llm.EventToolCallEnd)
	require.Contains(t, types, llm.EventTextDelta)
	// MessageEnd appears twice (once per provider turn).
	count := 0
	for _, ty := range types {
		if ty == llm.EventMessageEnd {
			count++
		}
	}
	require.Equal(t, 2, count)
}

func TestAgentLoop_ErrorPropagates(t *testing.T) {
	prov := llm.NewFakeProvider("fake", [][]llm.StreamEvent{
		{{Type: llm.EventError, Err: errors.New("upstream boom")}},
	})
	reg := tool.NewRegistry()
	a := agent.NewAgent(prov, reg, session.NewMemoryStore())
	err := a.Run(context.Background(), newSession(), "hi", nil)
	require.Error(t, err)
	require.True(t, strings.Contains(err.Error(), "upstream boom"))
}
