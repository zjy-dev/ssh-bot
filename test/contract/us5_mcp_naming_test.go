package contract_test

import (
	"context"
	"encoding/json"
	"testing"

	mcpproto "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"

	internalmcp "github.com/anomalyco/ssh-bot/internal/mcp"
	"github.com/anomalyco/ssh-bot/internal/tool"
)

type fakeMCPClient struct {
	list *mcpproto.ListToolsResult
	call *mcpproto.CallToolResult
}

func (f *fakeMCPClient) Initialize(context.Context, mcpproto.InitializeRequest) (*mcpproto.InitializeResult, error) {
	return &mcpproto.InitializeResult{}, nil
}
func (f *fakeMCPClient) ListTools(context.Context, mcpproto.ListToolsRequest) (*mcpproto.ListToolsResult, error) {
	return f.list, nil
}
func (f *fakeMCPClient) CallTool(context.Context, mcpproto.CallToolRequest) (*mcpproto.CallToolResult, error) {
	if f.call != nil {
		return f.call, nil
	}
	return mcpproto.NewToolResultText("ok"), nil
}
func (f *fakeMCPClient) Close() error { return nil }

func TestUS5_MCPAdapterPrefixesNamesAndPreservesSchema(t *testing.T) {
	rawSchema := json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`)
	defs := []mcpproto.Tool{{Name: "list_directory", Description: "List directory", RawInputSchema: rawSchema}}
	adapted := internalmcp.AdaptToolsForTest("filesystem", &fakeMCPClient{list: &mcpproto.ListToolsResult{Tools: defs}}, defs)
	require.Len(t, adapted, 1)
	require.Equal(t, "mcp__filesystem__list_directory", adapted[0].Name())
	require.Equal(t, tool.SourceMCP, adapted[0].Source())
	require.JSONEq(t, string(rawSchema), string(adapted[0].InputSchema()))
}
