package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	larkdocs "github.com/larksuite/oapi-sdk-go/v3/service/docs/v1"
	larkdocx "github.com/larksuite/oapi-sdk-go/v3/service/docx/v1"
	larkwiki "github.com/larksuite/oapi-sdk-go/v3/service/wiki/v1"

	"github.com/anomalyco/ssh-bot/internal/oauth"
	"github.com/anomalyco/ssh-bot/internal/tool"
)

const feishuDocReadSchema = `{
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
}`

const feishuDocSearchSchema = `{
  "type": "object",
  "properties": {
    "query": {"type": "string", "minLength": 1, "maxLength": 200},
    "count": {"type": "integer", "minimum": 1, "maximum": 20, "default": 10}
  },
  "required": ["query"],
  "additionalProperties": false
}`

type FeishuDocConfig struct {
	Store     *oauth.Store
	Refresher *oauth.TokenRefresher
	StartURL  func(openID string) (string, error)
	Client    *lark.Client
}

func NewFeishuDocRead(cfg FeishuDocConfig) tool.Tool   { return &feishuDocReadTool{cfg: cfg} }
func NewFeishuDocSearch(cfg FeishuDocConfig) tool.Tool { return &feishuDocSearchTool{cfg: cfg} }

type feishuDocReadTool struct{ cfg FeishuDocConfig }
type feishuDocSearchTool struct{ cfg FeishuDocConfig }

func (t *feishuDocReadTool) Name() string { return "feishu_doc_read" }
func (t *feishuDocReadTool) Description() string {
	return "Read a Feishu cloud document (docx / new docs) and return its content as Markdown. Requires the user to have authorized this bot via OAuth."
}
func (t *feishuDocReadTool) InputSchema() json.RawMessage {
	return json.RawMessage(feishuDocReadSchema)
}
func (t *feishuDocReadTool) Source() tool.Source { return tool.SourceBuiltin }
func (t *feishuDocReadTool) Available() bool     { return t.cfg.Store != nil && t.cfg.StartURL != nil }

func (t *feishuDocSearchTool) Name() string { return "feishu_doc_search" }
func (t *feishuDocSearchTool) Description() string {
	return "Search the caller's accessible Feishu cloud documents by keyword. Returns title, owner, and open URL."
}
func (t *feishuDocSearchTool) InputSchema() json.RawMessage {
	return json.RawMessage(feishuDocSearchSchema)
}
func (t *feishuDocSearchTool) Source() tool.Source { return tool.SourceBuiltin }
func (t *feishuDocSearchTool) Available() bool     { return t.cfg.Store != nil && t.cfg.StartURL != nil }

func (t *feishuDocReadTool) Call(ctx context.Context, args json.RawMessage) (tool.Result, error) {
	var req struct {
		URL      string `json:"url"`
		DocToken string `json:"doc_token"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return tool.Result{}, tool.SimpleUserError("invalid arguments")
	}
	caller := tool.CallerOpenID(ctx)
	cred, err := t.requireCredential(ctx, caller)
	if err != nil {
		return tool.Result{}, err
	}
	token := req.DocToken
	if token == "" {
		token = parseDocToken(req.URL)
	}
	if token == "" {
		return tool.Result{}, tool.SimpleUserError("missing doc_token or unreadable Feishu doc URL")
	}
	content, title, err := fetchFeishuDocContent(ctx, t.cfg, cred, token, req.URL)
	if err != nil {
		return tool.Result{}, err
	}
	return tool.Result{Content: fmt.Sprintf("Title: %s\n\n%s", title, truncateDoc(content, 20000))}, nil
}

func (t *feishuDocSearchTool) Call(ctx context.Context, args json.RawMessage) (tool.Result, error) {
	var req struct {
		Query string `json:"query"`
		Count int    `json:"count"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return tool.Result{}, tool.SimpleUserError("invalid arguments")
	}
	if strings.TrimSpace(req.Query) == "" {
		return tool.Result{}, tool.SimpleUserError(`"query" is required`)
	}
	caller := tool.CallerOpenID(ctx)
	cred, err := t.requireCredential(ctx, caller)
	if err != nil {
		return tool.Result{}, err
	}
	items, err := searchFeishuDocs(ctx, t.cfg, cred, req.Query)
	if err != nil {
		return tool.Result{}, err
	}
	if len(items) == 0 {
		return tool.Result{Content: "Found 0 documents:"}, nil
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Found %d documents:\n", len(items))
	for i, item := range items {
		fmt.Fprintf(&sb, "%d. **%s** — %s\n", i+1, item.Title, item.URL)
	}
	return tool.Result{Content: strings.TrimSpace(sb.String())}, nil
}

func (t *feishuDocReadTool) requireCredential(ctx context.Context, openID string) (*oauth.Credential, error) {
	return requireCredential(ctx, t.cfg, openID)
}

func (t *feishuDocSearchTool) requireCredential(ctx context.Context, openID string) (*oauth.Credential, error) {
	return requireCredential(ctx, t.cfg, openID)
}

func requireCredential(ctx context.Context, cfg FeishuDocConfig, openID string) (*oauth.Credential, error) {
	if openID == "" {
		return nil, tool.SimpleUserError("missing caller identity")
	}
	cred, err := cfg.Store.Get(ctx, openID)
	if err != nil {
		return nil, err
	}
	if cred == nil {
		return nil, oauthStartError(cfg, openID)
	}
	if cfg.Refresher != nil {
		updated, err := cfg.Refresher.RefreshIfNeeded(ctx, cred)
		if err != nil {
			if reauth, ok := err.(*oauth.ReauthorizeError); ok {
				return nil, tool.SimpleUserError("请先完成飞书授权：" + reauth.URL)
			}
			return nil, err
		}
		cred = updated
	}
	cred.LastUsedAt = time.Now().UTC()
	if err := cfg.Store.Save(ctx, cred); err == nil {
		return cred, nil
	}
	return cred, nil
}

func oauthStartError(cfg FeishuDocConfig, openID string) error {
	if cfg.StartURL == nil {
		return tool.SimpleUserError("请先完成飞书授权。")
	}
	url, err := cfg.StartURL(openID)
	if err != nil {
		return err
	}
	return tool.SimpleUserError("请先完成飞书授权：" + url)
}

func parseDocToken(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) == 0 {
		return ""
	}
	return parts[len(parts)-1]
}

