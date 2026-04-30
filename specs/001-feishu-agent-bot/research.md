# Research Log: 飞书 AI 机器人 (001-feishu-agent-bot)

**Date**: 2026-04-30
**Purpose**: Resolve NEEDS CLARIFICATION items from Technical Context before Phase 1 design.
**Inputs**: spec.md, feishu-agent-bot-plan.md (技术方案). The plan document pre-dates this research; **where this file and the plan disagree, this file wins**.

---

## 0. Version pins (user directive: always latest stable)

All versions verified via the authoritative release/releases page on 2026-04-30.

| Component | Pin | Source |
|---|---|---|
| Go toolchain | **go1.26.2** | https://go.dev/dl/ |
| github.com/cloudwego/eino | **v0.8.13** (stable; v0.9.0-alpha.18 avoided) | https://github.com/cloudwego/eino/releases |
| github.com/cloudwego/eino-ext | **per-subpackage** (see note below) | https://github.com/cloudwego/eino-ext/releases |
| github.com/mark3labs/mcp-go | **v0.49.0** | https://github.com/mark3labs/mcp-go/releases |
| github.com/larksuite/oapi-sdk-go | **v3.6.1** via `/v3` import path (see Decision D1 — corrected) | https://github.com/larksuite/oapi-sdk-go/releases |
| github.com/redis/go-redis/v9 | **v9.19.0** | https://github.com/redis/go-redis/releases |
| github.com/spf13/viper | **v1.21.0** | https://github.com/spf13/viper/releases |
| github.com/stretchr/testify | **v1.11.1** | https://github.com/stretchr/testify/releases |
| go-readability | **see Decision D2** (original archived) | https://github.com/go-shiori/go-readability |

### Note on eino-ext sub-packaging

`cloudwego/eino-ext` uses **per-sub-path tags** (e.g. `components/model/deepseek/v0.1.5`, `components/model/claude/...`, `libs/acl/openai/v0.1.17`). There is no repo-wide tag. The build will depend on several independent sub-module versions; `go.mod` must reference each path and `go get` will resolve each to its own latest. Concretely we expect to import at minimum:

```
github.com/cloudwego/eino-ext/components/model/claude
github.com/cloudwego/eino-ext/components/model/openai
github.com/cloudwego/eino-ext/components/model/deepseek
```
Latest tag per sub-path to be captured in `go.mod` at first `go get` time.

---

## Decision D1 — Feishu SDK import path

**Decision**: Use `github.com/larksuite/oapi-sdk-go/v3` at **v3.6.1** (verified during T003 `go get`).

**Rationale**: v3 is the actively maintained major version. An earlier pass of research mistakenly concluded v3 didn't exist on the public repo — that was wrong; `go get` resolves `v3@latest` to v3.6.1 and the full service surface (`lark.Client.Im.Message.Patch/Create/Reply`, `ws.NewClient(... WithEventHandler)`, `dispatcher.NewEventDispatcher().OnP2MessageReceiveV1`) is available. Kept this note so future maintainers don't re-derive the wrong answer from release-page scanning.

**Alternatives considered**:
- Pin to a `master` commit SHA — rejected: v3.6.1 is the published latest.

**Downstream impact**: All import paths in `internal/lark/**.go` use `/v3/...`. Build verified clean with `go build ./...` in this session.

---

## Decision D2 — Web-article extraction library

**Decision**: Use `codeberg.org/readeck/go-readability/v2` (maintained fork). The original `github.com/go-shiori/go-readability` is archived read-only (2025-12-30) and its own deprecation notice points to this fork.

**Rationale**: The upstream points users to this fork explicitly. Active maintenance, tagged releases.

**Alternatives considered**:
- Pin archived original at last commit SHA — **rejected**: contradicts "always latest stable" policy and forgoes ongoing bug fixes.
- Call an external readability service — rejected: adds latency + external dependency + cost.

**Action**: Pinned at `@latest` during T005 (v2.1.1 at time of implementation).

---

## Decision D3 — OAuth callback architecture

**Context**: Spec FR-045 through FR-048 mandate per-user OAuth (`user_access_token`) for Feishu document tools. Feishu's OAuth v2 flow is standard RFC 6749 `authorization_code` + refresh. The `redirect_uri` **must be pre-registered** in the Developer Console and must be reachable from the user's browser via HTTPS.

**Decision**: The bot process **runs a minimal HTTP server** alongside the long-connection ws client, serving **only two paths**:

