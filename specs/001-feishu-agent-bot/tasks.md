---
description: "Task list for 001-feishu-agent-bot implementation"
---

# Tasks: 飞书 AI 机器人 (Feishu Agent Bot)

**Input**: Design documents from `/specs/001-feishu-agent-bot/`
**Prerequisites**: plan.md, spec.md, research.md, data-model.md, contracts/, quickstart.md
**Feature branch**: `001-feishu-agent-bot`

**Tests**: Contract tests are included because `contracts/` defines frozen interface shapes that must not regress. Broader unit/integration suites are scoped to the Polish phase.

**Organization**: Tasks are grouped by user story. Shared infrastructure lives in Phase 2 (Foundational). User Story 1 (group @) and User Story 2 (DM) share almost all code; their distinct parsing paths are split across Phase 3 (US1) and Phase 4 (US2) but all shared pieces are in Phase 2.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different files, no dependencies)
- **[Story]**: Which user story this task belongs to (US1, US2, US3, US4, US5)
- File paths assume the single-project Go layout from plan.md (`cmd/`, `internal/`, `test/`, `configs/`)

---

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: Initialize the Go module, pin dependencies, set up tooling.

- [x] T001 Create repo layout per plan.md — `cmd/bot/`, `internal/{agent,llm,tool,tool/builtin,mcp,session,oauth,lark,render,config,log}/`, `configs/`, `test/{integration,fixtures}/` (directories only, no .go files yet)
- [x] T002 Initialize Go module: `go mod init github.com/<org>/ssh-bot` and commit an empty `go.mod` header setting `go 1.26.2`
- [x] T003 Add core dependencies via `go get` at exact pins from research.md §0: eino v0.8.13, mcp-go v0.49.0, oapi-sdk-go/v3 v3.6.1, go-redis/v9 v9.19.0, viper v1.21.0, testify v1.11.1. Run `go mod tidy`.
- [x] T004 [P] Add eino-ext per-subpackage deps at their latest tags: `components/model/claude`, `components/model/openai`, `components/model/deepseek`. Record the resolved versions in `go.mod`.
- [x] T005 [P] Pin `github.com/go-shiori/go-readability` by explicit commit SHA (archived repo — per research.md D2). Verify with `go list -m`.
- [x] T006 [P] Create `Makefile` with targets `build`, `test`, `test-integration`, `vet`, `fmt-check` (matches AGENTS.md "Commands" section). Commands: `go build ./cmd/bot`, `go test ./...`, `go test -tags=integration ./...`, `go vet ./...`, `gofmt -l . | tee /dev/stderr | wc -l | grep -q '^0$'`
- [x] T007 [P] Create `configs/config.example.yaml` matching the schema in quickstart.md §3. Add `configs/config.yaml` to `.gitignore`.
- [x] T008 [P] Create `.env.example` listing every env var from quickstart.md §4 (with empty placeholders). Add `.env` to `.gitignore`.
- [x] T009 [P] Create minimal `Dockerfile` (multi-stage: golang:1.26.2 build, distroless runtime) and `.dockerignore`. Full Dockerfile polishing is Phase N.
- [x] T010 [P] Create `README.md` with one-paragraph project description and a link to `specs/001-feishu-agent-bot/quickstart.md`.

**Checkpoint**: `go build ./cmd/bot` succeeds with a trivial `main()` (to be added in T014).

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: Shared infrastructure every user story depends on: config, logging, Redis, session/lock, LLM provider abstraction, tool registry, renderer skeleton, Feishu handler skeleton, command interception, agent loop, OAuth server skeleton. Without Phase 2, no user story can run end-to-end.

**⚠️ CRITICAL**: No user story work can begin until this phase is complete.

### Config & logging

- [x] T011 [P] Implement `internal/config/config.go` — viper-based loader, env interpolation (`${VAR}`), struct per `contracts/go-interfaces.md#config`. Unit test a fixture YAML.
- [x] T012 [P] Implement `internal/log/log.go` — `slog`-backed logger factory, `trace_id` propagation via `context.Context`, helper `WithTrace(ctx, id) context.Context` and `FromContext(ctx) *slog.Logger` (FR-071).