type feishuSearchItem struct {
	Title string
	URL   string
}

func fetchFeishuDocContent(ctx context.Context, cfg FeishuDocConfig, cred *oauth.Credential, token, rawURL string) (content, title string, err error) {
	if cfg.Client == nil {
		return "", "", fmt.Errorf("feishu client not configured")
	}
	opt := larkcore.WithUserAccessToken(cred.AccessToken)

	if isDocxToken(token, rawURL) {
		resp, err := cfg.Client.Docx.V1.Document.RawContent(ctx,
			larkdocx.NewRawContentDocumentReqBuilder().DocumentId(token).Build(),
			opt,
		)
		if err != nil {
			return "", "", mapFeishuErr(err)
		}
		if !resp.Success() {
			return "", "", mapFeishuCode(resp.Code)
		}
		info, err := cfg.Client.Docx.V1.Document.Get(ctx,
			larkdocx.NewGetDocumentReqBuilder().DocumentId(token).Build(),
			opt,
		)
		if err != nil {
			return "", "", mapFeishuErr(err)
		}
		title = token
		if info != nil && info.Data != nil && info.Data.Document != nil && info.Data.Document.Title != nil && *info.Data.Document.Title != "" {
			title = *info.Data.Document.Title
		}
		if resp.Data != nil && resp.Data.Content != nil {
			content = *resp.Data.Content
		}
		return content, title, nil
	}

	resp, err := cfg.Client.Docs.V1.Content.Get(ctx,
		larkdocs.NewGetContentReqBuilder().DocToken(token).DocType("doc").ContentType("markdown").Build(),
		opt,
	)
	if err != nil {
		return "", "", mapFeishuErr(err)
	}
	if !resp.Success() {
		return "", "", mapFeishuCode(resp.Code)
	}
	content = token
	if resp.Data != nil && resp.Data.Content != nil {
		content = *resp.Data.Content
	}
	return content, token, nil
}

func searchFeishuDocs(ctx context.Context, cfg FeishuDocConfig, cred *oauth.Credential, query string) ([]feishuSearchItem, error) {
	if cfg.Client == nil {
		return nil, fmt.Errorf("feishu client not configured")
	}
	resp, err := cfg.Client.Wiki.V1.Node.Search(ctx,
		larkwiki.NewSearchNodeReqBuilder().
			Body(larkwiki.NewSearchNodeReqBodyBuilder().Query(query).Build()).
			Build(),
		larkcore.WithUserAccessToken(cred.AccessToken),
	)
	if err != nil {
		return nil, mapFeishuErr(err)
	}
	if !resp.Success() {
		return nil, mapFeishuCode(resp.Code)
	}
	items := make([]feishuSearchItem, 0)
	if resp.Data == nil {
		return items, nil
	}
	for _, item := range resp.Data.Items {
		if item == nil {
			continue
		}
		result := feishuSearchItem{}
		if item.Title != nil {
			result.Title = *item.Title
		}
		if item.Url != nil {
			result.URL = *item.Url
		}
		if result.Title == "" && result.URL == "" {
			continue
		}
		items = append(items, result)
	}
	return items, nil
}

func isDocxToken(token, rawURL string) bool {
	if strings.HasPrefix(token, "dox") || strings.HasPrefix(token, "doccn") {
		return true
	}
	return strings.Contains(rawURL, "/docx/")
}

func truncateDoc(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return s[:max] + fmt.Sprintf("\n[truncated %d chars]", len(s)-max)
}

func mapFeishuCode(code int) error {
	switch code {
	case 403, 99991663:
		return tool.SimpleUserError("无权访问该文档，请确认文档已分享给你或授权 scope 足够。")
	case 404, 99991661:
		return tool.SimpleUserError("文档不存在或已删除。")
	default:
		return fmt.Errorf("feishu api code=%d", code)
	}
}

func mapFeishuErr(err error) error {
	if err == nil {
		return nil
	}
	if strings.Contains(err.Error(), "403") {
		return tool.SimpleUserError("无权访问该文档，请确认文档已分享给你或授权 scope 足够。")
	}
	if strings.Contains(err.Error(), "404") {
		return tool.SimpleUserError("文档不存在或已删除。")
	}
	if e, ok := err.(interface{ Timeout() bool }); ok && e.Timeout() {
		return tool.SimpleUserError("飞书文档服务超时，请稍后重试。")
	}
	if errors, ok := err.(interface{ Unwrap() error }); ok {
		_ = errors
	}
	return err
}
