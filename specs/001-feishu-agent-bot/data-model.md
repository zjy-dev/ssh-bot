# Data Model: 飞书 AI 机器人 (001-feishu-agent-bot)

**Date**: 2026-04-30
**Scope**: Entities listed in `spec.md#key-entities`, translated into storage-level and in-memory shapes. This is the **logical model**. Wire formats (JSON schemas) live in `contracts/`.

---

## Entity catalog

### 1. ConversationSession

A user's running conversation context. Scope: one per user per chat-scope.

| Field | Type | Notes |
|---|---|---|
| `key` | string | Identity key. `p2p:<open_id>` for DMs, `group:<chat_id>:<open_id>` for groups. FR-010. |
| `user_open_id` | string | Feishu open_id of the human user |
| `chat_id` | string | Feishu chat_id. Equal to user's p2p chat_id for DMs. |
| `chat_type` | enum(`p2p`,`group`) | Matches source message |
| `provider` | string | Alias into `Model Profile` registry. Default = configured default. Affected by `/model`. |
| `messages` | []Message | Ordered history, oldest→newest. See §2. |
| `created_at` | RFC3339 | Session creation time |
| `updated_at` | RFC3339 | Last activity; drives 24h TTL |
| `trace_id_last` | string | For operator debugging via `/whoami` |

