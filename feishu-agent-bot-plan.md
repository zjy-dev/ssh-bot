# 飞书 AI 机器人技术方案（Go 实现）

> 目标：一个部署在飞书群里的 AI 机器人，通过 @ 唤起对话，按人存储上下文，`/clear` 清空上下文，支持多 LLM provider、流式输出（含 thinking 展示）、工具调用可视化，具备 MCP（stdio + HTTP）能力。**暂不支持 Skill。**

---

## 0. 关键决策汇总

| 项 | 选型 |
| --- | --- |
| 语言 | Go 1.22+ |
| LLM 多 provider 库 | **cloudwego/eino** 的 `ChatModel` 抽象层 + eino-ext 的 provider 实现（仅用这一层，**不用** eino 的 ADK / compose / flow） |
| Agent loop | **自研**，不依赖任何 agent 框架 |
| 工具调用协议 | 自研 `Tool` 接口；MCP 工具通过 adapter 适配进来 |
| MCP SDK | **`mark3labs/mcp-go`**，支持 stdio、Streamable HTTP、SSE |
| 飞书 SDK | `larksuite/oapi-sdk-go/v3`，long-connection 模式 |
| 会话存储 | Redis（`redis/go-redis/v9`），JSON 序列化，TTL 24h |
| 流式输出 | 飞书"卡片流式更新" API（`im/v1/messages/:id` PATCH） |
| 群聊会话粒度 | **按人独立**（key = `group:<chat_id>:<open_id>`） |
| 日志 | 标准库 `log/slog` |
| 配置 | `viper`（yaml + env 覆盖） |

---

## 1. 总体架构

```
┌──────────────────────── 飞书开放平台 ────────────────────────┐
│  事件订阅 (im.message.receive_v1) via long connection       │
└──────────────────────────▲──────────────┬────────────────────┘
                           │ 事件          │ 发消息 / 更新卡片
┌──────────────────────────┴──────────────▼────────────────────┐
│ cmd/bot (main)                                                │
│   ├─ lark.Client (long-conn dispatcher)                      │
│   └─ bot.Router                                              │
├───────────────────────────────────────────────────────────────┤
│ internal/lark       事件解析、@ 识别、指令拦截、卡片发送/更新 │
│ internal/session    Session 接口 + Redis 实现 + per-key 锁   │
│ internal/agent      Agent Loop（核心）                        │
│ internal/llm        Provider 抽象（基于 eino ChatModel）     │
│ internal/tool       Tool 接口、注册表、内置工具              │
│ internal/mcp        MCP Client（stdio + HTTP）+ Tool Adapter │
│ internal/render     流式渲染器：thinking / text / tool 卡片  │
│ internal/config     配置加载                                  │
│ internal/log        slog 封装                                 │
└───────────────────────────────────────────────────────────────┘
```

---

## 2. 目录结构

```
feishu-agent-bot/
├── cmd/
│   └── bot/
│       └── main.go
├── internal/
│   ├── agent/
│   │   ├── loop.go
│   │   ├── types.go
│   │   └── context.go
│   ├── llm/
│   │   ├── provider.go         # Provider 接口 + 工厂
│   │   ├── eino_adapter.go     # 把 eino ChatModel 适配到我们的 Provider
│   │   └── types.go            # ChatRequest / ChatStreamEvent 等
│   ├── tool/
│   │   ├── tool.go             # Tool 接口
│   │   ├── registry.go
│   │   └── builtin/
│   │       ├── web_search.go
│   │       ├── web_fetch.go
│   │       ├── feishu_doc_read.go
│   │       ├── feishu_doc_search.go
│   │       ├── feishu_sheet_read.go
│   │       ├── feishu_message_search.go
│   │       ├── code_exec.go        (可选, 沙箱)
│   │       ├── datetime.go
│   │       └── calculator.go       (可选)
│   ├── mcp/
│   │   ├── manager.go          # 多 server 管理
│   │   ├── client.go           # 封装 mcp-go
│   │   ├── stdio.go
│   │   ├── http.go
│   │   └── adapter.go          # MCP Tool -> tool.Tool
│   ├── session/
│   │   ├── store.go
│   │   ├── redis.go
│   │   ├── memory.go           # 开发用
│   │   └── lock.go             # per-user 串行锁
│   ├── lark/
│   │   ├── handler.go          # 事件入口 + 指令拦截
│   │   ├── parser.go           # @ / 指令 / mention 解析
│   │   ├── sender.go           # 发卡片、更新卡片
│   │   └── card.go             # 卡片模板
│   ├── render/
│   │   ├── renderer.go         # 流式事件 → 卡片元素
│   │   └── state.go            # thinking / tool / text 三态机
│   ├── config/
│   │   └── config.go
│   └── log/
│       └── log.go
├── configs/
│   └── config.yaml
├── go.mod
├── go.sum
├── Makefile
└── README.md
```