1. `GET /oauth/start?state=<sessionKey>` → redirects user to `https://accounts.feishu.cn/open-apis/authen/v1/authorize?...` (note: `accounts.feishu.cn` host, not `open.feishu.cn`).
2. `GET /oauth/callback?code=<code>&state=<state>` → exchanges code at `https://open.feishu.cn/open-apis/authen/v2/oauth/token`, stores tokens encrypted in Redis, sends a Feishu message to the user confirming "授权成功，请回到聊天继续提问".

**Public exposure**: The HTTP server must be reachable from the public internet for Feishu users' browsers to redirect. Recommended deployment: TLS-terminating reverse proxy (cloud LB or Caddy) in front of the bot binary; bot binds to `127.0.0.1:<port>`.

**This supersedes the plan doc's claim** that "long-connection 模式 … 免公网 IP". With the OAuth-B path chosen, a public HTTPS endpoint **is** required for callbacks. Bot-to-Feishu traffic still uses long-connection ws (no inbound event-subscription endpoint needed), but the OAuth callback is a separate, narrow attack surface.

**Rationale**: This is the only documented Feishu OAuth pattern. Device-flow alternatives are unconfirmed in public docs; investigation timeboxed.

**Alternatives considered**:
- Hosted callback gateway relaying via back-channel — rejected: extra moving part for minimal benefit.
- Device flow (RFC 8628) via `authen/v2/oauth/device_authorization` — **deferred**: test empirically during M4; if it works, revisit and potentially replace callback server.

---

## Decision D4 — Card streaming strategy

**Decision**: Implement **PATCH-based full-card replacement** with a 250 ms ticker in M3 (compatible with plan doc). Simultaneously, **spike** Feishu's newer "streaming card update" API (visible in the docs tree at `/document/uAjLw4CM/ukTMukTMukTM/feishu-cards/streaming-update-OpenAPI`, currently behind auth wall) during M3 and, if available and stable, swap to it as an optimization before M7.

**Rate limit**: documented **5 QPS per single message** on `PATCH /open-apis/im/v1/messages/:message_id`. 250 ms ticker provides ~20% headroom. If `230020` (frequency limit) errors appear during load testing, **fall back** to 500 ms interval.

**Card size cap**: **30 KB per card**. FR-034 (auto-split long bodies) will be enforced at the renderer layer, not relied on the model to self-truncate.

**Required card config**: both the initial send and each PATCH must set `"config": { "update_multi": true }`, else update endpoint rejects.

**Alternatives considered**:
- Send multiple separate messages instead of patching one — rejected: plan UX goal is a single streaming card.
- Rely solely on the new streaming API — rejected: documentation not publicly retrievable; risk of moving target.

---

## Decision D5 — LLM reasoning/thinking field mapping

**Decision**: In `internal/llm/eino_adapter.go`, read **`schema.Message.ReasoningContent`** (top-level field, native to eino `schema`) as the source of thinking deltas. Branch per stream chunk:

```
if chunk.ReasoningContent != "" → emit EventThinkingDelta
if chunk.Content != ""          → emit EventTextDelta
```

**Rationale**: Verified by reading eino-ext source — both Claude and DeepSeek adapters populate this top-level field uniformly. Using `Extra[_eino_claude_thinking]` (the per-provider key) works too but is provider-coupled.

**Thinking signature replay**: Claude's thinking-block signature (needed for strict multi-turn reasoning replay) is **not** exposed via `ReasoningContent`; it lives in `Extra[_eino_claude_thinking_signature]` and the getter is package-private in eino-ext. **Accepted limitation for v1**: we will not replay thinking signatures on follow-up turns, which may degrade Claude extended-thinking quality slightly for long multi-turn chains. Flag for potential upstream PR in M5+.

**Alternatives considered**:
- Call provider REST APIs directly and skip eino — rejected: loses multi-provider abstraction and requires us to re-implement tool-call streaming per provider.

---

## Decision D6 — Session store and per-user locking

**Decision**: As per plan §9. Redis 7+ (we pin go-redis/v9 client at v9.19.0); keys `bot:sess:<sessionKey>`; JSON values; 24 h sliding TTL (re-set on every write). Per-user advisory lock via `SET bot:lock:<sessionKey> <trace_id> NX EX 60`. If acquire fails, reply "上一条还在处理中" and drop; do **not** enqueue.