**Storage**:
- Redis key: `bot:sess:<key>`
- Value: single JSON object (above)
- TTL: 24h sliding (`EXPIRE` reset on every write). FR-011.
- Enforce max `len(messages) <= 40` pre-send; oldest user/assistant pair drops first, **never** the system message (there is no persistent system message; it's composed per-turn — see §2 note).

**State transitions**:
- **create** on first message of new user (no existing key).
- **append** on every user/assistant/tool message of the loop.
- **delete** on `/clear`. Also deleted implicitly when TTL expires.
- **update provider** on `/model <name>` (mutates `provider` only, does not touch messages).

**Validation**:
- `key` format must match `^(p2p:[A-Za-z0-9_]+|group:[A-Za-z0-9_]+:[A-Za-z0-9_]+)$`.
- `len(messages)` capped at 40; trimming strategy above.
- Any single message's `content` length is capped at 20 KB pre-storage; longer bodies get truncated with suffix `[… truncated <N> chars]`. Reasoning: agent step N might retrieve a 500 KB doc; we must not persist the full body in session history.

---

### 2. Message

One entry in a session's `messages` array.

| Field | Type | Required | Notes |
|---|---|---|---|
| `role` | enum(`user`,`assistant`,`tool`) | yes | No `system` role stored — system prompt is composed per-turn from config + registered tools. |
| `content` | string | yes | Markdown for `user`/`assistant`; tool result content for `tool` (already stringified by the tool). |
| `thinking` | string | no | Captured from `EventThinkingDelta` stream; only non-empty on `assistant` rows when the model emitted reasoning. Plan §4. |
| `tool_calls` | []ToolCall | no | Only on `assistant` rows. |
| `tool_call_id` | string | conditional | Required on `tool` rows; matches the `assistant`-row `ToolCall.id` that spawned it. |
| `name` | string | conditional | Required on `tool` rows; the invoked tool's registered name. |
| `is_error` | bool | no | `tool` rows only; true if the tool returned an error. The error message is in `content`. FR-042. |
| `created_at` | RFC3339 | yes | |

**Why no stored system message**: the effective system prompt changes per turn (tools list changes with MCP connectivity, user's chosen `provider` may need different preambles). Composed fresh in `agent.buildRequest()` each loop iteration; not persisted.

---

### 3. ToolCall

A model-requested tool invocation. Lives inside `Message.tool_calls`.

| Field | Type | Notes |
|---|---|---|
| `id` | string | Provider-assigned (Anthropic: `toolu_*`, OpenAI: `call_*`). Used to match `tool`-role responses. |
| `name` | string | Tool name as registered. MCP tools use `mcp__<server>__<name>` prefix. |
| `arguments` | json.RawMessage | Arguments object; validated against `Tool.InputSchema()` before dispatch. |

**Validation**:
- `arguments` MUST parse as JSON; if the model emits malformed JSON across stream deltas, fallback to a synthetic tool-result row with `is_error=true, content="invalid JSON arguments from model"` (FR-042 path).

---

### 4. Tool (in-memory registry)

Not persisted; rebuilt on process startup.

| Field | Type | Notes |
|---|---|---|
| `name` | string | Unique within registry |
| `description` | string | Passed to model; 1–3 sentences |
| `source` | enum(`builtin`,`mcp`) | |
| `mcp_server` | string | Only for `source=mcp` |
| `input_schema` | json.RawMessage | JSON Schema draft-07 |
| `call` | func(ctx, args) (Result, error) | Closure |
| `available` | bool | False if the MCP server is currently disconnected; `/tools` and agent dispatch both respect this (FR-062). |

**Registry invariants**:
- Names are unique globally. On MCP reload, duplicates cause the duplicate MCP name to be rejected and logged; the first registration wins.
- Disabled tools (by config) are not inserted into the registry at all.

---

### 5. ModelProfile

A selectable provider+model pair. Configured at startup; in-memory only.

| Field | Type | Notes |
|---|---|---|
| `alias` | string | Used in `/model <alias>` |
| `provider_type` | enum(`claude`,`openai`,`openai_compatible`,`deepseek`,`gemini`,`ark`,`ollama`,`qwen`) | Maps to eino-ext sub-package |
| `model_id` | string | Vendor-specific (e.g. `claude-sonnet-4-5`) |
| `base_url` | string | Optional override |
| `api_key_env` | string | Name of env var to read at startup |
| `enable_thinking` | bool | Controls "thinking" surface in cards; only honored by providers that actually stream reasoning. |
| `max_tokens` | int | Per-turn output cap |
| `temperature` | *float32 | Nilable; nil = provider default |

**Validation on startup**:
- The env var named by `api_key_env` MUST resolve to a non-empty string; otherwise this profile is loaded in "disabled" state and excluded from `/model`. Bot does not abort.
- `alias` must be unique.

---

### 6. UserOAuthCredential

Per-user Feishu OAuth credential. Required by FR-045..FR-048 (Q1=B).

| Field | Type | Notes |
|---|---|---|
| `open_id` | string | Owning user. Key. |
| `access_token` | string | `user_access_token`. Encrypted at rest. |
| `refresh_token` | string | Encrypted at rest. Single-use (Feishu rotates on refresh). |
| `access_expires_at` | RFC3339 | From `expires_in` in token response |
| `refresh_expires_at` | RFC3339 | From `refresh_token_expires_in` |
| `scopes` | []string | Granted scope list |
| `granted_at` | RFC3339 | |
| `last_used_at` | RFC3339 | For operational visibility |

**Storage**:
- Redis key: `bot:oauth:<open_id>`
- **Value: encrypted JSON**. Encryption: AES-GCM with a key loaded from env `OAUTH_ENCRYPTION_KEY` (32-byte base64). Nonce stored inline in the ciphertext. FR-047.
- TTL: aligned to `refresh_expires_at` (Redis `EXPIREAT`); once refresh expires, the record self-cleans.

**State transitions**:
- **created** on successful OAuth callback (`GET /oauth/callback`).
- **refreshed** pre-emptively when `access_expires_at` is within 60 s; on refresh success, rotates both tokens and resets TTL.
- **invalidated** on refresh error (treat any 4xx from the token endpoint as "user must re-auth"); record is deleted. Next attempt to use restarts OAuth start flow.
- **used** → update `last_used_at` on each successful tool call that consumed it.

**Security invariants**:
- No logging of `access_token`, `refresh_token`, or decrypted state.
- Cross-user read MUST be impossible at the data layer: all lookups take `open_id` from the authenticated agent loop context, never from tool arguments.
- A user revoking via Feishu's console causes the first subsequent refresh call to 4xx; invalidation flow above handles it.

---

### 7. MCPServerRegistration

Config-level declaration of an MCP server.

| Field | Type | Notes |
|---|---|---|
| `name` | string | Unique |
| `enabled` | bool | Default false when omitted |
| `transport` | enum(`stdio`,`http`,`sse`) | |
| `command` | string | stdio only |
| `args` | []string | stdio only |
| `env` | map[string]string | stdio only; values interpolated from process env |
| `url` | string | http/sse only |
| `headers` | map[string]string | http/sse only; values interpolated |
| `initialize_timeout` | duration | Default 10 s |

**Runtime state** (not config):
- `status`: `connected` / `disconnected` / `failed`
- `last_error`: last transport error, for `/tools` output
- `tools`: tool-name set last seen

---

### 8. AgentTrace (log record, not entity)

For each agent run, emit a structured log record with fields: `trace_id`, `session_key`, `step`, `provider`, `model`, `tool_name`, `tool_duration_ms`, `tokens_in`, `tokens_out`, `stop_reason`, `status`. Not stored in Redis. FR-071.

---

## Relationship diagram

```
ConversationSession
  ├─ 1..N Message
  │       ├─ (assistant) 0..N ToolCall ──refers-by-id──▶ (tool) Message
  │       └─ (tool) IsError? content
  └─ references ModelProfile (by alias)

UserOAuthCredential (1:1 with user_open_id; independent of session)

Tool Registry  ◀── derived from Builtin Tools + active MCPServerRegistrations
```

---

## Key constraints summary (cross-cutting)

| # | Constraint | Source |
|---|---|---|
| C1 | Per-user session isolation in groups | FR-010; key shape prevents collision |
| C2 | 24h sliding TTL on sessions | FR-011; Redis EXPIRE on write |
| C3 | Single-flight per user | FR-012; Redis `SET NX EX 60` on `bot:lock:<key>` |
| C4 | OAuth tokens encrypted at rest | FR-047; AES-GCM |
| C5 | MaxSteps = 12 per turn | FR-043; D9 |
| C6 | Command messages bypass context entirely | FR-021; commands short-circuit before `sess.Append` |
| C7 | Tool errors feed back into the loop, never abort the turn | FR-042; `is_error=true` tool-role message |
| C8 | Card size ≤ 30 KB; split if larger | FR-034; D4 |
| C9 | Card PATCH ≤ 5 QPS/message | D4; 250 ms ticker floor |