---

## 3. LLM Provider 层

### 为什么用 eino 的 ChatModel

- 字节自研，活跃（截至编写时 v0.8.x，10.9k★，更新频繁）
- eino-ext 官方实现了 **OpenAI / Claude / Gemini / 豆包 Ark / Ollama / DeepSeek / Qwen** 等
- 流式是一等公民：`Stream(ctx, messages) -> StreamReader[*Message]`，每家 provider 的差异它已经抹平
- 我们**只用它的 ChatModel 接口和具体实现**，不碰 ADK / compose / flow，避免被框架绑架

### 自己的 Provider 抽象

```go
// internal/llm/types.go
type ChatRequest struct {
    System    string
    Messages  []agent.Message
    Tools     []tool.Tool
    Model     string
    Temperature *float32
    MaxTokens int
    // Thinking 支持（Claude extended thinking / DeepSeek reasoning_content）
    EnableThinking bool
}

// 流式事件（我们自己定义的统一事件模型）
type StreamEventType string
const (
    EventThinkingDelta StreamEventType = "thinking_delta"  // reasoning 增量
    EventTextDelta     StreamEventType = "text_delta"      // 正文增量
    EventToolCallStart StreamEventType = "tool_call_start" // 工具调用开始(含 name, id)
    EventToolCallArgs  StreamEventType = "tool_call_args"  // 工具参数增量
    EventToolCallEnd   StreamEventType = "tool_call_end"
    EventMessageEnd    StreamEventType = "message_end"     // 一轮结束，带 stop_reason / usage
    EventError         StreamEventType = "error"
)

type StreamEvent struct {
    Type       StreamEventType
    Text       string
    ToolCallID string
    ToolName   string
    ArgsDelta  string
    StopReason string
    Usage      Usage
    Err        error
}

type Provider interface {
    Name() string
    Stream(ctx context.Context, req ChatRequest) (<-chan StreamEvent, error)
}
```

### eino_adapter.go 做的事

1. 把 `agent.Message` 转成 `schema.Message`
2. 把 `tool.Tool` 的 JSON Schema 转成 eino 的 `schema.ToolInfo`
3. 调用 `chatModel.Stream(...)`，把 eino 的 chunk 翻译成我们统一的 `StreamEvent`
   - 特别地：Claude 的 `reasoning_content` / DeepSeek 的 `reasoning_content` 字段映射到 `EventThinkingDelta`
   - `tool_calls` 增量映射到 `EventToolCall*`
4. 关闭/错误时发送 `EventMessageEnd` / `EventError`

### Provider 工厂与配置

```yaml
# configs/config.yaml 片段
llm:
  default_provider: claude
  providers:
    claude:
      type: claude
      model: claude-sonnet-4-5
      api_key_env: ANTHROPIC_API_KEY
      base_url: ""
      enable_thinking: true
    openai:
      type: openai
      model: gpt-4o
      api_key_env: OPENAI_API_KEY
    deepseek:
      type: openai_compatible
      model: deepseek-reasoner
      base_url: https://api.deepseek.com/v1
      api_key_env: DEEPSEEK_API_KEY
```

---

## 4. Agent Loop

### 核心伪代码