### Entry point skeleton

- [x] T013 Implement `cmd/bot/main.go` — reads config, sets up logger, wires (in order) redis → oauth store → llm providers → tool registry → mcp manager → render → lark handler → agent. Graceful shutdown on SIGINT/SIGTERM. Dependencies block each other per startup order in quickstart.md §5.

### Session + lock (Redis)

- [x] T014 Implement `internal/session/store.go` — `Store` interface per `contracts/go-interfaces.md#internal-session`
- [x] T015 Implement `internal/session/redis.go` — Redis `Store` implementation: keys `bot:sess:<key>`, JSON values, 24h TTL via `EXPIRE` on every write (data-model.md C2). Use `go-redis/v9` v9.19.0.
- [x] T016 [P] Implement `internal/session/memory.go` — in-memory `Store` for unit tests (no TTL; tests use fake clock).
- [x] T017 Implement `internal/session/lock.go` — `Locker` via `SET NX EX 60` + Lua `GET + DEL if match` for `Release` (data-model.md C3). Token from `crypto/rand`.
- [x] T018 [P] Contract test `test/contract/session_test.go` — exercise `Store.Get` returns `(nil, nil)` for missing keys (not error); `Save` resets TTL; `Delete` is idempotent; `Locker.TryAcquire` returns `(token, true, nil)` first time, `("", false, nil)` second time while held, succeeds after `Release`. Use `alicebob/miniredis/v2`.

### LLM provider abstraction

- [x] T019 Implement `internal/llm/types.go` — `Role`, `Message`, `ToolCall`, `ChatRequest`, `ToolSpec`, `StreamEvent`, `StreamEventType`, `Usage`, `Provider` interface per `contracts/go-interfaces.md#internal-llm`.
- [x] T020 Implement `internal/llm/provider.go` — `Provider` factory taking a `ModelProfile` and returning the right concrete provider. Disabled profiles (missing API key env) return a sentinel `nil` provider and log a warning; not an error.
- [x] T021 Implement `internal/llm/eino_adapter.go` — wraps an `eino.ChatModel` into `Provider`. Maps `schema.Message.ReasoningContent != ""` → `EventThinkingDelta`, `.Content != ""` → `EventTextDelta`, tool-call deltas → `EventToolCall*`. Closes the event channel after exactly one of `EventMessageEnd`/`EventError` (contract invariant from go-interfaces.md).
- [x] T022 [P] Wire `internal/llm/providers/claude.go` — constructs `eino-ext/components/model/claude` ChatModel. Honor `enable_thinking`.
- [x] T023 [P] Wire `internal/llm/providers/openai.go` — constructs `eino-ext/components/model/openai` ChatModel.
- [x] T024 [P] Contract test `test/contract/llm_stream_test.go` — uses a fake `eino.ChatModel` that emits a scripted sequence; asserts `EventThinkingDelta` precedes any `EventTextDelta`, tool-call `Start`/`Args*`/`End` ordering holds, channel closes exactly once.

### Tool registry + builtins stubs

- [x] T025 Implement `internal/tool/tool.go` — `Tool`, `Result`, `Source` per contracts.
- [x] T026 Implement `internal/tool/registry.go` — `Register` rejects duplicates; `List` ordering (builtins by name, then MCP grouped by server); `Available` filter. Unit test in-package.
- [x] T027 [P] Implement `internal/tool/builtin/datetime.go` — trivial tool used as the loop's "I'm alive" smoke tool in M2. Schema per `contracts/tools.md#datetime`.
- [x] T028 [P] Contract test `test/contract/tool_registry_test.go` — `Register` duplicate returns error; `List` ordering is deterministic; `Available` respects the tool's `Available()` return value.

### Agent loop

