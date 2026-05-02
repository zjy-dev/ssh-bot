package contract_test

import (
	"context"
	"fmt"
	"testing"

	mcpproto "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"

	internalmcp "github.com/anomalyco/ssh-bot/internal/mcp"
)

func TestUS5_ManagerLoadAllIsolatesBrokenServer(t *testing.T) {
	mgr := internalmcp.NewManagerForTest(func(cfg internalmcp.ServerConfig) (internalmcp.Client, error) {
		if cfg.Name == "broken" {
			return nil, fmt.Errorf("dial failed")
		}
		return &fakeMCPClient{list: &mcpproto.ListToolsResult{Tools: []mcpproto.Tool{{Name: "ok_tool", Description: "ok"}}}}, nil
	})

	err := mgr.LoadAll(context.Background(), []internalmcp.ServerConfig{
		{Name: "broken", Enabled: true, Transport: "http", URL: "http://127.0.0.1:1"},
		{Name: "working", Enabled: true, Transport: "stdio", Command: "dummy"},
	})
	require.NoError(t, err)

	statuses := mgr.Status()
	require.Len(t, statuses, 2)
	var broken, working *internalmcp.ServerStatus
	for i := range statuses {
		if statuses[i].Name == "broken" {
			broken = &statuses[i]
		}
		if statuses[i].Name == "working" {
			working = &statuses[i]
		}
	}
	require.NotNil(t, broken)
	require.NotNil(t, working)
	require.False(t, broken.Connected)
	require.NotEmpty(t, broken.LastError)
	require.True(t, working.Connected)
	require.NotEmpty(t, working.Tools)
	require.Equal(t, "mcp__working__ok_tool", working.Tools[0])
	require.Len(t, mgr.Tools(), 1)
	require.Equal(t, "mcp__working__ok_tool", mgr.Tools()[0].Name())
}
