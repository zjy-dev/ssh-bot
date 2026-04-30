package tool

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Registry holds the set of tools exposed to the agent loop at runtime.
// Names are globally unique; Register returns an error on collision.
type Registry struct {
	mu   sync.RWMutex
	data map[string]Tool
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{data: map[string]Tool{}}
}

// Register adds t. Returns an error if t.Name() is already registered.
// Enforces that MCP-sourced tools carry the MCPNamePrefix (FR-061).
func (r *Registry) Register(t Tool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := t.Name()
	if n == "" {
		return fmt.Errorf("tool name is empty")
	}
	if t.Source() == SourceMCP && !strings.HasPrefix(n, MCPNamePrefix) {
		return fmt.Errorf("mcp-sourced tool %q must use prefix %q", n, MCPNamePrefix)
	}
	if t.Source() != SourceMCP && strings.HasPrefix(n, MCPNamePrefix) {
		return fmt.Errorf("non-mcp tool %q must not use reserved prefix %q", n, MCPNamePrefix)
	}
	if _, exists := r.data[n]; exists {
		return fmt.Errorf("tool %q already registered", n)
	}
	r.data[n] = t
	return nil
}

// Get returns the tool registered under name, or ok=false.
func (r *Registry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.data[name]
	return t, ok
}

// List returns all registered tools in a stable order: built-in tools sorted by
// name first, then MCP tools sorted by name (already prefix-namespaced).
func (r *Registry) List() []Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	tools := make([]Tool, 0, len(r.data))
	for _, t := range r.data {
		tools = append(tools, t)
	}
	sort.Slice(tools, func(i, j int) bool {
		si, sj := tools[i].Source(), tools[j].Source()
		if si != sj {
			// builtins before mcp
			return si == SourceBuiltin
		}
		return tools[i].Name() < tools[j].Name()
	})
	return tools
}

// Available returns List() filtered by t.Available() == true.
func (r *Registry) Available() []Tool {
	all := r.List()
	out := all[:0]
	for _, t := range all {
		if t.Available() {
			out = append(out, t)
		}
	}
	return out
}

// RemoveByPrefix drops all registered tools whose Name starts with prefix.
// Used when an MCP server disconnects and we want to replace its tool set
// wholesale.
func (r *Registry) RemoveByPrefix(prefix string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for n := range r.data {
		if strings.HasPrefix(n, prefix) {
			delete(r.data, n)
		}
	}
}
