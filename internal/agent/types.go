// Package agent implements the self-authored agent loop (plan §4).
//
// Deliberately does NOT depend on any agent framework (no eino ADK, compose,
// or flow). The loop drives a Provider, dispatches tool calls it emits, feeds
// results back, and stops on MessageEnd-with-no-tool-calls or MaxSteps.
package agent

import "github.com/anomalyco/ssh-bot/internal/llm"

// Message is a local alias for llm.Message kept separate to let the agent
// package evolve its internal shape if needed. For now it's a type alias.
type Message = llm.Message

// ToolCall is the agent-level alias.
type ToolCall = llm.ToolCall

// Role aliases.
type Role = llm.Role

const (
	RoleUser      = llm.RoleUser
	RoleAssistant = llm.RoleAssistant
	RoleTool      = llm.RoleTool
)
