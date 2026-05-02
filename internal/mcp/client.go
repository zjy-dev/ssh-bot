package mcp

import (
	"context"
	"fmt"
	"net/http"
	"time"

	mcpclient "github.com/mark3labs/mcp-go/client"
	mcptransport "github.com/mark3labs/mcp-go/client/transport"
	mcpproto "github.com/mark3labs/mcp-go/mcp"
)

// Client is the minimal MCP client surface the manager/adapter need.
type Client interface {
	Initialize(ctx context.Context, request mcpproto.InitializeRequest) (*mcpproto.InitializeResult, error)
	ListTools(ctx context.Context, request mcpproto.ListToolsRequest) (*mcpproto.ListToolsResult, error)
	CallTool(ctx context.Context, request mcpproto.CallToolRequest) (*mcpproto.CallToolResult, error)
	Close() error
}

func initializeClient(ctx context.Context, c Client, name string) error {
	_, err := c.Initialize(ctx, mcpproto.InitializeRequest{Params: mcpproto.InitializeParams{
		ProtocolVersion: mcpproto.LATEST_PROTOCOL_VERSION,
		ClientInfo: mcpproto.Implementation{
			Name:    "ssh-bot-mcp-client-" + name,
			Version: "1.0.0",
		},
		Capabilities: mcpproto.ClientCapabilities{},
	}})
	if err != nil {
		return fmt.Errorf("initialize mcp client %q: %w", name, err)
	}
	return nil
}

func newStdioClient(cfg ServerConfig) (Client, error) {
	return mcpclient.NewStdioMCPClient(cfg.Command, envMapToList(cfg.Env), cfg.Args...)
}

func newHTTPClient(cfg ServerConfig) (Client, error) {
	transport, err := mcptransport.NewStreamableHTTP(
		cfg.URL,
		mcptransport.WithHTTPHeaders(cfg.Headers),
		mcptransport.WithContinuousListening(),
		mcptransport.WithHTTPTimeout(timeoutOrDefault(cfg.InitializeTimeout)),
	)
	if err != nil {
		return nil, err
	}
	return mcpclient.NewClient(transport), nil
}

func newSSEClient(cfg ServerConfig) (Client, error) {
	return mcpclient.NewSSEMCPClient(
		cfg.URL,
		mcpclient.WithHeaders(cfg.Headers),
		mcpclient.WithHTTPClient(&http.Client{Timeout: timeoutOrDefault(cfg.InitializeTimeout)}),
	)
}

func timeoutOrDefault(d time.Duration) time.Duration {
	if d <= 0 {
		return 10 * time.Second
	}
	return d
}

func envMapToList(m map[string]string) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k, v := range m {
		out = append(out, k+"="+v)
	}
	return out
}