**Rationale**: Matches FR-012, FR-072. 60 s lock TTL > typical agent turn bound (12 steps × 30 s per-tool timeout is a hard upper; typical is <10 s), protecting against bot crashes leaving stale locks.

**Alternatives considered**:
- Distributed locking library (`bsm/redislock`, `go-redsync/redsync`) — deferred; stdlib `SET NX EX` is sufficient for single-key advisory locks.
- In-process `sync.Map[key]*sync.Mutex` — rejected for multi-instance deployments (Decision D7).

---

## Decision D7 — Deployment topology

**Decision**: **Single-binary, horizontally-scalable**. Each instance runs: long-connection ws client + OAuth HTTP server + agent loop workers. State is in Redis (session + per-user lock + OAuth tokens). MCP stdio subprocesses are per-instance (not shared); each instance initializes its own MCP client set at startup.

**Rationale**: Redis-backed locks make multi-instance safe. Feishu long-connection events are dispatched per-socket by Feishu's side (no "stampede" when N instances connect — each gets a subset, documented by the SDK). Allows zero-downtime rolling deploys.

**Open items deferred to M7**:
- How Feishu's long-connection event dispatch behaves with multiple ws clients for the same app: the SDK supports this, but confirm that duplicate event delivery does not occur (empirical).
- Graceful shutdown of MCP stdio subprocesses on SIGTERM.

**Alternatives considered**:
- Single-instance only — rejected: blocks HA.
- Leader-election with only one active ws client — rejected: adds coordination complexity.

---

## Decision D8 — Initial LLM provider set for v1

**Decision**: v1 ships with **Claude** (via `eino-ext/components/model/claude`, Anthropic API) as the default provider and **OpenAI** (GPT-4-class model via `eino-ext/components/model/openai`) as the second provider exposed via `/model`. DeepSeek is deferred to v1.1.

**Rationale**: Spec FR-050/FR-051 require multi-provider support, not specifically any provider. Claude's extended-thinking integration is the primary showcase of the streaming UX. OpenAI is the most commonly configured fallback and validates that our `Provider` abstraction is not Claude-specific.

**Alternatives considered**:
- Ship only one provider in v1 — rejected: multi-provider is an explicit requirement (FR-050) and is easy once the eino adapter layer works for one.
- Ship three from day one — deferred: each addition is independent, easy to add post-v1.

---

## Decision D9 — Max tool-call loop steps

**Decision**: `MaxSteps = 12` per turn (matches plan §4). Per-tool timeout: **30 s** (matches plan §4). On hitting `MaxSteps`, the agent returns an error that the renderer shows as `❌ 已达到推理步数上限（12 步），请拆解问题后重试`.

**Rationale**: Empirically balances legitimate multi-step tool use (research tasks often chain 3–5 tools) with runaway-prevention. Tunable via config; no compile-time constant.

---

## Decision D10 — Tool input schema representation

**Decision**: Our internal `tool.Tool.InputSchema() json.RawMessage` returns **JSON Schema draft-07** (as per plan §7). The eino adapter converts it to `schema.ToolInfo` at dispatch time. MCP tools' native schema is already JSON Schema, pass-through.

**Rationale**: draft-07 is what every LLM provider publicly documents as their `tools[].parameters` format. No translation required for OpenAI/Claude beyond structural wrapping.

---

## Open questions (non-blocking — to handle during implementation)

| # | Question | Blocking phase? | Resolution plan |
|---|---|---|---|
| O1 | Exact scope strings for "search user-visible docs" (`search:*` vs legacy `suite:*`) | M4 | Hit endpoint, read `permission_violations` from response |
| O2 | Whether Feishu's newer streaming-card API is stable | M3 spike | Log in, pull the auth-walled doc, prototype a branch |
| O3 | Feishu device-flow OAuth availability (`authen/v2/oauth/device_authorization`) | Post-v1 | Empirical probe; if works, removes need for public callback HTTP |
| O4 | Duplicate-event behavior of multiple concurrent long-connection ws clients | M7 | Deploy 2 instances against staging app, verify message deduplication |
| O5 | Graceful MCP stdio subprocess shutdown on SIGTERM (and crash-restart policy) | M7 | Implement `supervisor` goroutine; exponential backoff on restart |

These are tracked in `tasks.md` (generated by `/speckit.tasks`) and surface in the relevant milestone.

---

## Summary

All Technical Context NEEDS CLARIFICATION items are resolved. Phase 1 design can proceed.