```go
func (a *Agent) Run(ctx context.Context, sess *Session, userInput string, emit func(StreamEvent)) error {
    sess.Append(Message{Role: User, Content: userInput})

    for step := 0; step < a.MaxSteps; step++ {
        events, err := a.provider.Stream(ctx, buildReq(sess, a.tools))
        if err != nil { return err }

        asst := Message{Role: Assistant}
        var thinkingBuf, textBuf strings.Builder
        toolCalls := map[string]*ToolCall{} // id -> partial

        for ev := range events {
            switch ev.Type {
            case EventThinkingDelta:
                thinkingBuf.WriteString(ev.Text)
                emit(ev)
            case EventTextDelta:
                textBuf.WriteString(ev.Text)
                emit(ev)
            case EventToolCallStart:
                toolCalls[ev.ToolCallID] = &ToolCall{ID: ev.ToolCallID, Name: ev.ToolName}
                emit(ev)
            case EventToolCallArgs:
                if tc, ok := toolCalls[ev.ToolCallID]; ok {
                    tc.Arguments = append(tc.Arguments, ev.ArgsDelta...)
                }
            case EventToolCallEnd:
                emit(ev)
            case EventMessageEnd:
                asst.Thinking = thinkingBuf.String()
                asst.Content = textBuf.String()
                for _, tc := range toolCalls { asst.ToolCalls = append(asst.ToolCalls, *tc) }
                sess.Append(asst)

                if len(asst.ToolCalls) == 0 {
                    return nil  // 结束
                }
                // 并发执行 tool_calls
                results := a.execTools(ctx, asst.ToolCalls, emit)
                for _, r := range results {
                    sess.Append(Message{
                        Role: Tool,
                        Content: r.Output,
                        ToolCallID: r.ID,
                        Name: r.Name,
                        IsError: r.Err != nil,
                    })
                }
            case EventError:
                return ev.Err
            }
        }
    }
    return errors.New("reached max steps")
}
```

### 关键点

- **MaxSteps = 12**，超过返回错误提示
- **每个 tool 调用独立 goroutine**，带 30s 超时和 `recover`
- **错误回传给模型**：tool 报错时把 `error: xxx` 作为 tool result 内容返回，让模型自己决定下一步
- **context 截断**：朴素策略——保留 system + 最近 20 条消息；超长再做压缩（v2 再考虑）
- **emit 回调**：向上层（render 层）推送事件，解耦 agent 与 IM

---

## 5. 流式渲染 → 飞书卡片

### 三态渲染机

状态机：`idle → thinking → text → tool_executing → (loop back to thinking/text) → done`

飞书卡片主结构：

```
┌───────────── 机器人回复卡片 ─────────────┐
│ 💭 思考中...                            │  ← thinking 区，text 到来后清空
│ ─────────────────────────────────────  │
│ <正文 markdown>                        │  ← text 区，流式增量追加
│ ─────────────────────────────────────  │
│ 🔧 工具调用                             │
│  ├─ web_search(query="xxx")  ✅ 1.2s    │
│  ├─ feishu_doc_read(token=...)  ⏳     │
│  └─ ...                               │
└────────────────────────────────────────┘
```

### 节流 / 合流策略

- 飞书卡片更新有频率限制（官方建议 ≥ 200ms 一次，实测太快会被限流）
- renderer 内部跑一个 **goroutine + ticker(250ms)**，批量 flush buffer 到飞书
- **text 到来时立刻清空 thinking 区**（把 thinking 折叠成"💭 思考完成（用时 Xs） [展开]"的小标签，或直接删除——按需取舍）
- 每次 flush 用 `PATCH im/v1/messages/:message_id` 替换整张卡片

### 卡片元素映射（伪 JSON）

```json
{
  "config": { "update_multi": true },
  "elements": [
    {"tag": "div", "text": {"tag": "lark_md", "content": "💭 **思考中...**\n> <thinking text>"}},
    {"tag": "hr"},
    {"tag": "div", "text": {"tag": "lark_md", "content": "<正文>"}},
    {"tag": "hr"},
    {"tag": "div", "text": {"tag": "lark_md", "content":
       "🔧 **工具调用**\n- `web_search` ✅ 1.2s\n- `feishu_doc_read` ⏳"
    }}
  ]
}
```

