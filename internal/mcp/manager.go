// Package mcp is the Model Context Protocol integration layer, wrapping
// mark3labs/mcp-go. This package's contract (contracts/go-interfaces.md#internal-mcp)
// is satisfied in skeleton form here; real transport code lands in US5 (tasks
// T075-T081).
package mcp

import (
	"context"
	"sync"
	"time"

	mcpproto "github.com/mark3labs/mcp-go/mcp"

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
	mu      sync.RWMutex
	servers map[string]*managedServer
	newFn   func(cfg ServerConfig) (Client, error)
}

type managedServer struct {
	config      ServerConfig
	client      Client
	status      ServerStatus
	adapted     []tool.Tool
	closeClient bool
}

// NewManager returns an empty Manager.
func NewManager() *Manager { return &Manager{servers: map[string]*managedServer{}} }

// NewManagerForTest injects a client factory for contract tests.
func NewManagerForTest(newFn func(cfg ServerConfig) (Client, error)) *Manager {
	return &Manager{servers: map[string]*managedServer{}, newFn: newFn}
}

// LoadAll loads the given server configs. Per-server failures are recorded
// in Status() and MUST NOT cause LoadAll to return an error (FR-062).
//

func (m *Manager) LoadAll(ctx context.Context, configs []ServerConfig) error {
	m.mu.Lock()
	m.servers = map[string]*managedServer{}
	m.mu.Unlock()

	for _, c := range configs {
		if !c.Enabled {
			m.setServer(&managedServer{config: c, status: ServerStatus{Name: c.Name, Connected: false, LastError: "disabled"}})
			continue
		}
		loadCtx, cancel := context.WithTimeout(ctx, timeoutOrDefault(c.InitializeTimeout))
		managed := m.loadOne(loadCtx, c)
		cancel()
		m.setServer(managed)
	}
	return nil
}

// Tools returns tool.Tool adapters for all currently-connected servers.
func (m *Manager) Tools() []tool.Tool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]tool.Tool, 0)
	for _, srv := range m.servers {
		if !srv.status.Connected {
			continue
		}
		out = append(out, srv.adapted...)
	}
	return out
}

// Status returns the observable state of each configured server.
func (m *Manager) Status() []ServerStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()
	cp := make([]ServerStatus, 0, len(m.servers))
	for _, srv := range m.servers {
		cp = append(cp, srv.status)
	}
	return cp
}

// Close stops all managed servers.
func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, srv := range m.servers {
		if srv.client != nil {
			_ = srv.client.Close()
		}
	}
	return nil
}

func (m *Manager) setServer(s *managedServer) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.servers[s.config.Name] = s
}

func (m *Manager) loadOne(ctx context.Context, cfg ServerConfig) *managedServer {
	managed := &managedServer{config: cfg}
	client, err := m.newClient(cfg)
	if err != nil {
		managed.status = ServerStatus{Name: cfg.Name, Connected: false, LastError: err.Error()}
		return managed
	}
	managed.client = client
	if err := initializeClient(ctx, client, cfg.Name); err != nil {
		_ = client.Close()
		managed.client = nil
		managed.status = ServerStatus{Name: cfg.Name, Connected: false, LastError: err.Error()}
		return managed
	}
	list, err := client.ListTools(ctx, mcpproto.ListToolsRequest{})
	if err != nil {
		_ = client.Close()
		managed.client = nil
		managed.status = ServerStatus{Name: cfg.Name, Connected: false, LastError: err.Error()}
		return managed
	}
	managed.status = toServerStatus(cfg.Name, true, "", list.Tools)
	managed.adapted = adaptTools(cfg.Name, client, list.Tools, func() ServerStatus { return managed.status })
	return managed
}

func (m *Manager) newClient(cfg ServerConfig) (Client, error) {
	if m.newFn != nil {
		return m.newFn(cfg)
	}
	switch cfg.Transport {
	case "stdio":
		return newStdioClient(cfg)
	case "http":
		return newHTTPClient(cfg)
	case "sse":
		return newSSEClient(cfg)
	default:
		return nil, unsupportedTransportError(cfg.Transport)
	}
}