- [x] T029 Implement `internal/agent/types.go` — internal `Message`/`ToolCall` aliases that mirror `llm.*`. Import boundary kept narrow.
- [x] T030 Implement `internal/agent/context.go` — `buildRequest(sess, tools) llm.ChatRequest`. System prompt composed from fixed preamble + tool catalog. Context trimming: keep last 40 messages (C1 from data-model).
- [x] T031 Implement `internal/agent/loop.go` — `Agent.Run(ctx, sess, input, emit)` per plan §4. Honors `MaxSteps=12` (D9), 30s per-tool timeout, `recover` per tool goroutine, feeds tool errors back as `role=tool IsError=true` messages (FR-042). Emits synthetic `EventToolCallStart/End` events (go-interfaces.md invariant).
- [x] T032 [P] Contract test `test/contract/agent_loop_test.go` — mock `Provider` + fake tool registry; verify (a) loop exits on first `EventMessageEnd` with zero tool calls, (b) tool result appended with correct `tool_call_id`, (c) `MaxSteps` returns a loop-terminated error, (d) tool panic is caught and becomes `is_error=true` tool message rather than propagating.

### Renderer skeleton (no Feishu calls yet)

- [x] T033 Implement `internal/render/state.go` — state machine `idle → thinking → text → tool_executing → done` per `contracts/feishu-card.md`. Holds accumulating buffers.
- [x] T034 Implement `internal/render/renderer.go` — `Renderer` interface per contracts. 250 ms ticker goroutine. Calls an injected `Sender` for PATCH (injection lets T047 substitute the real Feishu sender while tests use a fake).
- [x] T035 [P] Contract test `test/contract/renderer_test.go` — scripted event sequence; assert minimum 250 ms between flushes (use fake clock), force-flush on `EventMessageEnd` and `EventToolCallEnd`, thinking region collapses on first `EventTextDelta`, final card JSON ≤ 30 KB (serialize and measure).

### Lark handler skeleton

- [x] T036 Implement `internal/lark/parser.go` — given a raw message event: extract `ChatID`, `ChatType`, `SenderOpenID`, `MessageID`, determine `MentionedBot` (by matching `LARK_BOT_OPEN_ID` in mentions), strip @mention prefix from text. Unit test with fixture events in `test/fixtures/lark/`.
- [x] T037 Implement `internal/lark/card.go` — JSON builders for: initial card (thinking placeholder), thinking-only card, text card, tool-list card, terminal error card. Every card includes `config.update_multi=true` (contracts/feishu-card.md constraint).
- [x] T038 Implement `internal/lark/sender.go` — thin wrapper around `larksuite/oapi-sdk-go/v3`: `SendInitialCard(chatID) (messageID, error)`, `PatchCard(messageID, cardJSON) error`. On 230020 error, returns a typed `ErrRateLimited` so the renderer can back off. Also `SendMessage(chatID, text)` for command responses.
- [x] T039 Implement `internal/lark/handler.go` — `Handle(ctx, ev)` entry point: acquire per-user lock, if fail → reply "上一条还在处理中", else: check for commands (short-circuit, FR-021), else: spawn agent + renderer, release lock at end. Command dispatcher lives here; actual command implementations are Phase 5 US3 tasks.

### OAuth server skeleton (HTTP + state)

- [x] T040 Implement `internal/oauth/state.go` — HMAC-SHA256 `state` encoder/decoder per `contracts/http-endpoints.md#state-parameter-construction`. Load `OAUTH_STATE_KEY` from env.
- [x] T041 Implement `internal/oauth/tokens.go` — AES-GCM encrypt/decrypt with `OAUTH_ENCRYPTION_KEY` (fail-closed on missing key at startup, FR-047).
- [x] T042 Implement `internal/oauth/store.go` — Redis CRUD for `UserOAuthCredential`: key `bot:oauth:<open_id>`, value = encrypted JSON, `EXPIREAT refresh_expires_at`. Encryption/decryption gated through T041.
- [x] T043 Implement `internal/oauth/server.go` — HTTP mux with `/healthz`, `/oauth/start`, `/oauth/callback`. Callback exchanges code at `https://open.feishu.cn/open-apis/authen/v2/oauth/token`, stores credential, sends a confirming Feishu message to the user via injected `Sender`. Token-bucket rate-limit (20/min/IP) per contract.
- [x] T044 [P] Contract test `test/contract/oauth_http_test.go` — `/oauth/start` redirects to accounts.feishu.cn with correct query shape; `/oauth/callback` rejects invalid state with 400; on mocked token-endpoint success stores an encrypted record and returns 200. Does NOT hit real Feishu.

