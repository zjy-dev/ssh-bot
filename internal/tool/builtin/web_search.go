package builtin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/anomalyco/ssh-bot/internal/tool"
)

const webSearchSchema = `{
  "$schema": "http://json-schema.org/draft-07/schema#",
  "type": "object",
  "properties": {
    "query":       {"type": "string", "minLength": 1, "maxLength": 200},
    "max_results": {"type": "integer", "minimum": 1, "maximum": 10, "default": 5}
  },
  "required": ["query"],
  "additionalProperties": false
}`

type WebSearchConfig struct {
	Provider   string
	APIKeyEnv  string
	MaxResults int
	Client     *http.Client
	Endpoint   string
}

func NewWebSearch(cfg WebSearchConfig) tool.Tool {
	if cfg.Provider == "" {
		cfg.Provider = "tavily"
	}
	if cfg.MaxResults <= 0 {
		cfg.MaxResults = 5
	}
	if cfg.Endpoint == "" {
		cfg.Endpoint = "https://api.tavily.com/search"
	}
	if cfg.Client == nil {
		cfg.Client = &http.Client{Timeout: 10 * time.Second}
	}
	return &webSearchTool{cfg: cfg}
}

type webSearchTool struct{ cfg WebSearchConfig }

func (t *webSearchTool) Name() string { return "web_search" }
func (t *webSearchTool) Description() string {
	return "Search the public web. Returns up to max_results hits with title, URL, and a short snippet. Use this for current-events or open-web questions the model cannot answer from training data."
}
func (t *webSearchTool) InputSchema() json.RawMessage { return json.RawMessage(webSearchSchema) }
func (t *webSearchTool) Source() tool.Source          { return tool.SourceBuiltin }
func (t *webSearchTool) Available() bool              { return os.Getenv(t.cfg.APIKeyEnv) != "" }

func (t *webSearchTool) Call(ctx context.Context, args json.RawMessage) (tool.Result, error) {
	var req struct {
		Query      string `json:"query"`
		MaxResults int    `json:"max_results"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return tool.Result{}, tool.SimpleUserError("invalid arguments")
	}
	if strings.TrimSpace(req.Query) == "" {
		return tool.Result{}, tool.SimpleUserError(`"query" is required`)
	}
	if req.MaxResults <= 0 {
		req.MaxResults = t.cfg.MaxResults
	}
	if req.MaxResults > 10 {
		req.MaxResults = 10
	}

	apiKey := os.Getenv(t.cfg.APIKeyEnv)
	if apiKey == "" {
		return tool.Result{}, tool.SimpleUserError("Search provider is not configured.")
	}

	payload := map[string]any{
		"api_key":        apiKey,
		"query":          req.Query,
		"max_results":    req.MaxResults,
		"search_depth":   "basic",
		"include_answer": false,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return tool.Result{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, t.cfg.Endpoint, bytes.NewReader(raw))
	if err != nil {
		return tool.Result{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := t.cfg.Client.Do(httpReq)
	if err != nil {
		return tool.Result{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == http.StatusTooManyRequests {
		return tool.Result{}, tool.SimpleUserError("Search rate-limited, try again in a moment.")
	}
	if resp.StatusCode >= 400 {
		return tool.Result{}, fmt.Errorf("search provider status %d", resp.StatusCode)
	}

	var parsed struct {
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Content string `json:"content"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return tool.Result{}, err
	}

	var out strings.Builder
	metaResults := make([]map[string]string, 0, len(parsed.Results))
	for i, item := range parsed.Results {
		title := strings.TrimSpace(item.Title)
		if title == "" {
			title = item.URL
		}
		snippet := truncateSpace(item.Content, 300)
		fmt.Fprintf(&out, "%d. **%s** — %s\n", i+1, title, item.URL)
		if snippet != "" {
			fmt.Fprintf(&out, "   %s\n", snippet)
		}
		metaResults = append(metaResults, map[string]string{"title": title, "url": item.URL, "snippet": snippet})
	}
	if len(parsed.Results) == 0 {
		out.WriteString("No results found.")
	}
	return tool.Result{
		Content: strings.TrimSpace(out.String()),
		Meta: map[string]any{
			"provider": t.cfg.Provider,
			"results":  metaResults,
		},
	}, nil
}

func truncateSpace(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
