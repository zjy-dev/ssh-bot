package contract_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/anomalyco/ssh-bot/internal/llm"
	"github.com/anomalyco/ssh-bot/internal/render"
)

// mockSender records every Patch call and its body size.
type mockSender struct {
	mu      sync.Mutex
	bodies  [][]byte
	patches int
	// returnRateLimit makes Patch return ErrRateLimited N times, then succeed.
	rateLimitRemaining int
}

func (m *mockSender) Patch(_ context.Context, _ string, body []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.rateLimitRemaining > 0 {
		m.rateLimitRemaining--
		return render.ErrRateLimited
	}
	cp := make([]byte, len(body))
	copy(cp, body)
	m.bodies = append(m.bodies, cp)
	m.patches++
	return nil
}

func (m *mockSender) ReplyInThread(_ context.Context, _, _ string) error { return nil }

func TestRenderer_FlushBatchingAndFinal(t *testing.T) {
	m := &mockSender{}
	r := render.New(m, nil)

	events := make(chan llm.StreamEvent, 8)
	events <- llm.StreamEvent{Type: llm.EventTextDelta, Text: "Hello "}
	events <- llm.StreamEvent{Type: llm.EventTextDelta, Text: "there."}
	events <- llm.StreamEvent{Type: llm.EventMessageEnd}
	close(events)

	require.NoError(t, r.Feed(context.Background(), "mid", events))
	require.NoError(t, r.Stop(context.Background(), "mid"))

	m.mu.Lock()
	defer m.mu.Unlock()
	require.GreaterOrEqual(t, m.patches, 1)

	// The final body must include both pieces of text.
	last := m.bodies[len(m.bodies)-1]
	require.Contains(t, string(last), "Hello ")
	require.Contains(t, string(last), "there.")
}

func TestRenderer_CardJSONSizeBound(t *testing.T) {
	m := &mockSender{}
	r := render.New(m, nil)
	r.State().TraceID = "abc123"

	events := make(chan llm.StreamEvent, 1)
	// Produce a very large text body — renderer should truncate before sending.
	large := make([]byte, 60_000)
	for i := range large {
		large[i] = 'x'
	}
	events <- llm.StreamEvent{Type: llm.EventTextDelta, Text: string(large)}
	events <- llm.StreamEvent{Type: llm.EventMessageEnd}
	close(events)

	require.NoError(t, r.Feed(context.Background(), "mid", events))
	require.NoError(t, r.Stop(context.Background(), "mid"))

	m.mu.Lock()
	defer m.mu.Unlock()
	for _, b := range m.bodies {
		require.LessOrEqual(t, len(b), render.MaxCardJSONBytes+200,
			"every patched body must be under the ~25KB cap")
	}
	// Final body must contain the truncation marker.
	last := m.bodies[len(m.bodies)-1]
	require.Contains(t, string(last), "本卡片过长")
}

func TestRenderer_ThinkingCollapsesOnFirstText(t *testing.T) {
	m := &mockSender{}
	r := render.New(m, nil)

	events := make(chan llm.StreamEvent, 8)
	events <- llm.StreamEvent{Type: llm.EventThinkingDelta, Text: "thinking step 1. "}
	events <- llm.StreamEvent{Type: llm.EventThinkingDelta, Text: "thinking step 2."}
	events <- llm.StreamEvent{Type: llm.EventTextDelta, Text: "Final answer."}
	events <- llm.StreamEvent{Type: llm.EventMessageEnd}
	close(events)

	require.NoError(t, r.Feed(context.Background(), "mid", events))
	require.NoError(t, r.Stop(context.Background(), "mid"))

	m.mu.Lock()
	defer m.mu.Unlock()
	last := m.bodies[len(m.bodies)-1]
	// Thinking region should have collapsed to a small note — "思考中" prose
	// must NOT be present in the final body.
	require.NotContains(t, string(last), "思考中...")
	// Final answer must be in the text region.
	require.Contains(t, string(last), "Final answer.")
}

