package builtin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	readability "codeberg.org/readeck/go-readability/v2"

	"github.com/anomalyco/ssh-bot/internal/tool"
)

const webFetchSchema = `{
  "type": "object",
  "properties": {
    "url":       {"type": "string", "format": "uri", "maxLength": 2048},
    "max_chars": {"type": "integer", "minimum": 500, "maximum": 50000, "default": 20000}
  },
  "required": ["url"],
  "additionalProperties": false
}`

type WebFetchConfig struct {
	Client   *http.Client
	MaxChars int
	Resolver *net.Resolver
}

func NewWebFetch(cfg WebFetchConfig) tool.Tool {
	if cfg.MaxChars <= 0 {
		cfg.MaxChars = 20000
	}
	if cfg.Client == nil {
		cfg.Client = &http.Client{Timeout: 10 * time.Second}
	}
	if cfg.Resolver == nil {
		cfg.Resolver = net.DefaultResolver
	}
	return &webFetchTool{cfg: cfg}
}

type webFetchTool struct{ cfg WebFetchConfig }

func (t *webFetchTool) Name() string { return "web_fetch" }
func (t *webFetchTool) Description() string {
	return "Fetch a URL and return its main article content converted to Markdown. Use after web_search or when the user provides a URL."
}
func (t *webFetchTool) InputSchema() json.RawMessage { return json.RawMessage(webFetchSchema) }
func (t *webFetchTool) Source() tool.Source          { return tool.SourceBuiltin }
func (t *webFetchTool) Available() bool              { return true }

func (t *webFetchTool) Call(ctx context.Context, args json.RawMessage) (tool.Result, error) {
	var req struct {
		URL      string `json:"url"`
		MaxChars int    `json:"max_chars"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return tool.Result{}, tool.SimpleUserError("invalid arguments")
	}
	if req.URL == "" {
		return tool.Result{}, tool.SimpleUserError(`"url" is required`)
	}
	if req.MaxChars <= 0 {
		req.MaxChars = t.cfg.MaxChars
	}
	parsed, err := url.Parse(req.URL)
	if err != nil {
		return tool.Result{}, tool.SimpleUserError("invalid url")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return tool.Result{}, tool.SimpleUserError("unsupported URL scheme")
	}
	if parsed.Hostname() == "" {
		return tool.Result{}, tool.SimpleUserError("invalid url")
	}
	if err := t.rejectPrivateTarget(ctx, parsed.Hostname()); err != nil {
		return tool.Result{}, tool.SimpleUserError(err.Error())
	}
	if blocked, err := t.disallowedByRobots(ctx, parsed); err != nil {
		return tool.Result{}, err
	} else if blocked {
		return tool.Result{}, tool.SimpleUserError("blocked by robots.txt")
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, req.URL, nil)
	if err != nil {
		return tool.Result{}, err
	}
	httpReq.Header.Set("User-Agent", "ssh-bot/1.0")
	resp, err := t.cfg.Client.Do(httpReq)
	if err != nil {
		return tool.Result{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return tool.Result{}, fmt.Errorf("fetch status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 5<<20))
	if err != nil {
		return tool.Result{}, err
	}
	article, err := readability.FromReader(bytes.NewReader(body), parsed)
	if err != nil {
		return tool.Result{}, err
	}
	var text bytes.Buffer
	if err := article.RenderText(&text); err != nil {
		return tool.Result{}, err
	}
	content := strings.TrimSpace(text.String())
	if len(content) > req.MaxChars {
		content = content[:req.MaxChars] + fmt.Sprintf("\n[truncated %d chars]", len(content)-req.MaxChars)
	}
	return tool.Result{Content: fmt.Sprintf("Title: %s\n\n%s", article.Title(), content)}, nil
}

func (t *webFetchTool) rejectPrivateTarget(ctx context.Context, host string) error {
	if addr, err := netip.ParseAddr(host); err == nil {
		if isPrivateIP(addr) {
			return fmt.Errorf("refusing private or loopback target")
		}
		return nil
	}
	addrs, err := t.cfg.Resolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return err
	}
	for _, addr := range addrs {
		if isPrivateIP(addr) {
			return fmt.Errorf("refusing private or loopback target")
		}
	}
	return nil
}

func (t *webFetchTool) disallowedByRobots(ctx context.Context, parsed *url.URL) (bool, error) {
	robotsURL := parsed.Scheme + "://" + parsed.Host + "/robots.txt"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, robotsURL, nil)
	if err != nil {
		return false, err
	}
	resp, err := t.cfg.Client.Do(req)
	if err != nil {
		return false, nil
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return false, nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
	if err != nil {
		return false, err
	}
	path := parsed.EscapedPath()
	if path == "" {
		path = "/"
	}
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		lower := strings.ToLower(line)
		if strings.HasPrefix(lower, "disallow:") {
			rule := strings.TrimSpace(line[len("disallow:"):])
			if rule != "" && strings.HasPrefix(path, rule) {
				return true, nil
			}
		}
	}
	return false, nil
}

func isPrivateIP(addr netip.Addr) bool {
	return addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast()
}
