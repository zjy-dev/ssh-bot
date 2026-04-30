# Feishu Card Contract

**Date**: 2026-04-30
**Scope**: JSON shape of the streaming reply card that `internal/render` PATCHes on `/open-apis/im/v1/messages/:message_id`. The card is re-rendered **in full** on each flush (plan D4).

---

## Structural blueprint

```json
{
  "config": { "update_multi": true, "wide_screen_mode": true },
  "header": {
    "template": "blue",
    "title": { "tag": "plain_text", "content": "AI 助手" }
  },
  "elements": [

    // --- Thinking region (optional) ---
    { "tag": "div",
      "text": { "tag": "lark_md",
                "content": "💭 **思考中...**\n> <collapsed thinking preview, ≤300 chars>" } },
    { "tag": "hr" },

    // --- Text region (always present once text begins) ---
    { "tag": "div",
      "text": { "tag": "lark_md",
                "content": "<正文 markdown, up to 25 KB>" } },

    // --- Tool-call region (optional, present after first tool call starts) ---
    { "tag": "hr" },
    { "tag": "div",
      "text": { "tag": "lark_md",
                "content": "🔧 **工具调用**\n- `web_search(\"news\")` ✅ 1.2s\n- `feishu_doc_read` ⏳" } },

    // --- Footer (optional, only on error or max-steps) ---
    { "tag": "hr" },
    { "tag": "note",
      "elements": [
        { "tag": "plain_text",
          "content": "trace: 4d8a1b0e · model: claude-sonnet-4-5" }
      ] }
  ]
}
```

**Constraints**:

- `config.update_multi` **MUST** be `true` on every PATCH; Feishu requires it for multi-user chats (required by endpoint).
- Total serialized JSON ≤ **30 000 bytes** (Feishu hard cap). Renderer MUST measure before sending and activate the split-message strategy (FR-034) when approaching this limit.

---

## State-machine lifecycle (what's present when)

```
idle → thinking → text → tool_executing → text → …  → done
```

| Phase | Sections present | Notes |
|---|---|---|
| `idle` | header + "初始化中…" placeholder div | The very first send; immediately supplanted. |
| `thinking` | header + thinking div | Updates via ticker; content is latest ~300 chars of buffer. |
| `text` (first chunk) | header + collapsed-thinking note + text div | Transition: the thinking div changes to a small `note` "💭 思考完成（用时 Xs）". |
| `tool_executing` | all above + tool-call region | New list item appended on each `EventToolCallStart`. |
| `done` | all above + footer note | Footer carries trace_id + model; only rendered on final flush. |

**Transition rules**:

- Thinking region is **cleared/collapsed** on the first `EventTextDelta` (FR-032). It does not return to full size during the same assistant message.
- Each tool call is a separate list item. On `EventToolCallEnd`, the item's status marker updates: `⏳` → `✅ <duration>` or `❌ <err 60 chars>`.
- On stream error after any visible content: append `❌ 出错了：<truncated>` in the text region, preserve existing content; do not nuke the card.

---

## Serialization rules

- `lark_md` is used for all user-visible text; `plain_text` only for the header title.
- Double-escape sequences: Feishu's `lark_md` allows standard markdown but the renderer MUST escape any `\` the model emits (common in code blocks) when the body is embedded into the JSON envelope. Use `encoding/json` for the envelope — do NOT hand-concat.
- Emoji forms used consistently: `💭` thinking, `🔧` tool calls, `✅` success, `⏳` in-progress, `❌` error. No other emoji introduced programmatically (model-generated emojis in text are untouched).

---

## Flush and rate-limit policy

- **Ticker interval**: 250 ms (D4). On `230020` ("frequency limit"), fall back to 500 ms for the remainder of the turn.
- **Force-flush events** (bypass ticker):
  - End of stream (`EventMessageEnd` or `EventError`).
  - Transition `thinking → text` (gives user immediate visual feedback).
  - Tool-call terminal states (`EventToolCallEnd`).
- **Skip flush** when there is no delta since the last flush.

---

## Error-path cards

For operator-visible errors (not user errors that an LLM retry fixes), the terminal card renders only:

```json
{
  "config": { "update_multi": true },
  "header": { "template": "red", "title": { "tag": "plain_text", "content": "AI 助手（遇到问题）" } },
  "elements": [
    { "tag": "div", "text": { "tag": "lark_md",
        "content": "❌ **<user-friendly error>**\n\n如问题持续，请联系管理员并提供以下信息：\n```\ntrace: <trace_id>\n```" } }
  ]
}
```

Error-friendly phrase catalog:
- `"上一条还在处理中，请稍候"` — FR-012 path.
- `"已达到推理步数上限（12 步），请拆解问题后重试"` — FR-043 path.
- `"大模型暂时不可用，请稍后重试"` — provider 5xx.
- `"上下文太长无法继续，发送 /clear 后再试"` — context-window overflow after internal trim failed.

Internal details (stack traces, provider error bodies, API keys) MUST NOT appear in cards (FR-070).