### 工具调用的展示细节

- **开始调用**：追加一行 `🔧 tool_name(input_summary) ⏳`
  - `input_summary` 取 args 的关键字段，截断到 60 字符
- **完成**：`⏳` → `✅ <elapsed>`；失败 → `❌ <err_msg 截断>`
- **可折叠**：点击可展开查看完整 input / output（飞书卡片的 collapsible panel 元素）

---

## 6. MCP 集成

### 库选择：`mark3labs/mcp-go`

- Go 生态最成熟的 MCP 实现，client + server 都有
- 支持 stdio、Streamable HTTP、SSE

### 启动流程

1. 读 `configs/config.yaml` 中的 `mcp_servers` 列表
2. 为每个 server 创建 client 并 `Initialize`
3. `ListTools` 拿到该 server 的工具列表
4. 通过 `mcp/adapter.go` 把每个 MCP tool 包装成我们的 `tool.Tool` 实现
5. 注册进全局 `tool.Registry`

### 命名空间

工具名统一改写为 `mcp__<server_name>__<original_name>`，避免与内置工具冲突。description 中可附加 `(from MCP server: xxx)`。

### 配置示例

```yaml
mcp_servers:
  - name: filesystem
    enabled: true
    transport: stdio
    command: npx
    args: ["-y", "@modelcontextprotocol/server-filesystem", "/data/shared"]
    env: {}

  - name: github
    enabled: true
    transport: stdio
    command: npx
    args: ["-y", "@modelcontextprotocol/server-github"]
    env:
      GITHUB_TOKEN: ${GITHUB_TOKEN}

  - name: internal-wiki
    enabled: true
    transport: http
    url: https://mcp.internal.example.com/mcp
    headers:
      Authorization: Bearer ${MCP_WIKI_TOKEN}
```

### 运行时行为

- 子进程（stdio）启动失败不阻塞主服务，只记录 error 日志并跳过该 server
- 支持热重载（SIGHUP）：重新加载 MCP 配置并重连（v2）
- 每次工具调用日志记录 `server_name / tool_name / duration / error`

---

## 7. 内置工具清单

> 原则：**高频、无法通过 MCP 便宜拿到、跟飞书强相关** 的放内置；其他能通过 MCP 解决的不做。

### 必选（M3）

| 工具 | 说明 | 入参关键字段 | 出参 |
| --- | --- | --- | --- |
| `web_search` | 网页搜索。默认用 **Tavily**（有 summary），可切 Serper/Bing | `query`, `max_results` | `[{title, url, snippet}]` |
| `web_fetch` | 抓取 URL 正文，用 `go-readability` 转 markdown | `url`, `max_chars` | `{title, markdown}` |
| `feishu_doc_read` | 读飞书 docx / 新版 docs 内容，转 markdown | `doc_token` 或 `url` | `{title, markdown}` |
| `feishu_doc_search` | 在用户可访问的飞书云文档中搜索 | `query`, `count` | `[{title, token, url, owner}]` |
| `datetime` | 当前时间 / 时区转换 / 日期加减。避免模型幻觉 | `action`, `timezone` | 时间字符串 |

### 推荐（M4）

| 工具 | 说明 |
| --- | --- |
| `feishu_sheet_read` | 读飞书电子表格指定 range（CSV/markdown 表格） |
| `feishu_message_search` | 搜索群聊历史（需 `im:message:readonly` 权限） |
| `feishu_wiki_read` | 读飞书知识库节点内容 |
| `feishu_user_lookup` | 按 open_id / 邮箱 / 姓名查用户信息（慎重，涉及隐私） |

### 可选（按需）

| 工具 | 说明 |
| --- | --- |
| `calculator` | 纯计算表达式。模型数学烂的时候救命，Claude/GPT-4 可省 |
| `code_exec` | 沙箱执行 Python/JS（docker/gvisor），做图表/数据分析。**重，先不做** |

### 工具接口定义