### MCP manager skeleton (no servers connected yet — that's US5)

- [x] T045 Implement `internal/mcp/manager.go`, `internal/mcp/client.go`, `internal/mcp/adapter.go` — empty `Manager` that satisfies the contract but does nothing when given an empty server list. Named tool prefix `mcp__<server>__<name>` enforced at adapter boundary (FR-061).

### Wire everything in main

- [x] T046 Update `cmd/bot/main.go` to fully wire all Phase 2 components together. At the end of main, print the startup log sequence from quickstart.md §5 — the bot should be able to receive @ messages and reply with a trivial "echo" via a default-disabled LLM path. (Proves the plumbing works.)

**Checkpoint**: Bot starts, connects to Feishu ws, connects to Redis, responds to @ messages with a placeholder card that never advances past thinking. OAuth HTTP server is reachable. All contract tests pass. No user story is functionally complete yet.

---

## Phase 3: User Story 1 — 群内 @ 机器人获得流式 AI 回答 (Priority: P1) 🎯 MVP

**Goal**: Member @-mentions bot in a group; bot streams a real AI answer via card updates. Thinking/text/tool-call regions all work. Concurrent @-messages from different users in the same group are isolated per data-model C1.

**Independent Test** (spec US1): Add bot to a test group. User A sends `@bot 介绍一下你自己` — card streams content within 3 s and finishes within ~10 s for a non-tool answer. User B @ mentions in the same group while A is active — both answers proceed independently without cross-talk.

### Contract tests for User Story 1

- [x] T047 [P] [US1] Contract test `test/contract/us1_group_flow_test.go` — simulate full flow with mocked Feishu sender + mocked LLM: group message → lock acquired → initial card sent → events stream → final card carries answer. Use agent + real renderer, fake providers.

### Implementation for User Story 1

