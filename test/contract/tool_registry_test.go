package contract_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/anomalyco/ssh-bot/internal/tool"
	"github.com/anomalyco/ssh-bot/internal/tool/builtin"
)

// fakeTool is a minimal Tool used for registry tests.
type fakeTool struct {
	name      string
	source    tool.Source
	available bool
}

func (f *fakeTool) Name() string                 { return f.name }
func (f *fakeTool) Description() string          { return "fake" }
func (f *fakeTool) InputSchema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (f *fakeTool) Source() tool.Source          { return f.source }
func (f *fakeTool) Available() bool              { return f.available }
func (f *fakeTool) Call(context.Context, json.RawMessage) (tool.Result, error) {
	return tool.Result{Content: "ok"}, nil
}

func TestRegistry_RegisterDuplicateRejected(t *testing.T) {
	r := tool.NewRegistry()
	require.NoError(t, r.Register(&fakeTool{name: "a", source: tool.SourceBuiltin, available: true}))
	err := r.Register(&fakeTool{name: "a", source: tool.SourceBuiltin, available: true})
	require.Error(t, err)
	require.Contains(t, err.Error(), "already registered")
}

func TestRegistry_MCPNameEnforcement(t *testing.T) {
	r := tool.NewRegistry()
	// MCP source without prefix: rejected.
	require.Error(t, r.Register(&fakeTool{name: "no_prefix", source: tool.SourceMCP, available: true}))
	// Builtin with mcp__ prefix: rejected.
	require.Error(t, r.Register(&fakeTool{name: "mcp__builtin", source: tool.SourceBuiltin, available: true}))
	// MCP with prefix: accepted.
	require.NoError(t, r.Register(&fakeTool{name: "mcp__fs__read", source: tool.SourceMCP, available: true}))
}

func TestRegistry_ListOrdering(t *testing.T) {
	r := tool.NewRegistry()
	require.NoError(t, r.Register(&fakeTool{name: "c_builtin", source: tool.SourceBuiltin, available: true}))
	require.NoError(t, r.Register(&fakeTool{name: "a_builtin", source: tool.SourceBuiltin, available: true}))
	require.NoError(t, r.Register(&fakeTool{name: "mcp__x__do", source: tool.SourceMCP, available: true}))
	require.NoError(t, r.Register(&fakeTool{name: "mcp__a__go", source: tool.SourceMCP, available: true}))

	list := r.List()
	require.Equal(t, 4, len(list))
	// Builtins first, sorted by name.
	require.Equal(t, "a_builtin", list[0].Name())
	require.Equal(t, "c_builtin", list[1].Name())
	// Then MCP tools, sorted by name (prefix keeps grouping-by-server implicit).
	require.Equal(t, "mcp__a__go", list[2].Name())
	require.Equal(t, "mcp__x__do", list[3].Name())
}

func TestRegistry_AvailableFiltersDisabled(t *testing.T) {
	r := tool.NewRegistry()
	require.NoError(t, r.Register(&fakeTool{name: "on", source: tool.SourceBuiltin, available: true}))
	require.NoError(t, r.Register(&fakeTool{name: "off", source: tool.SourceBuiltin, available: false}))

	all := r.List()
	require.Len(t, all, 2)
	avail := r.Available()
	require.Len(t, avail, 1)
	require.Equal(t, "on", avail[0].Name())
}

func TestDatetimeTool_Now(t *testing.T) {
	tl := builtin.NewDatetime()
	res, err := tl.Call(context.Background(), json.RawMessage(`{"action":"now","timezone":"UTC"}`))
	require.NoError(t, err)
	require.NotEmpty(t, res.Content)
}