```go
type Tool interface {
    Name() string
    Description() string
    InputSchema() json.RawMessage             // JSON Schema (draft-07)
    Call(ctx context.Context, args json.RawMessage) (Result, error)
}

type Result struct {
    Content string   // 给模型看的文本；建议 markdown
    Meta    map[string]any  // 给 UI 展示用（耗时、原始 URL 等）
}
```

---

## 8. 飞书对接关键点

### SDK 与模式

- `github.com/larksuite/oapi-sdk-go/v3`
- **long-connection 模式**（`larkws.NewClient`）：免公网 IP，直连飞书开放平台，最省运维
- 订阅事件：`im.message.receive_v1`；机器人在群里只响应被 @ 的消息

### 指令前置拦截（不走 LLM）

在 `lark/handler.go` 解析完消息后、进入 agent loop 前：

| 指令 | 行为 |
| --- | --- |
| `/clear` | `sessionStore.Delete(sessionKey)`；回复 ✅ 已清空上下文 |
| `/help` | 打印内置命令与工具列表 |
| `/model <name>` | 切换当前用户的 provider（存到 session 元数据） |
| `/tools` | 列出当前可用工具及 MCP server 状态 |
| `/whoami` | debug：打印 session key / user id |

### Session Key 设计

```go
func SessionKey(event *MessageEvent) string {
    if event.ChatType == "p2p" {
        return "p2p:" + event.SenderOpenID
    }
    return "group:" + event.ChatID + ":" + event.SenderOpenID  // 按人独立
}
```

### 消息收发流程

```
收到 @ 消息
  ├─ 解析：去掉 @mention 文本、判断指令
  ├─ 指令 → 直接回复 → 结束
  ├─ 非指令：
  │    ├─ 加 per-key 锁（防同一用户消息交错）
  │    ├─ 立即发一张"初始卡片"（只有 💭 思考中...）拿到 message_id
  │    ├─ 启动 agent loop，renderer 订阅事件 → 节流 PATCH 卡片
  │    └─ 结束：最终 PATCH 卡片，解锁
```

### 长文本处理

飞书消息 markdown 单次长度上限约 30KB。超过时把正文切段，第一段保留在卡片内，后续作为楼中回复追加（或挂附件）。

---

## 9. 会话存储

### Redis schema

- Key: `bot:sess:<session_key>`
- Value: JSON `{user_id, messages[], provider, model, updated_at}`
- TTL: 24h（每次写入刷新）
- 超长消息（比如整段文档）在入 session 前截断 + 标注 `[truncated]`

### 并发控制

- per-key 互斥锁：用 Redis `SET NX EX 60s` 做分布式锁；同一用户并发消息直接回"上一条还在处理中"
- 单机部署时也可以用 `x/sync/singleflight` 或 `sync.Map[key]*sync.Mutex`

### 接口

```go
type Store interface {
    Get(ctx context.Context, key string) (*Session, error) // nil, nil 表示不存在
    Save(ctx context.Context, key string, sess *Session) error
    Delete(ctx context.Context, key string) error
}
```

---

## 10. 配置文件示例

```yaml
server:
  log_level: info

lark:
  app_id: ${LARK_APP_ID}
  app_secret: ${LARK_APP_SECRET}
  verification_token: ${LARK_VERIFICATION_TOKEN}
  encrypt_key: ${LARK_ENCRYPT_KEY}
  bot_open_id: ${LARK_BOT_OPEN_ID}    # 用来识别 @

redis:
  addr: localhost:6379
  password: ""
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
    openai:
      type: openai
      model: gpt-4o
      api_key_env: OPENAI_API_KEY
    deepseek:
      type: openai_compatible
      model: deepseek-reasoner
      base_url: https://api.deepseek.com/v1
      api_key_env: DEEPSEEK_API_KEY

tools:
  web_search:
    provider: tavily
    api_key_env: TAVILY_API_KEY
    max_results: 5
  web_fetch:
    max_chars: 20000
  feishu_doc:
    enabled: true

mcp_servers:
  - name: filesystem
    enabled: false
    transport: stdio
    command: npx
    args: ["-y", "@modelcontextprotocol/server-filesystem", "/data/shared"]
```