func TestRenderer_ToolLifecycleEntry(t *testing.T) {
	m := &mockSender{}
	r := render.New(m, nil)

	events := make(chan llm.StreamEvent, 8)
	events <- llm.StreamEvent{Type: llm.EventToolCallStart, ToolCallID: "tc1", ToolName: "web_search", Text: `{"q":"hi"}`}
	events <- llm.StreamEvent{Type: llm.EventToolCallEnd, ToolCallID: "tc1", Text: "success", StopReason: "1234"}
	events <- llm.StreamEvent{Type: llm.EventTextDelta, Text: "here is the answer"}
	events <- llm.StreamEvent{Type: llm.EventMessageEnd}
	close(events)

	require.NoError(t, r.Feed(context.Background(), "mid", events))
	require.NoError(t, r.Stop(context.Background(), "mid"))

	m.mu.Lock()
	defer m.mu.Unlock()
	last := m.bodies[len(m.bodies)-1]
	require.Contains(t, string(last), "🔧")
	require.Contains(t, string(last), "web_search")
	require.Contains(t, string(last), "✅")
}

func TestRenderer_ErrorEventFlipsToErrorCard(t *testing.T) {
	m := &mockSender{}
	r := render.New(m, nil)
	r.State().TraceID = "abc"

	events := make(chan llm.StreamEvent, 4)
	events <- llm.StreamEvent{Type: llm.EventError, Err: errors.New("boom")}
	close(events)

	require.NoError(t, r.Feed(context.Background(), "mid", events))
	require.NoError(t, r.Stop(context.Background(), "mid"))

	m.mu.Lock()
	defer m.mu.Unlock()
	last := m.bodies[len(m.bodies)-1]
	require.Contains(t, string(last), "❌")
	require.Contains(t, string(last), "abc")
}

func TestRenderer_SplitLongBody(t *testing.T) {
	m := &mockSender{}
	r := render.New(m, nil)

	big := make([]byte, 80_000)
	for i := range big {
		big[i] = 'x'
	}
	events := make(chan llm.StreamEvent, 2)
	events <- llm.StreamEvent{Type: llm.EventTextDelta, Text: string(big)}
	events <- llm.StreamEvent{Type: llm.EventMessageEnd}
	close(events)

	require.NoError(t, r.Feed(context.Background(), "mid", events))
	inCard, threaded := r.SplitLongBody()
	require.LessOrEqual(t, len(inCard), 20_000)
	require.NotEmpty(t, threaded, "thread continuations expected for 80KB body")
	require.NoError(t, r.Stop(context.Background(), "mid"))
}

func TestRenderer_BackoffOnRateLimit(t *testing.T) {
	m := &mockSender{rateLimitRemaining: 2}
	r := render.New(m, nil)

	events := make(chan llm.StreamEvent, 8)
	events <- llm.StreamEvent{Type: llm.EventTextDelta, Text: "a"}
	events <- llm.StreamEvent{Type: llm.EventTextDelta, Text: "b"}
	events <- llm.StreamEvent{Type: llm.EventTextDelta, Text: "c"}
	events <- llm.StreamEvent{Type: llm.EventMessageEnd}
	close(events)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	require.NoError(t, r.Feed(ctx, "mid", events))
	require.NoError(t, r.Stop(ctx, "mid"))

	m.mu.Lock()
	defer m.mu.Unlock()
	require.GreaterOrEqual(t, m.patches, 1, "should have eventually succeeded after rate-limit back-off")
}

// Guard against accidental JSON validity regressions.
func TestRenderer_FinalBodyIsValidJSON(t *testing.T) {
	m := &mockSender{}
	r := render.New(m, nil)
	events := make(chan llm.StreamEvent, 2)
	events <- llm.StreamEvent{Type: llm.EventTextDelta, Text: "hi"}
	events <- llm.StreamEvent{Type: llm.EventMessageEnd}
	close(events)
	require.NoError(t, r.Feed(context.Background(), "mid", events))
	require.NoError(t, r.Stop(context.Background(), "mid"))
	m.mu.Lock()
	defer m.mu.Unlock()
	var parsed map[string]any
	require.NoError(t, json.Unmarshal(m.bodies[len(m.bodies)-1], &parsed))
	require.Contains(t, parsed, "config")
	require.Contains(t, parsed, "elements")
}
