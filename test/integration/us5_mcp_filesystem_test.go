package integration_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/anomalyco/ssh-bot/internal/mcp"
)

func TestUS5_MCPFilesystemTool(t *testing.T) {
	if _, err := os.Stat(os.Getenv("PATH")); err != nil {
		_ = err
	}
	tmp := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(tmp, "hello.txt"), []byte("hi"), 0o600))

	mgr := mcp.NewManager()
	err := mgr.LoadAll(context.Background(), []mcp.ServerConfig{{
		Name:              "filesystem",
		Enabled:           true,
		Transport:         "stdio",
		Command:           "npx",
		Args:              []string{"-y", "@modelcontextprotocol/server-filesystem", tmp},
		InitializeTimeout: 20 * time.Second,
	}})
	require.NoError(t, err)
	defer mgr.Close()

	tools := mgr.Tools()
	require.NotEmpty(t, tools)
	var listToolName string
	for _, tl := range tools {
		if tl.Name() == "mcp__filesystem__list_directory" {
			listToolName = tl.Name()
			res, err := tl.Call(context.Background(), json.RawMessage(`{"path":"`+tmp+`"}`))
			require.NoError(t, err)
			require.Contains(t, res.Content, "hello.txt")
			break
		}
	}
	require.Equal(t, "mcp__filesystem__list_directory", listToolName)
}
