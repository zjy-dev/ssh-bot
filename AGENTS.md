# ssh-bot (feishu-agent-bot) — Agent Guide

## Repo status

- **Pre-implementation.** No Go source, no `go.mod`, no `cmd/` or `internal/` yet.
  The single source of truth is `feishu-agent-bot-plan.md` (Chinese). Read it before writing any code — it contains decided architecture, directory layout, interfaces, and milestone order.
- Scaffolding present: Spec-Kit (`.specify/`) + OpenCode commands (`.opencode/command/`).
- Git: single branch `master`, one initial commit. `.specify/` scripts and extension config are currently uncommitted — do not blow them away.

## Target stack (from plan)

Go backend bot for Feishu (Lark). High-level pieces a new agent must not re-invent:

- **Language:** Go 1.22+.
- **LLM abstraction:** `github.com/cloudwego/eino` + `eino-ext` — use ONLY its `ChatModel` / `schema.Message` / streaming. **Do NOT** pull in eino's ADK, `compose`, or `flow` packages. Agent loop is hand-rolled in `internal/agent`.
- **Agent loop:** custom; defined in plan §4. Tool-call model mirrors Anthropic/OpenAI structure, unified via our own `StreamEvent` enum (`thinking_delta` / `text_delta` / `tool_call_*` / `message_end` / `error`). `MaxSteps = 12`.
- **MCP:** `github.com/mark3labs/mcp-go` (stdio + streamable HTTP + SSE). MCP tools are adapted into the local `tool.Tool` interface; **tool names are namespaced `mcp__<server>__<name>`** to avoid collisions with builtins.
- **Feishu SDK:** `github.com/larksuite/oapi-sdk-go/v3` in **long-connection mode** (`larkws.NewClient`). No public HTTP server required.
- **Session store:** Redis (`github.com/redis/go-redis/v9`), JSON values, 24h TTL.
  - **Session key shape:** `p2p:<open_id>` for DMs, `group:<chat_id>:<open_id>` for groups — per-user isolation inside a group is a deliberate choice, not a bug.
  - Per-key lock via `SET NX EX 60s`; concurrent messages from same user must be rejected, not queued.
- **Config:** `viper` (YAML + env interpolation). See plan §10 for full schema.
- **Logging:** stdlib `log/slog` with per-request `trace_id`.

### Version policy (user directive)

> "各工具链和库版本采用最新版本" — **always pin to latest stable** of every listed dependency and of Go itself. The version numbers written inside `feishu-agent-bot-plan.md` §14 (e.g. `eino v0.8.x`) are stale guidance; verify current latest before adding to `go.mod`. `go-readability`, `viper`, `go-redis/v9`, `oapi-sdk-go/v3`, `mcp-go`, `eino`, `eino-ext`, `testify` all: use latest.

## Non-obvious rules from the plan

- **Streaming card updates are rate-limited.** Renderer must batch via a 250 ms ticker goroutine and `PATCH im/v1/messages/:id` with the full card body each flush. Naive per-chunk updates will be throttled.
- **Thinking vs text:** Claude `reasoning_content` and DeepSeek `reasoning_content` both map to `EventThinkingDelta`. When first `text_delta` arrives, collapse/clear the thinking region of the card.
- **Commands bypass the LLM** — `/clear`, `/help`, `/model <name>`, `/tools`, `/whoami` are intercepted in `internal/lark/handler.go` before the agent loop. Do not route them through the model.
- **Skills are explicitly out of scope** for this iteration. Do not add a `skill` package.
- **Tool errors are fed back to the model** as tool results (with `IsError: true`), never surfaced as loop errors.
- **Feishu doc tools require `user_access_token` OAuth** (not bot token) — this is unresolved in the plan (§13). Flag it if implementing M4 tools.
- **Long messages:** Feishu markdown caps ~30 KB per message; split into threaded replies, don't silently truncate.

## Workflow: Spec-Kit is the intended path

This repo is driven by Spec-Kit commands under `.opencode/command/` (prefix `speckit.*`). Canonical order:

```
/speckit.constitution  → /speckit.specify  → /speckit.clarify
                       → /speckit.plan     → /speckit.tasks
                       → /speckit.implement
```

- `.specify/extensions.yml` auto-triggers `speckit.git.*` hooks before/after each phase (feature-branch creation, auto-commits). Expect the workflow to want to create branches and commits — don't disable hooks unless the user asks.
- `.specify/memory/constitution.md` is still the placeholder template — fill via `/speckit.constitution` before non-trivial planning work.
- Feature branches are numbered sequentially (`init-options.json` → `"branch_numbering": "sequential"`).
- `.specify/scripts/bash/update-agent-context.sh` auto-rewrites this very file (`AGENTS.md`) from `.specify/templates/agent-file-template.md` whenever a new `plan.md` appears. **Any hand-written guidance must live between the `<!-- MANUAL ADDITIONS START -->` and `<!-- MANUAL ADDITIONS END -->` markers below**, otherwise it will be clobbered on next run.

## When no plan.md exists yet

Running `.specify/scripts/bash/update-agent-context.sh` will fail with "No plan.md found" — this is expected until a feature has been `/speckit.plan`'d. Don't try to fix the script; create a feature first.

## Commands (once Go code lands)

Plan calls for a `Makefile`, but it does not yet exist. Until it does, the stock commands will be:

```
go build ./cmd/bot
go test ./...
go vet ./...
gofmt -l .        # must return nothing
```

Add a single-test shortcut as `go test ./internal/<pkg> -run TestName -v` — do not rely on IDE harnesses.

## File-tree landmarks

- `feishu-agent-bot-plan.md` — **read first.** Sections §1–§14 cover architecture, interfaces, config schema, milestones (M1–M7), risks.
- `.opencode/command/speckit.*.md` — slash-command prompts; do not edit casually, they are upstream-provided.
- `.specify/scripts/bash/*.sh` — Spec-Kit automation (feature creation, plan setup, agent-context update). Already locally modified vs upstream; preserve that state.
- `.specify/extensions/git/` — git hook commands pulled in by `extensions.yml`.

<!-- MANUAL ADDITIONS START -->
<!-- MANUAL ADDITIONS END -->

## Active Technologies
- Go 1.26.2 (latest stable per project policy) (001-feishu-agent-bot)
- Redis ≥ 7 (sessions, per-user locks, encrypted OAuth tokens); no RDBMS. (001-feishu-agent-bot)

## Recent Changes
- 001-feishu-agent-bot: Added Go 1.26.2 (latest stable per project policy)
