# Quickstart: 飞书 AI 机器人 (001-feishu-agent-bot)

**Audience**: A developer setting up the project locally for the first time, or an operator bringing up a fresh staging instance.

---

## 0. Prerequisites

| What | Why | How |
|---|---|---|
| Go **1.26.2** | Build toolchain (pinned latest per project policy) | `go install golang.org/dl/go1.26.2@latest && go1.26.2 download` or distro package |
| Redis **≥ 7** | Session + lock + OAuth storage | `docker run --rm -p 6379:6379 redis:7` suffices for local dev |
| Feishu developer app | Event subscription + card API + OAuth | https://open.feishu.cn/app — create "企业自建应用" |
| HTTPS-terminating reverse proxy (for OAuth callback) | Feishu requires HTTPS on `redirect_uri` | Caddy or ngrok for local dev |
| Claude API key (Anthropic) | Default LLM provider | https://console.anthropic.com |
| Tavily (or similar) search API key | `web_search` tool | Optional in M1/M2, required from M4 |

---

## 1. Clone + pin dependencies

```sh
git clone <repo> ssh-bot && cd ssh-bot
git checkout 001-feishu-agent-bot       # feature branch
```

`go.mod` is created empty by `go mod init` (first time). The first-time-setup commands are:

```sh
go mod init github.com/<org>/ssh-bot
# Core deps — each will resolve to its own latest
go get github.com/cloudwego/eino@v0.8.13
go get github.com/cloudwego/eino-ext/components/model/claude@latest
go get github.com/cloudwego/eino-ext/components/model/openai@latest
go get github.com/mark3labs/mcp-go@v0.49.0
go get github.com/larksuite/oapi-sdk-go/v3@v3.6.1     # v3 is the active major version
go get github.com/redis/go-redis/v9@v9.19.0
go get github.com/spf13/viper@v1.21.0
go get github.com/stretchr/testify@v1.11.1
# Archived; pin by commit SHA — see research.md D2
go get github.com/go-shiori/go-readability@<latest-commit-sha>
go mod tidy
```

---

## 2. Feishu app configuration

In **Developer Console → Events & Callbacks**:

- Subscribe event `im.message.receive_v1`.
- Connection mode: **long connection** (no public event URL needed).

In **Security Settings → Redirect URLs**:

- Add `https://<your-domain>/oauth/callback`. For local dev: `https://<ngrok-subdomain>.ngrok.io/oauth/callback`.

In **Permissions**:

- Add scopes: `im:message`, `im:message.group_at_msg`, `im:message.p2p_msg`, `docx:document:readonly`, `drive:drive:readonly`, `contact:user.base:readonly`, `offline_access`.
- Request tenant admin approval if required by your org.

Record **App ID**, **App Secret**, **Encrypt Key** (if enabled), and **Verification Token**.

---

## 3. Configuration file

Copy `configs/config.example.yaml` to `configs/config.yaml` and fill in:

```yaml
server:
  log_level: info
  oauth_http_addr: 127.0.0.1:8080
  public_base_url: https://bot.example.com      # used to construct redirect_uri

lark:
  app_id: ${LARK_APP_ID}
  app_secret: ${LARK_APP_SECRET}
  encrypt_key: ${LARK_ENCRYPT_KEY}
  verification_token: ${LARK_VERIFICATION_TOKEN}
  bot_open_id: ${LARK_BOT_OPEN_ID}

redis:
  addr: localhost:6379
  db: 0
  session_ttl: 24h

llm:
  default_provider: claude
  max_steps: 12
  providers:
    claude:
      type: claude
      model: claude-sonnet-4-5
      api_key_env: ANTHROPIC_API_KEY
      enable_thinking: true
      max_tokens: 8192
    gpt:
      type: openai
      model: gpt-4o
      api_key_env: OPENAI_API_KEY

tools:
  web_search:
    provider: tavily
    api_key_env: TAVILY_API_KEY

oauth:
  encryption_key_env: OAUTH_ENCRYPTION_KEY       # 32-byte base64 value
  state_key_env: OAUTH_STATE_KEY                  # 32-byte base64 value
  scopes:
    - docx:document:readonly
    - drive:drive:readonly
    - contact:user.base:readonly
    - offline_access

mcp_servers: []   # start empty; add later (see §7)
```

