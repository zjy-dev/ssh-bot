package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	mcpproto "github.com/mark3labs/mcp-go/mcp"

	"github.com/anomalyco/ssh-bot/internal/tool"
)

func prefixedToolName(serverName, toolName string) string {
	return tool.MCPNamePrefix + serverName + "__" + toolName
}

type adaptedTool struct {
	serverName string
	definition mcpproto.Tool
	client     Client
	statusFn   func() ServerStatus
}

func (t *adaptedTool) Name() string { return prefixedToolName(t.serverName, t.definition.Name) }

func (t *adaptedTool) Description() string { return t.definition.Description }

func (t *adaptedTool) InputSchema() json.RawMessage {
	raw, _ := json.Marshal(t.definition.InputSchema)
	if len(t.definition.RawInputSchema) > 0 {
		return append(json.RawMessage(nil), t.definition.RawInputSchema...)
	}
	return raw
}

func (t *adaptedTool) Source() tool.Source { return tool.SourceMCP }

func (t *adaptedTool) Available() bool {
	if t.statusFn == nil {
		return false
	}
	return t.statusFn().Connected
}

func (t *adaptedTool) Call(ctx context.Context, args json.RawMessage) (tool.Result, error) {
	var parsed map[string]any
	if len(args) > 0 {
		if err := json.Unmarshal(args, &parsed); err != nil {
			return tool.Result{}, tool.SimpleUserError("invalid arguments")
		}
	}
	resp, err := t.client.CallTool(ctx, mcpproto.CallToolRequest{Params: mcpproto.CallToolParams{
		Name:      t.definition.Name,
		Arguments: parsed,
	}})
	if err != nil {
		return tool.Result{}, err
	}
	text := callToolResultText(resp)
	if resp.IsError {
		return tool.Result{}, tool.SimpleUserError(text)
	}
	return tool.Result{Content: text}, nil
}

func adaptTools(serverName string, c Client, toolsDef []mcpproto.Tool, statusFn func() ServerStatus) []tool.Tool {
	out := make([]tool.Tool, 0, len(toolsDef))
	for _, def := range toolsDef {
		out = append(out, &adaptedTool{serverName: serverName, definition: def, client: c, statusFn: statusFn})
	}
	return out
}

// AdaptToolsForTest exposes the adapter in contract tests.
func AdaptToolsForTest(serverName string, c Client, toolsDef []mcpproto.Tool) []tool.Tool {
	status := ServerStatus{Name: serverName, Connected: true}
	return adaptTools(serverName, c, toolsDef, func() ServerStatus { return status })
}

func callToolResultText(resp *mcpproto.CallToolResult) string {
	if resp == nil || len(resp.Content) == 0 {
		return ""
	}
	parts := make([]string, 0, len(resp.Content))
	for _, item := range resp.Content {
		switch v := item.(type) {
		case mcpproto.TextContent:
			parts = append(parts, v.Text)
		default:
			raw, _ := json.Marshal(v)
			parts = append(parts, string(raw))
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

func toServerStatus(name string, connected bool, lastErr string, defs []mcpproto.Tool) ServerStatus {
	status := ServerStatus{Name: name, Connected: connected, LastError: lastErr}
	for _, def := range defs {
		status.Tools = append(status.Tools, prefixedToolName(name, def.Name))
	}
	return status
}

func unsupportedTransportError(transport string) error {
	return fmt.Errorf("unsupported mcp transport %q", transport)
}
