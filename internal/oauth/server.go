package oauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/anomalyco/ssh-bot/internal/log"
)

// FeishuTokenEndpoint is the authen/v2 endpoint (research.md D3).
const FeishuTokenEndpoint = "https://open.feishu.cn/open-apis/authen/v2/oauth/token"

// FeishuAuthorizeURL is the user-facing authorize page host.
// Note: accounts.feishu.cn (NOT open.feishu.cn) per research.md D3.
const FeishuAuthorizeURL = "https://accounts.feishu.cn/open-apis/authen/v1/authorize"

// UserNotifier lets the server DM the user post-authorization.
// Implemented by internal/lark.Sender.SendPlainCard via a thin adapter.
type UserNotifier interface {
	// NotifyUser sends a text message to the given user's p2p chat with the bot.
	// Passed the OAuth-flow result so we can tell the user "成功" or describe the error.
	NotifyUser(ctx context.Context, openID, message string) error
}

// Config for the OAuth HTTP server.
type Config struct {
	ListenAddr    string
	PublicBaseURL string
	AppID         string
	AppSecret     string
	Scopes        []string
	Notifier      UserNotifier
}

// Server hosts /oauth/start, /oauth/callback, and /healthz.
type Server struct {
	cfg     Config
	signer  *StateSigner
	store   *Store
	logger  *slog.Logger
	http    *http.Server
	limiter *callbackLimiter
}

// NewServer wires the server.
func NewServer(cfg Config, signer *StateSigner, store *Store, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	mux := http.NewServeMux()
	s := &Server{
		cfg:     cfg,
		signer:  signer,
		store:   store,
		logger:  logger,
		limiter: newCallbackLimiter(20, time.Minute), // 20/min/IP per contract
	}
	mux.HandleFunc("/healthz", s.handleHealth)
	mux.HandleFunc("/oauth/start", s.handleStart)
	mux.HandleFunc("/oauth/callback", s.handleCallback)
	s.http = &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
	}
	return s
}

// ListenAndServe starts the HTTP server (blocking).
func (s *Server) ListenAndServe() error {
	s.logger.Info("oauth: http listening", "addr", s.cfg.ListenAddr)
	if err := s.http.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Shutdown gracefully stops the server.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.http.Shutdown(ctx)
}

// tokenEndpointOverride, if non-empty, replaces FeishuTokenEndpoint. Set only
// by tests via OverrideTokenEndpointForTest.
var tokenEndpointOverride string

// OverrideTokenEndpointForTest replaces the Feishu token endpoint URL for
// tests. Safe to call from one goroutine at a time. Do not use in production.
func (s *Server) OverrideTokenEndpointForTest(url string) { tokenEndpointOverride = url }

// HandleHealthForTest exposes the internal handler for tests.
func (s *Server) HandleHealthForTest(w http.ResponseWriter, r *http.Request) { s.handleHealth(w, r) }

// HandleStartForTest exposes the internal handler for tests.
func (s *Server) HandleStartForTest(w http.ResponseWriter, r *http.Request) { s.handleStart(w, r) }

// HandleCallbackForTest exposes the internal handler for tests.
func (s *Server) HandleCallbackForTest(w http.ResponseWriter, r *http.Request) {
	s.handleCallback(w, r)
}