- [x] T048 [US1] Extend `internal/lark/parser.go` to correctly identify group @-mentions: match `bot_open_id` inside `event.message.mentions`, reject messages where bot is not @'d (FR-001). Edit existing file from T036.
- [x] T049 [US1] Extend `internal/lark/handler.go` to: construct session key as `group:<chat_id>:<open_id>` when `ChatType == "group"` (data-model C1, FR-010), load/create session, invoke `agent.Run`, subscribe `render.Feed` to the emitted events, persist the final session on success. Edit from T039.
- [x] T050 [US1] Wire real LLM provider in the default config path (Claude via Phase 2 T022). Confirm `cmd/bot/main.go` registers it as default. Edit from T046.
- [x] T051 [US1] Implement message-size hard cap in `internal/session/redis.go`: on save, truncate any message `content` > 20 KB with `[… truncated <N> chars]` suffix (data-model C1 validation).
- [x] T052 [P] [US1] Implement long-answer split in `internal/render/renderer.go`: if final body > 25 KB (safety margin below Feishu's 30 KB card cap), split into threaded replies via `Sender.ReplyInThread(parentMessageID, bodySegment)` — add that method to `internal/lark/sender.go` too. Covers FR-034 + edge case "超长回答". (Edit T034 and T038.)
- [x] T053 [P] [US1] Integration test `test/integration/us1_group_claude_test.go` (build tag `integration`) — requires `ANTHROPIC_API_KEY` and real Redis; sends a canned group event through handler, asserts final card carries non-empty text and includes "trace:" footer.

**Checkpoint**: US1 independently demoable. @-mention a bot in a group, get a streamed Claude answer. US2/US3/US4/US5 not yet functional.

---

## Phase 4: User Story 2 — 私聊对话 (Priority: P1, parallel with US1)

**Goal**: Same streaming experience in 1-1 chats, no @ required.

**Independent Test**: Open 1-1 with bot, send "你好" — get streamed answer. Follow-up question remembers context. No @ prefix required (FR-002-ish, spec US2).

### Contract tests for User Story 2

- [ ] T054 [P] [US2] Contract test `test/contract/us2_p2p_flow_test.go` — mirror T047 but with `ChatType=p2p` and a message with no @-mention.

### Implementation for User Story 2

- [ ] T055 [US2] Extend `internal/lark/handler.go`: for `ChatType == "p2p"`, skip the `MentionedBot` gate; always accept. Session key = `p2p:<open_id>` (data-model C1). Edit T049.
- [ ] T056 [P] [US2] Integration test `test/integration/us2_p2p_test.go` — sends a p2p event, asserts the same success criteria as T053.

**Checkpoint**: US1 + US2 both functional. Basic AI chat in groups and DMs works for every team member.

---

## Phase 5: User Story 3 — 会话控制与切换 (Priority: P2)

**Goal**: `/clear`, `/help`, `/model <name>`, `/tools`, `/whoami` short-circuit before the LLM loop, return within 1 s, never consume LLM quota (FR-020..FR-023, SC-005).

**Independent Test** (spec US3): After a multi-turn chat, send `/clear`; next question must NOT reference prior content. `/model gpt` switches, and the next answer uses gpt without losing history.

### Contract tests for User Story 3

- [ ] T057 [P] [US3] Contract test `test/contract/us3_commands_test.go` — each command short-circuits without invoking the mocked LLM (assert `Provider.Stream` is called zero times). `/clear` calls `Store.Delete`. `/model invalid` returns listing. `/tools` lists all registered tools.

### Implementation for User Story 3

- [ ] T058 [US3] Implement command dispatcher `internal/lark/commands.go` with handlers for `/clear`, `/help`, `/model`, `/tools`, `/whoami`. Each returns a structured response that `handler.go` sends via `Sender.SendMessage`. Commands MUST run outside the per-user lock path (FR-021: no context contamination, no lock contention).
- [ ] T059 [US3] Wire `commands.go` into `internal/lark/handler.go` — dispatch BEFORE lock acquire, BEFORE session load. Edit T049/T055.
- [ ] T060 [P] [US3] Integration test `test/integration/us3_commands_test.go` — send each command in sequence against real Redis + a **nil-op** LLM provider, assert zero provider invocations occurred.

**Checkpoint**: Users have full session control. Still no tools.

---

## Phase 6: User Story 4 — 工具辅助的信息查询 (Priority: P2)

**Goal**: Bot uses tools when needed. Web search, URL fetch, Feishu doc read/search (Q1=B: per-user OAuth), datetime. All calls visible on card; tool errors don't abort the turn.

**Independent Test** (spec US4): Ask "最新发布的 iOS 版本号" → card shows `web_search` call; ask "读一下 https://<feishu-doc-url>" → first time triggers OAuth flow, after auth second time succeeds.

### Contract tests for User Story 4

- [ ] T061 [P] [US4] Contract test `test/contract/us4_web_search_test.go` — schema validation: args missing `query` → `is_error=true`; valid args → mocked HTTP returns canned results → tool returns numbered markdown list per `contracts/tools.md#web_search`.
- [ ] T062 [P] [US4] Contract test `test/contract/us4_web_fetch_ssrf_test.go` — URLs with scheme `file://`, hosts resolving to `127.0.0.1`, `10.0.0.0/8`, etc. all rejected with `is_error=true` before any HTTP call (SSRF defense per `contracts/tools.md#web_fetch`).
- [ ] T063 [P] [US4] Contract test `test/contract/us4_feishu_doc_oauth_test.go` — `feishu_doc_read` when no OAuth credential for user returns `is_error=true, content` containing a `/oauth/start?state=…` URL (the signed state from T040).

### Tool implementations

- [ ] T064 [P] [US4] Implement `internal/tool/builtin/web_search.go` — Tavily client (default), schema + result per contracts/tools.md. Config reads `tools.web_search.api_key_env`.
- [ ] T065 [P] [US4] Implement `internal/tool/builtin/web_fetch.go` — HTTPS fetch + go-readability conversion. Enforce scheme allowlist, pre-fetch DNS resolution + RFC1918 check, `robots.txt` Disallow check, 10 s timeout, 5 MB body cap (contracts/tools.md#web_fetch).
- [ ] T066 [P] [US4] Implement `internal/tool/builtin/feishu_doc_read.go` — look up `UserOAuthCredential` via `internal/oauth.Store`, refresh if access_token within 60 s of expiry (per D3 + FR-047), call Feishu docx/docs API. On 403 → error per contracts. On no-credential → return OAuth-start URL.
- [ ] T067 [P] [US4] Implement `internal/tool/builtin/feishu_doc_search.go` — same auth path as T066, hits search API with `query`. Note open item O1: exact scope name to be confirmed empirically; wire the call and log `permission_violations` from error responses.
- [ ] T068 [US4] Register all five builtins (plus existing `datetime` from T027) in `cmd/bot/main.go`'s tool registry setup. Edit T046.
- [ ] T069 [US4] Token refresh path — in `internal/oauth/tokens.go`, add `RefreshIfNeeded(ctx, cred) (*UserOAuthCredential, error)` invoked by T066/T067 before use. On 4xx from refresh endpoint, delete the credential (per D3 "invalidated" state transition) and return the OAuth-start URL as a typed sentinel error.
- [ ] T070 [US4] Rate-limit `/oauth/callback` — finalize the token-bucket from T043 to 20/min/IP per contract. Add metrics counter for denial rate (Phase N-friendly).
- [ ] T071 [P] [US4] Integration test `test/integration/us4_tool_loop_test.go` — build-tag `integration`; uses a real Tavily key or `TAVILY_MOCK=1`; asserts a question triggers exactly one `web_search` call and the final card references the returned URLs.
- [ ] T072 [P] [US4] Integration test `test/integration/us4_oauth_full_flow_test.go` — simulates a user visiting `/oauth/start` (signed), callback with a Feishu-mocked token response, verifies `UserOAuthCredential` stored encrypted and `feishu_doc_read` succeeds on subsequent call.

**Checkpoint**: Bot is genuinely useful — can research on the web and read internal docs as the asking user.

---

## Phase 7: User Story 5 — 通过 MCP 扩展外部工具生态 (Priority: P3)

**Goal**: Operator-configured MCP servers are auto-loaded at startup; their tools appear in `/tools` and can be called. A failed MCP server does not affect the rest of the bot (FR-062).

**Independent Test** (spec US5): Enable a filesystem MCP server in config → restart → user asks "列一下 /tmp/shared 下有什么" → bot calls `mcp__filesystem__list_directory` and shows result.

### Contract tests for User Story 5

- [ ] T073 [P] [US5] Contract test `test/contract/us5_mcp_naming_test.go` — adapter prefixes all MCP tool names with `mcp__<server>__` and leaves input schemas unchanged (FR-061).
- [ ] T074 [P] [US5] Contract test `test/contract/us5_mcp_isolation_test.go` — with one broken MCP server config (unreachable URL) and one working, verify `Manager.LoadAll` returns nil, `Status()` reports the broken one `Connected=false LastError != ""`, and the working server's tools are registered normally (FR-062).

### Implementation for User Story 5

- [ ] T075 [US5] Flesh out `internal/mcp/client.go` with `mark3labs/mcp-go` wrappers for `Initialize` and `ListTools`.
- [ ] T076 [P] [US5] Implement `internal/mcp/stdio.go` — spawn subprocess, pipe stdio, `context.Context`-scoped. On process exit, mark server `disconnected`; optional restart policy deferred to open item O5 (M7/Polish).
- [ ] T077 [P] [US5] Implement `internal/mcp/http.go` — HTTP + SSE transports via mcp-go.
- [ ] T078 [US5] Complete `internal/mcp/adapter.go` — convert `mcp.Tool` schemas pass-through into `tool.Tool`; wire `Call` to route via the backing client; `Available()` reflects `Status().Connected`.
- [ ] T079 [US5] Complete `internal/mcp/manager.go` — `LoadAll` iterates configs, per-server init with its own timeout, aggregates into registry, never returns an error for per-server failures (logs + status instead).
- [ ] T080 [US5] Wire `Manager.Tools()` into the tool registry at startup in `cmd/bot/main.go`, with a hot-fail pattern: if Redis or LLM fail, abort; if all MCP servers fail, log and continue. Edit T046/T068.
- [ ] T081 [P] [US5] Integration test `test/integration/us5_mcp_filesystem_test.go` — spins up `npx @modelcontextprotocol/server-filesystem` against a tempdir, invokes `mcp__filesystem__list_directory` through the agent loop (real mcp-go, real subprocess). Skipped if `npx` not available.

**Checkpoint**: Operators can extend bot capabilities without code changes. All user stories functional.

---

## Phase 8: Polish & Cross-Cutting Concerns

**Purpose**: Hardening, operational readiness, and the remaining open items from research.md.

### Correctness & resilience

- [ ] T082 Implement graceful MCP subprocess teardown on SIGTERM (research.md O5). Edit `internal/mcp/stdio.go` + `cmd/bot/main.go`.
- [ ] T083 Add crash-restart policy for MCP stdio children with exponential backoff (research.md O5). 3 retries, cap 30 s, then mark permanently failed.
- [ ] T084 [P] Verify multi-instance long-connection behavior (research.md O4): deploy 2 bot replicas to staging against same Feishu app, send 10 group messages, assert each is handled exactly once (no duplicate card). Document finding in research.md as an addendum.
- [ ] T085 [P] Spike on Feishu's new Streaming Card API (research.md O2/D4): log in to open.feishu.cn, pull the `streaming-update-OpenAPI` page, prototype a branch that uses incremental content append, decide go/no-go. Outcome written up in `specs/001-feishu-agent-bot/research-addendum.md`.
- [ ] T086 [P] Empirically confirm `search:*` scopes for `feishu_doc_search` (research.md O1): run the tool with the baseline scope set, capture `permission_violations` from 99991679 responses, update config scope list. Edit `configs/config.example.yaml`.

### Observability

- [ ] T087 [P] Add structured-log fields `session_key, step, provider, model, tool_name, tool_duration_ms, tokens_in, tokens_out, stop_reason` to every agent-loop log record (plan §11, FR-071). Edit `internal/agent/loop.go`.
- [ ] T088 [P] Add `trace_id` to the card footer note (contracts/feishu-card.md#error-path-cards).

### Tests & quality

- [ ] T089 [P] Unit tests for every remaining public function under `internal/config`, `internal/log`, `internal/render/state.go` — get line coverage ≥ 70 % for the `internal/` tree.
- [ ] T090 [P] Run `go vet ./...` and `gofmt -l .` in CI; fail build on any output. Extend Makefile `fmt-check`.
- [ ] T091 Run the full quickstart.md §6 smoke test against a real staging Feishu app. Record pass/fail for each of steps 1–6.

### Docs & deploy

- [ ] T092 [P] Finalize Dockerfile (T009) — distroless runtime, non-root user, healthcheck hitting `/healthz`.
- [ ] T093 [P] Expand `README.md` with deployment notes: required env vars, reverse-proxy recipe (Caddy snippet for TLS termination in front of `/oauth/*`), Redis sizing.
- [ ] T094 Run `/speckit.constitution` (recommended in plan.md Constitution Check) to codify the load-bearing invariants (no-secrets-in-logs, tool-failures-recovered, user-data-encrypted).

---

## Dependencies & Execution Order

### Phase Dependencies

- **Setup (Phase 1)**: No dependencies — can start immediately.
- **Foundational (Phase 2)**: Depends on Setup. **BLOCKS all user stories.**
- **US1 (Phase 3) & US2 (Phase 4)**: Both depend only on Foundational. Can run in parallel (different tasks, shared-but-stable foundation files).
- **US3 (Phase 5)**: Depends on Foundational (specifically T039, T044 lark handler skeleton + session store). Independent of US1/US2 — commands dispatch before story-specific code runs.
- **US4 (Phase 6)**: Depends on Foundational. Tool implementations are independent of US1/US2/US3 code, but the end-to-end "tool actually runs during a group @" requires US1 (or US2) to demonstrate — in practice US4 can be built in parallel with US1/US2 but only shown working once either US1 or US2 has landed.
- **US5 (Phase 7)**: Depends on Foundational (mcp skeleton from T045). Independent of other stories.
- **Polish (Phase 8)**: Depends on all user stories the team wants to ship being complete.

### Within Each User Story

- Contract tests listed in a story SHOULD be written first and fail before implementation (convention, not strictly TDD-enforced here).
- Models/types → services → handlers.
- Each user story's checkpoint MUST pass before moving on.

### Parallel Opportunities

- Phase 1: T004–T010 all [P], run in parallel.
- Phase 2: T011–T012 [P]; T016 [P]; T022–T024 [P]; T027–T028 [P]; T032 [P]; T035 [P]; T044 [P]. Roughly half the foundational work parallelizable.
- Phase 3 & Phase 4 can be worked by two developers in parallel after Phase 2 completes.
- Phase 5 in parallel with Phase 6 and/or Phase 7.
- Phase 8: most tasks [P], except T091 (depends on T082/T083 landing first).

---

## Parallel Example: After Phase 2 checkpoint, launch 3 developers

```bash
# Developer A — US1 (P1 MVP)
Task T047, T048, T049, T050, T051, T052, T053

# Developer B — US2 (P1 MVP parallel)
Task T054, T055, T056

# Developer C — US3 (P2)
Task T057, T058, T059, T060
```

After US1+US2+US3 land, launch US4 and US5 in parallel.

---

## Implementation Strategy

### MVP First (US1 only)

1. Phase 1 (Setup) ~0.5 day.
2. Phase 2 (Foundational) — biggest phase; ~1 week for one dev, ~3–4 days with parallelism.
3. Phase 3 (US1) — ~2–3 days.
4. **STOP & VALIDATE**: Demo "@ the bot in a group, get an answer". Deploy to staging.

### Incremental Delivery (recommended cadence)

1. Setup + Foundational → Foundation demo.
2. + US1 → **MVP** deploy ("I can chat with it in a group").
3. + US2 → expanded MVP ("I can chat with it in DM too").
4. + US3 → "I can manage my session".
5. + US4 → "It can search the web and read internal docs". ← **biggest perceived-value jump**.
6. + US5 → "We can add more tools without code". ← extensibility.
7. Polish → production.

### Parallel Team Strategy

- 2 devs: one on Phase 2 foundation while the other scaffolds Phase 1 + reviews contracts; then split US1+US2 in parallel; then split US3+US4; then both on US5 + Polish.
- 3+ devs: same as above with a third dev owning OAuth/security bits (T040–T044, T066, T069) end-to-end.

---

## Notes

- [P] tasks = different files, no dependencies.
- [Story] label traces each task back to a user story from spec.md.
- Contract tests pin interface shapes defined in `contracts/`; do not modify contracts without a concurrent update to those tests.
- Each user story checkpoint is an independent deploy/demo milestone — never roll up multiple story deliveries into one release.
- Commit after each task or logical group; hooks in `.specify/extensions.yml` will prompt to commit between phases.
- Avoid: vague tasks, same-file collisions (esp. `internal/lark/handler.go` which several stories touch — edits are sequential), cross-story dependencies that break independence.
