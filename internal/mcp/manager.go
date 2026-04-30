// Package mcp is the Model Context Protocol integration layer, wrapping
// mark3labs/mcp-go. This package's contract (contracts/go-interfaces.md#internal-mcp)
// is satisfied in skeleton form here; real transport code lands in US5 (tasks
// T075-T081).
package mcp

import (
	"context"
	"time"

	"github.com/anomalyco/ssh-bot/internal/tool"
)

// ServerConfig mirrors config.MCPServerConfig to keep this package import-free
// from internal/config.
type ServerConfig struct {
	Name              string
	Enabled           bool
	Transport         string
	Command           string
	Args              []string
	Env               map[string]string
	URL               string
	Headers           map[string]string
	InitializeTimeout time.Duration
}

// ServerStatus is the observable state of one registered MCP server.
type ServerStatus struct {
	Name      string
	Connected bool
	LastError string
	Tools     []string
}

// Manager manages the lifetime of zero or more MCP servers.
//
// The current implementation is a no-op; US5 adds real stdio/http/sse wiring.
// A no-op manager is sufficient to boot the bot with mcp_servers == [] (the
// default in configs/config.example.yaml).
type Manager struct {
	servers []ServerStatus
}

// NewManager returns an empty Manager.
func NewManager() *Manager { return &Manager{} }

// LoadAll loads the given server configs. Per-server failures are recorded
// in Status() and MUST NOT cause LoadAll to return an error (FR-062).
//
// v1 skeleton: accepts configs but does not actually connect.
func (m *Manager) LoadAll(_ context.Context, configs []ServerConfig) error {
	for _, c := range configs {
		m.servers = append(m.servers, ServerStatus{
			Name:      c.Name,
			Connected: false,
			LastError: "mcp transports not yet implemented (US5)",
		})
	}
	return nil
}

// Tools returns tool.Tool adapters for all currently-connected servers.
// Skeleton returns nil.
func (m *Manager) Tools() []tool.Tool { return nil }

// Status returns the observable state of each configured server.
func (m *Manager) Status() []ServerStatus {
	cp := make([]ServerStatus, len(m.servers))
	copy(cp, m.servers)
	return cp
}

// Close stops all managed servers.
func (m *Manager) Close() error { return nil }