// StartURL returns a full /oauth/start URL for the given user.
// Used by feishu doc tools when they encounter a missing credential
// (contracts/tools.md feishu_doc_read error path).
func (s *Server) StartURL(openID string) (string, error) {
	state, err := s.signer.Sign(openID)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s/oauth/start?state=%s", strings.TrimRight(s.cfg.PublicBaseURL, "/"), state), nil
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

// handleStart redirects the user to Feishu's authorize page. State MUST be
// pre-signed; we verify it here (defense-in-depth — would fail on tamper).
func (s *Server) handleStart(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("state")
	if state == "" {
		http.Error(w, "invalid state", http.StatusBadRequest)
		return
	}
	if _, err := s.signer.Verify(state); err != nil {
		http.Error(w, "invalid state", http.StatusBadRequest)
		return
	}
	redirectURI := strings.TrimRight(s.cfg.PublicBaseURL, "/") + "/oauth/callback"
	q := url.Values{}
	q.Set("app_id", s.cfg.AppID)
	q.Set("redirect_uri", redirectURI)
	q.Set("state", state)
	q.Set("scope", strings.Join(s.cfg.Scopes, " "))
	q.Set("response_type", "code")
	authURL := FeishuAuthorizeURL + "?" + q.Encode()
	http.Redirect(w, r, authURL, http.StatusFound)
}

// handleCallback exchanges the authorization code and stores the credential.
func (s *Server) handleCallback(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if !s.limiter.allow(ip) {
		http.Error(w, "too many requests", http.StatusTooManyRequests)
		return
	}

	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")
	if code == "" || state == "" {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	openID, err := s.signer.Verify(state)
	if err != nil {
		http.Error(w, "invalid state", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	ctx = log.WithTrace(ctx, log.NewTraceID())
	logger := log.FromContext(ctx, s.logger)

	cred, exchErr := s.exchange(ctx, code, openID)
	if exchErr != nil {
		logger.Warn("oauth: callback exchange failed", "err", exchErr.Error())
		status := http.StatusBadRequest
		if errors.Is(exchErr, errUpstreamTimeout) {
			status = http.StatusGatewayTimeout
			http.Error(w, "授权服务暂时不可用", status)
			return
		}
		http.Error(w, "授权失败，请稍后重试", status)
		return
	}
	if err := s.store.Save(ctx, cred); err != nil {
		logger.Error("oauth: save credential", "err", err.Error())
		http.Error(w, "授权存储失败，请稍后重试", http.StatusInternalServerError)
		return
	}

	// Notify the user in-chat (best-effort).
	if s.cfg.Notifier != nil {
		if err := s.cfg.Notifier.NotifyUser(ctx, openID, "✅ 授权成功，请回到飞书继续提问。"); err != nil {
			logger.Warn("oauth: user notify failed", "err", err.Error())
		}
	}

	// Log: never include token contents.
	logger.Info("oauth: credential stored",
		"open_id_prefix", firstN(openID, 6),
		"scopes", cred.Scopes,
		"refresh_expires_at", cred.RefreshExpiresAt)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(`<!doctype html><meta charset=utf-8><title>授权成功</title><body style="font-family:sans-serif;padding:2em"><h1>✅ 授权成功</h1><p>请回到飞书继续提问。可以关闭此页面。</p>`))
}

// ErrUpstreamTimeout is returned when Feishu's token endpoint times out.
var errUpstreamTimeout = errors.New("upstream timeout")

// exchange POSTs to the Feishu v2 token endpoint and returns a Credential.
func (s *Server) exchange(ctx context.Context, code, openID string) (*Credential, error) {
	redirectURI := strings.TrimRight(s.cfg.PublicBaseURL, "/") + "/oauth/callback"
	body := map[string]string{
		"grant_type":    "authorization_code",
		"client_id":     s.cfg.AppID,
		"client_secret": s.cfg.AppSecret,
		"code":          code,
		"redirect_uri":  redirectURI,
	}
	raw, _ := json.Marshal(body)
	endpoint := FeishuTokenEndpoint
	if tokenEndpointOverride != "" {
		endpoint = tokenEndpointOverride
	}
	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")

	cli := &http.Client{Timeout: 10 * time.Second}
	resp, err := cli.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, errUpstreamTimeout
		}
		return nil, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("token endpoint status %d", resp.StatusCode)
	}
	var tok struct {
		AccessToken           string `json:"access_token"`
		ExpiresIn             int    `json:"expires_in"`
		RefreshToken          string `json:"refresh_token"`
		RefreshTokenExpiresIn int    `json:"refresh_token_expires_in"`
		Scope                 string `json:"scope"`
		Code                  int    `json:"code"`
		Error                 string `json:"error,omitempty"`
		ErrorDesc             string `json:"error_description,omitempty"`
	}
	if err := json.Unmarshal(respBody, &tok); err != nil {
		return nil, fmt.Errorf("parse token response: %w", err)
	}
	if tok.Code != 0 && tok.AccessToken == "" {
		return nil, fmt.Errorf("feishu code=%d", tok.Code)
	}
	if tok.Error != "" {
		return nil, fmt.Errorf("feishu error=%s desc=%s", tok.Error, tok.ErrorDesc)
	}
	if tok.AccessToken == "" {
		return nil, errors.New("empty access_token")
	}

	now := time.Now().UTC()
	return &Credential{
		OpenID:           openID,
		AccessToken:      tok.AccessToken,
		RefreshToken:     tok.RefreshToken,
		AccessExpiresAt:  now.Add(time.Duration(tok.ExpiresIn) * time.Second),
		RefreshExpiresAt: now.Add(time.Duration(tok.RefreshTokenExpiresIn) * time.Second),
		Scopes:           strings.Fields(tok.Scope),
		GrantedAt:        now,
	}, nil
}

// clientIP extracts a best-effort client IP for rate limiting.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if idx := strings.IndexByte(xff, ','); idx > 0 {
			return strings.TrimSpace(xff[:idx])
		}
		return strings.TrimSpace(xff)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil && host != "" {
		return host
	}
	return r.RemoteAddr
}

func firstN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// ----------------------------------------------------------------------------
// Per-IP token bucket rate limiter (contracts/http-endpoints.md).
// ----------------------------------------------------------------------------

type callbackLimiter struct {
	mu       sync.Mutex
	window   time.Duration
	burst    int
	byIP     map[string][]time.Time
	lastScan time.Time
}

func newCallbackLimiter(burst int, window time.Duration) *callbackLimiter {
	return &callbackLimiter{
		window: window,
		burst:  burst,
		byIP:   make(map[string][]time.Time),
	}
}

// allow returns false if ip has exceeded its bucket in the last window.
func (l *callbackLimiter) allow(ip string) bool {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()

	// Infrequent gc of old buckets.
	if now.Sub(l.lastScan) > 5*time.Minute {
		cutoff := now.Add(-l.window)
		for k, ts := range l.byIP {
			nts := ts[:0]
			for _, t := range ts {
				if t.After(cutoff) {
					nts = append(nts, t)
				}
			}
			if len(nts) == 0 {
				delete(l.byIP, k)
			} else {
				l.byIP[k] = nts
			}
		}
		l.lastScan = now
	}

	cutoff := now.Add(-l.window)
	ts := l.byIP[ip]
	nts := ts[:0]
	for _, t := range ts {
		if t.After(cutoff) {
			nts = append(nts, t)
		}
	}
	if len(nts) >= l.burst {
		l.byIP[ip] = nts
		return false
	}
	nts = append(nts, now)
	l.byIP[ip] = nts
	return true
}
