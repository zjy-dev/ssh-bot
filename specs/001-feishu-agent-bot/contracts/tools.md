# Tool Contracts (v1 builtins)

**Date**: 2026-04-30
**Scope**: Input/output schemas for built-in tools the agent exposes to the LLM. MCP-sourced tools ship their own schemas via the MCP protocol (passed through unchanged).

Each tool's `Name()`, `Description()`, and `InputSchema()` are frozen contracts: changing them requires a bump in the tool registry version. `Call()` behavior is the implementation concern; the fields here are the wire contract with the LLM.

---

## `web_search`

**Name**: `web_search`
**Source**: builtin
**Description** (passed to LLM): *"Search the public web. Returns up to `max_results` hits with title, URL, and a short snippet. Use this for current-events or open-web questions the model cannot answer from training data."*

**Input schema** (JSON Schema draft-07):

```json
{
  "$schema": "http://json-schema.org/draft-07/schema#",
  "type": "object",
  "properties": {
    "query":       {"type": "string", "minLength": 1, "maxLength": 200},
    "max_results": {"type": "integer", "minimum": 1, "maximum": 10, "default": 5}
  },
  "required": ["query"],
  "additionalProperties": false
}
```

**Result.Content** (markdown fed back to LLM):

```
1. **<title>** — <url>
   <snippet (up to 300 chars)>
2. …
```

**Result.Meta**: `{provider: "tavily"|"serper"|..., results: [{title, url, snippet}], duration_ms: int}`.

**Errors**: provider 429 → `tool` message with `is_error=true, content="Search rate-limited, try again in a moment."`. No retries at the tool layer.

---

## `web_fetch`

**Name**: `web_fetch`
**Source**: builtin
**Description**: *"Fetch a URL and return its main article content converted to Markdown. Use after `web_search` or when the user provides a URL."*

**Input**:

```json
{
  "type": "object",
  "properties": {
    "url":       {"type": "string", "format": "uri", "maxLength": 2048},
    "max_chars": {"type": "integer", "minimum": 500, "maximum": 50000, "default": 20000}
  },
  "required": ["url"],
  "additionalProperties": false
}
```

**Result.Content**: `## <title>\n\n<markdown body, truncated to max_chars with "[truncated N chars]" suffix>`

**Security**:

- URL scheme MUST be `http` or `https`; reject `file:`, `ftp:`, etc.
- Forbid private/internal targets: resolve DNS pre-fetch and refuse if the resolved IP is in RFC1918/loopback/link-local. Prevents SSRF against internal hosts (the bot might share a subnet with sensitive services).
- Respect `robots.txt` Disallow: skip and return `is_error=true`.
- Timeout: 10 s total (connect + read).
- Max response body read: 5 MB hard cap before readability extraction.

---

## `datetime`

**Name**: `datetime`
**Source**: builtin
**Description**: *"Get the current date/time or convert timezones. Use this instead of guessing — your training data is stale."*

**Input**:

```json
{
  "type": "object",
  "properties": {
    "action":   {"type": "string", "enum": ["now", "convert", "add"]},
    "timezone": {"type": "string", "default": "Asia/Shanghai"},
    "iso_input":{"type": "string"},
    "delta":    {"type": "string", "description": "ISO-8601 duration, e.g. P1D or PT2H"}
  },
  "required": ["action"],
  "additionalProperties": false
}
```

**Result.Content**: a single ISO-8601 timestamp or duration, plain text.

---

## `feishu_doc_read`

**Name**: `feishu_doc_read`
**Source**: builtin
**Auth**: Uses caller's `user_access_token` (FR-045). If absent/expired, the tool returns `is_error=true, content="请先完成飞书授权：<oauth_start_url>"` — the `oauth_start_url` is generated per-call by HMAC-signing the user's `open_id`.

**Description**: *"Read a Feishu cloud document (docx / new docs) and return its content as Markdown. Requires the user to have authorized this bot via OAuth."*

**Input**:

```json
{
  "type": "object",
  "properties": {
    "url":       {"type": "string", "format": "uri"},
    "doc_token": {"type": "string"}
  },
  "oneOf": [
    {"required": ["url"]},
    {"required": ["doc_token"]}
  ],
  "additionalProperties": false
}
```

At least one of `url` or `doc_token` must be present; if both are given, `doc_token` wins.

**Result.Content**: `## <title>\n\n<body as markdown, truncated to 20000 chars>`

**Errors**:

| Error | `content` |
|---|---|
| No valid OAuth credential for user | `"请先完成飞书授权：<oauth_start_url>"` |
| Feishu API 403 (scope/permission) | `"无权访问该文档，请确认文档已分享给你或授权 scope 足够。"` |
| Feishu API 404 | `"文档不存在或已删除。"` |
| Network timeout (>10s) | `"飞书文档服务超时，请稍后重试。"` |

---

## `feishu_doc_search`

**Name**: `feishu_doc_search`
**Source**: builtin
**Auth**: Same as `feishu_doc_read`.

**Description**: *"Search the caller's accessible Feishu cloud documents by keyword. Returns title, owner, and open URL."*

**Input**:

```json
{
  "type": "object",
  "properties": {
    "query": {"type": "string", "minLength": 1, "maxLength": 200},
    "count": {"type": "integer", "minimum": 1, "maximum": 20, "default": 10}
  },
  "required": ["query"],
  "additionalProperties": false
}
```

**Result.Content**:

```
Found N documents:
1. **<title>** — <owner> — <url>
2. …
```

---

## Tool-name reservation

The prefix `mcp__` is reserved for MCP-sourced tools. Built-ins MUST NOT use it. The prefix `feishu_` is informal and may be used by both builtins and MCP servers; uniqueness is enforced by the full name in the registry.

---

## Validation flow (all tools)

For every LLM-emitted `tool_call`:

1. Parse `arguments` as JSON. Malformed → synthetic tool-result with `is_error=true`.
2. Validate against the tool's `InputSchema()`. Violation → synthetic tool-result with `is_error=true, content="<violation message>"`.
3. Apply tool-specific pre-checks (URL scheme for `web_fetch`, OAuth cred presence for `feishu_*`).
4. Invoke `Call()` with 30 s context timeout.
5. On any panic, recover and emit `is_error=true, content="internal error"`.
