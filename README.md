# ssh-bot — 飞书 AI Agent Bot

A Go backend bot for Feishu (Lark) that answers team questions in group chats (`@` mention) and DMs, with streaming responses showing reasoning, answer, and tool-call progress in a single card. Supports multiple LLM providers (Claude, OpenAI, DeepSeek), built-in tools (web search, URL fetch, Feishu doc read/search, datetime), and MCP-server-sourced tools.

## Quick start

See [specs/001-feishu-agent-bot/quickstart.md](specs/001-feishu-agent-bot/quickstart.md) for full setup, config, deployment, and smoke testing.

## Build

```sh
make build       # → bin/bot
make test        # unit + contract tests
make vet
make fmt-check
```

## Status

Feature branch `001-feishu-agent-bot`; see `specs/001-feishu-agent-bot/tasks.md` for the task-level state of the implementation.

## Further docs

- Spec / user stories → [spec.md](specs/001-feishu-agent-bot/spec.md)
- Implementation plan → [plan.md](specs/001-feishu-agent-bot/plan.md)
- Design decisions + version pins → [research.md](specs/001-feishu-agent-bot/research.md)
- Data model → [data-model.md](specs/001-feishu-agent-bot/data-model.md)
- Interface contracts → [contracts/](specs/001-feishu-agent-bot/contracts/)

## Contributing

Agent-oriented context for future work: [AGENTS.md](AGENTS.md).