---

## 4. Environment variables

Create a `.env` (not committed):

```sh
LARK_APP_ID=cli_xxx
LARK_APP_SECRET=xxx
LARK_ENCRYPT_KEY=xxx
LARK_VERIFICATION_TOKEN=xxx
LARK_BOT_OPEN_ID=ou_xxx

ANTHROPIC_API_KEY=sk-ant-...
OPENAI_API_KEY=sk-...
TAVILY_API_KEY=tvly-...

# Generate with: openssl rand -base64 32
OAUTH_ENCRYPTION_KEY=<base64>
OAUTH_STATE_KEY=<base64>
```

---

## 5. Build + run

```sh
go build -o bin/bot ./cmd/bot
bin/bot --config configs/config.yaml
```

Expected startup log sequence:

1. `config loaded`
2. `redis connected`
3. `llm providers registered: [claude, gpt]`
4. `mcp servers loaded: 0 ok, 0 failed, 0 skipped`
5. `oauth http listening on 127.0.0.1:8080`
6. `lark ws connecting…`
7. `lark ws connected; subscribed to im.message.receive_v1`
8. `bot ready`

If step 5 or 6 fails, the process exits non-zero (fail-closed). If step 4 logs failures for individual MCP servers, the bot continues — those servers are marked unavailable (FR-062).

---

## 6. Smoke test

1. In Feishu, open the 1-1 chat with the bot.
2. Send: `你好`. Expected: within 3 s a card appears and streams a reply.
3. Send: `/clear`. Expected: `✅ 已清空上下文` within 1 s, no LLM call in logs.
4. Send: `/tools`. Expected: a list including `web_search`, `web_fetch`, `feishu_doc_read`, `feishu_doc_search`, `datetime`.
5. Send: `搜一下今天 iOS 最新版本号`. Expected: card shows a `web_search` tool call entry, then an answer grounded in results.
6. Send: `读一下 https://<a-feishu-doc-you-own>`. Expected: the card shows `feishu_doc_read` attempt. First time → the tool returns `请先完成飞书授权：<url>`; click the link, land on `/oauth/callback`, see "授权成功"; return and re-ask; expect the doc content.

---

## 7. Adding an MCP server (optional)

Append to `mcp_servers` in config:

```yaml
mcp_servers:
  - name: filesystem
    enabled: true
    transport: stdio
    command: npx
    args: ["-y", "@modelcontextprotocol/server-filesystem", "/tmp/shared"]
```

Restart the bot. `/tools` should now list `mcp__filesystem__read_file`, etc.

If the stdio subprocess crashes on start, the bot logs the error and continues; `/tools` marks `filesystem` as unavailable.

---

## 8. Tests

```sh
go test ./...                       # full unit suite
go test ./internal/agent -run TestLoopToolErrorFeedback -v
go vet ./...
gofmt -l .                          # must return empty
```

Integration tests that hit Redis: start a local Redis (port 6379) before running.
Tests that touch Feishu or LLM providers are behind the `integration` build tag:

```sh
go test -tags=integration ./...
```

Skip them in CI unless `ANTHROPIC_API_KEY` and `LARK_*` env vars are populated.

---

## 9. Common troubleshooting

| Symptom | Likely cause | Fix |
|---|---|---|
| bot hangs on "lark ws connecting…" | App not approved / app_id mismatch | Verify in developer console; check tenant admin approval |
| Card updates show `frequency limit (230020)` in logs | Renderer ticker too aggressive | Renderer auto-falls-back to 500 ms; if persistent, reduce `render.flush_ms` in config |
| OAuth callback 400 `invalid state` | `state` token tampered or `OAUTH_STATE_KEY` rotated | Regenerate state key, restart |
| `feishu_doc_read` permanent 403 | User lacks doc-read scope | Re-authorize with a broader scope set |
| Agent loop returns "reached max steps" | Model stuck in tool-call loop | Inspect logs; typically malformed tool args causing retries |

---

## 10. What's next

After getting the smoke test to pass:

- Read `research.md` for the *why* behind each non-obvious decision.
- Read `data-model.md` for entity shapes and invariants.
- Read `contracts/*` for precise interfaces before modifying any package.
- Consult `tasks.md` (generated by `/speckit.tasks`) for the ordered work list.
