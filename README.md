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

## Required env vars

- `LARK_APP_ID`
- `LARK_APP_SECRET`
- `LARK_ENCRYPT_KEY`
- `LARK_VERIFICATION_TOKEN`
- `LARK_BOT_OPEN_ID`
- `OAUTH_ENCRYPTION_KEY`
- `OAUTH_STATE_KEY`

One configured provider key is also required, for example:

- `ANTHROPIC_API_KEY`
- `OPENAI_API_KEY`
- `DEEPSEEK_API_KEY`

Optional tool keys:

- `TAVILY_API_KEY`

## Deployment notes

- Feishu events arrive through long connection, but OAuth callback still requires a public HTTPS endpoint.
- Recommended deployment shape: run the bot privately, terminate TLS at a reverse proxy, and forward `/oauth/*` plus `/healthz` to the bot's local HTTP listener.
- Redis stores 24h sessions, per-user locks, and encrypted OAuth credentials. Team-scale deployments generally only need a small Redis instance.

## Reverse proxy example

Example Caddy config:

```caddy
bot.example.com {
  reverse_proxy /oauth/* 127.0.0.1:8080
  reverse_proxy /healthz 127.0.0.1:8080
}
```

## Container

```sh
docker build -t ssh-bot .
docker run --rm --env-file .env -p 8080:8080 ssh-bot
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