---

## 11. 可观测性

- **结构化日志**（slog）：每次请求一条 `trace_id`，贯穿 agent loop / tool call / llm call
- 关键字段：`session_key, step, provider, model, tool_name, tool_duration_ms, tokens_in, tokens_out, stop_reason`
- 指标（后期 v2 加 Prometheus）：
  - `agent_runs_total{provider, status}`
  - `tool_calls_total{tool, status}`
  - `llm_tokens_total{provider, kind}`
  - `card_patch_errors_total`

---

## 12. 开发里程碑

| 阶段 | 目标 | 交付 |
| --- | --- | --- |
| **M1 骨架** | 飞书 long-conn 收发、@ 解析、`/clear` 拦截、Redis session、回显 | 能在群里 @ 机器人，它能原样回复 |
| **M2 LLM + Loop** | 接入 Claude（先一个 provider），实现非流式 agent loop，无工具也能多轮对话 | 问答可用 |
| **M3 流式 + 卡片** | 接流式 API，实现 thinking/text/tool 三态卡片渲染，节流合流 | 体验接近 Claude.ai |
| **M4 内置工具** | `web_search`、`web_fetch`、`feishu_doc_read`、`feishu_doc_search`、`datetime` | 真正有用 |
| **M5 多 provider** | 加 OpenAI、DeepSeek，`/model` 切换 | 灵活 |
| **M6 MCP** | 集成 `mcp-go`，先 stdio 后 HTTP，适配到内部 Tool | 生态打通 |
| **M7 打磨** | 并发锁、超时、错误处理完善、长文本切段、指标上报、Dockerfile | 上线 |

---

## 13. 风险与未决问题

1. **飞书卡片更新限流**：如果 250ms 节流仍被限，需要降级为"每 500ms 或 200 token 才刷一次"
2. **Claude thinking 字段暴露**：不同 provider 的 "reasoning" 返回结构差异大，eino-ext 是否都已抹平需要实测
3. **MCP stdio 子进程管理**：机器人进程重启时子进程要优雅收尾；crash 后是否自动重拉
4. **权限与隐私**：飞书文档工具只能读取"机器人 / 调用用户"可访问的资源，需要走"用户身份 token"（`user_access_token`），涉及 OAuth 授权流——M4 要单独设计
5. **上下文膨胀**：朴素"保留最近 20 条"在长工具结果下会爆，M7 考虑做摘要压缩
6. **Skill 系统**：本期明确不做；如果后面要加，接入点在 `agent.Run` 前的 system prompt 组装处，以及一个独立的 `skill` 包

---

## 14. 核心依赖清单

```
github.com/cloudwego/eino                v0.8.x      # ChatModel 抽象
github.com/cloudwego/eino-ext            latest      # OpenAI/Claude/... 实现
github.com/mark3labs/mcp-go              latest      # MCP client
github.com/larksuite/oapi-sdk-go/v3      latest      # 飞书
github.com/redis/go-redis/v9             latest
github.com/spf13/viper                   latest
github.com/stretchr/testify              latest      # 测试
```

---

## 附录 A：关键接口速览

```go
// agent
type Agent struct {
    Provider llm.Provider
    Tools    *tool.Registry
    Store    session.Store
    MaxSteps int
}
func (a *Agent) Run(ctx, sess, input, emit) error

// tool
type Tool interface {
    Name() string
    Description() string
    InputSchema() json.RawMessage
    Call(ctx, args) (Result, error)
}

// llm
type Provider interface {
    Name() string
    Stream(ctx, req) (<-chan StreamEvent, error)
}

// session
type Store interface {
    Get(ctx, key) (*Session, error)
    Save(ctx, key, *Session) error
    Delete(ctx, key) error
}

// mcp
type Manager struct{ ... }
func (m *Manager) LoadAll(ctx, configs) error
func (m *Manager) Tools() []tool.Tool   // 已适配的 MCP 工具
func (m *Manager) Close() error
```
