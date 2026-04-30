package contract_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/anomalyco/ssh-bot/internal/llm"
)

// TestFakeProvider_OrderingInvariants exercises the contract listed in
// contracts/go-interfaces.md#internal-llm: thinking deltas before text deltas,
// tool-call Start→Args*→End, exactly one terminator, channel closed after.
func TestFakeProvider_OrderingInvariants(t *testing.T) {
	script := [][]llm.StreamEvent{
		{
			{Type: llm.EventThinkingDelta, Text: "Let me think... "},
			{Type: llm.EventThinkingDelta, Text: "ok."},
			{Type: llm.EventTextDelta, Text: "Here is the answer: "},
			{Type: llm.EventToolCallStart, ToolCallID: "tc1", ToolName: "search"},
			{Type: llm.EventToolCallArgs, ToolCallID: "tc1", ArgsDelta: `{"q":`},
			{Type: llm.EventToolCallArgs, ToolCallID: "tc1", ArgsDelta: `"x"}`},
			{Type: llm.EventToolCallEnd, ToolCallID: "tc1"},
			{Type: llm.EventMessageEnd, StopReason: "tool_use"},
		},
	}
	p := llm.NewFakeProvider("fake", script)
	ch, err := p.Stream(context.Background(), llm.ChatRequest{})
	require.NoError(t, err)

	var seq []llm.StreamEventType
	for ev := range ch {
		seq = append(seq, ev.Type)
	}
	// channel drained and closed
	_, ok := <-ch
	require.False(t, ok, "channel must be closed")

	// Ordering: thinking precedes text; tool start/args/end contiguous; end is last.
	require.Equal(t, []llm.StreamEventType{
		llm.EventThinkingDelta,
		llm.EventThinkingDelta,
		llm.EventTextDelta,
		llm.EventToolCallStart,
		llm.EventToolCallArgs,
		llm.EventToolCallArgs,
		llm.EventToolCallEnd,
		llm.EventMessageEnd,
	}, seq)
}

func TestFakeProvider_ErrorTerminator(t *testing.T) {
	script := [][]llm.StreamEvent{
		{
			{Type: llm.EventTextDelta, Text: "partial..."},
			{Type: llm.EventError, Err: errStub("upstream timeout")},
		},
	}
	p := llm.NewFakeProvider("fake", script)
	ch, err := p.Stream(context.Background(), llm.ChatRequest{})
	require.NoError(t, err)

	var last llm.StreamEvent
	count := 0
	for ev := range ch {
		last = ev
		count++
	}
	require.Equal(t, 2, count)
	require.Equal(t, llm.EventError, last.Type)
	require.EqualError(t, last.Err, "upstream timeout")
}

func TestFakeProvider_ScriptExhausted(t *testing.T) {
	p := llm.NewFakeProvider("fake", nil)
	_, err := p.Stream(context.Background(), llm.ChatRequest{})
	require.Error(t, err)
}

// errStub lets us construct error values without importing fmt.Errorf.
type errStub string

func (e errStub) Error() string { return string(e) }

// TestToolSpecJSONSchema verifies that our ToolSpec's InputSchema field holds
// valid JSON that our eino adapter would consume. (We don't call into eino
// here; that's covered by integration tests.)
func TestToolSpecJSONSchema(t *testing.T) {
	raw := json.RawMessage(`{"type":"object","properties":{"q":{"type":"string"}},"required":["q"]}`)
	spec := llm.ToolSpec{
		Name:        "search",
		Description: "Search the web.",
		InputSchema: raw,
	}
	require.Equal(t, "search", spec.Name)
	require.NotEmpty(t, spec.InputSchema)
	// Must be syntactically valid JSON.
	var parsed map[string]any
	require.NoError(t, json.Unmarshal(spec.InputSchema, &parsed))
	require.Equal(t, "object", parsed["type"])
}
