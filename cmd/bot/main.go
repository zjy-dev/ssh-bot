// Command bot is the Feishu AI Agent Bot entry point.
//
// Startup order (quickstart.md §5):
//
//  1. Load config
//  2. Build logger
//  3. Connect Redis (session + lock + oauth stores)
//  4. Build OAuth HTTP server (fails closed on missing key env)
//  5. Build LLM provider registry (default provider MUST load)
//  6. Build tool registry + register builtins
//  7. Load MCP servers (failures non-fatal)
//  8. Build Lark long-connection client + event dispatcher
//  9. Run OAuth HTTP server + Lark ws client concurrently
//
// 10. Wait for SIGINT/SIGTERM; shut down gracefully
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"
	"github.com/redis/go-redis/v9"

	"github.com/anomalyco/ssh-bot/internal/config"
	larklocal "github.com/anomalyco/ssh-bot/internal/lark"
	"github.com/anomalyco/ssh-bot/internal/llm"
	"github.com/anomalyco/ssh-bot/internal/llm/providers"
	locallog "github.com/anomalyco/ssh-bot/internal/log"
	"github.com/anomalyco/ssh-bot/internal/mcp"
	"github.com/anomalyco/ssh-bot/internal/oauth"
	"github.com/anomalyco/ssh-bot/internal/session"
	"github.com/anomalyco/ssh-bot/internal/tool"
	"github.com/anomalyco/ssh-bot/internal/tool/builtin"
)

func main() {
	cfgPath := flag.String("config", "configs/config.yaml", "path to config YAML")
	flag.Parse()

	if err := run(*cfgPath); err != nil {
		fmt.Fprintf(os.Stderr, "bot startup failed: %v\n", err)
		os.Exit(1)
	}
}

func run(cfgPath string) error {
	loadedDotenv, err := config.LoadDotenvIfExists(config.DotenvCandidatePaths(cfgPath)...)
	if err != nil {
		return fmt.Errorf("dotenv load: %w", err)
	}

	// 1. Load config.
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("config load: %w", err)
	}

	// 2. Logger.
	level := locallog.ParseLevel(cfg.Server.LogLevel)
	logger := locallog.NewLogger(os.Stderr, level)
	slog.SetDefault(logger)
	if loadedDotenv != "" {
		logger.Info("dotenv loaded", "path", loadedDotenv)
	}
	logger.Info("config loaded", "path", cfgPath, "default_provider", cfg.LLM.DefaultProvider)

	rootCtx, cancelRoot := context.WithCancel(context.Background())
	defer cancelRoot()

	// 3. Redis.
	rdb := redis.NewClient(&redis.Options{
		Addr:     cfg.Redis.Addr,
		Password: cfg.Redis.Password,
		DB:       cfg.Redis.DB,
	})
	if err := rdb.Ping(rootCtx).Err(); err != nil {
		return fmt.Errorf("redis ping: %w", err)
	}
	logger.Info("redis connected", "addr", cfg.Redis.Addr)
	sessStore := session.NewRedisStore(rdb, cfg.Redis.SessionTTL)
	locker := session.NewRedisLocker(rdb)

	// 4. OAuth setup (fail-closed on missing env).
	oauthKey := os.Getenv(cfg.OAuth.EncryptionKeyEnv)
	stateKey := os.Getenv(cfg.OAuth.StateKeyEnv)
	enc, err := oauth.NewEncryptor(oauthKey)
	if err != nil {
		return fmt.Errorf("oauth encryption: %w", err)
	}
	signer, err := oauth.NewStateSigner(stateKey)
	if err != nil {
		return fmt.Errorf("oauth state signer: %w", err)
	}
	oauthStore := oauth.NewStore(rdb, enc)

	// 5. LLM registry.
	llmRegistry, err := buildLLMRegistry(rootCtx, cfg, logger)
	if err != nil {
		return fmt.Errorf("llm registry: %w", err)
	}

	// 6. Tool registry + builtins.
	toolReg := tool.NewRegistry()
	if err := toolReg.Register(builtin.NewDatetime()); err != nil {
		return fmt.Errorf("register datetime: %w", err)
	}
	// Other builtins (web_search, web_fetch, feishu_doc_*) land in US4 tasks.
	logger.Info("builtin tools registered", "count", len(toolReg.List()))

	// 7. MCP manager (skeleton in v1 Phase 2; real transports in US5).
	mcpMgr := mcp.NewManager()
	var mcpCfgs []mcp.ServerConfig
	for _, c := range cfg.MCPServers {
		mcpCfgs = append(mcpCfgs, mcp.ServerConfig{
			Name:              c.Name,
			Enabled:           c.Enabled,
			Transport:         c.Transport,
			Command:           c.Command,
			Args:              c.Args,
			Env:               c.Env,
			URL:               c.URL,
			Headers:           c.Headers,
			InitializeTimeout: c.InitializeTimeout,
		})
	}
	if err := mcpMgr.LoadAll(rootCtx, mcpCfgs); err != nil {
		logger.Warn("mcp LoadAll error", "err", err.Error())
	}
	var mcpFailed, mcpOK int
	for _, s := range mcpMgr.Status() {
		if s.Connected {
			mcpOK++
		} else {
			mcpFailed++
		}
	}
	logger.Info("mcp servers loaded", "ok", mcpOK, "failed", mcpFailed, "skipped", len(cfg.MCPServers)-mcpOK-mcpFailed)

	// 8. Lark client (REST) + ws (event subscription) + sender.
	larkClient := lark.NewClient(cfg.Lark.AppID, cfg.Lark.AppSecret)
	sender := larklocal.NewSender(larkClient)

	// OAuth notifier backed by lark sender.
	notifier := &larkNotifier{sender: sender}

	oauthCfg := oauth.Config{
		ListenAddr:    cfg.Server.OAuthHTTPAddr,
		PublicBaseURL: cfg.Server.PublicBaseURL,
		AppID:         cfg.Lark.AppID,
		AppSecret:     cfg.Lark.AppSecret,
		Scopes:        cfg.OAuth.Scopes,
		Notifier:      notifier,
	}
	oauthSrv := oauth.NewServer(oauthCfg, signer, oauthStore, logger)
	_ = oauthSrv // goroutine below runs it

	// Handler.
	handler := larklocal.NewHandler(
		sessStore, locker, sender, toolReg, llmRegistry,
		cfg.Lark.BotOpenID, cfg.LLM.DefaultProvider, logger,
	)

	// Event dispatcher wiring.
	eventDispatcher := dispatcher.NewEventDispatcher(cfg.Lark.VerificationToken, cfg.Lark.EncryptKey).
		OnP2MessageReceiveV1(func(ctx context.Context, e *larkim.P2MessageReceiveV1) error {
			ev, ok := larklocal.Parse(e, cfg.Lark.BotOpenID)
			if !ok {
				return nil
			}
			// Process in its own goroutine so the dispatcher remains unblocked.
			go func() {
				// Give the turn its own context with a generous cap (far over
				// per-tool and per-stream timeouts, but bounded).
				turnCtx, cancel := context.WithTimeout(rootCtx, 5*time.Minute)
				defer cancel()
				if err := handler.Handle(turnCtx, ev); err != nil {
					logger.Error("lark: handler error", "err", err.Error())
				}
			}()
			return nil
		})

	// 9. Start goroutines.
	// OAuth server.
	oauthErrCh := make(chan error, 1)
	go func() { oauthErrCh <- oauthSrv.ListenAndServe() }()

	// Lark ws client.
	wsClient := larkws.NewClient(
		cfg.Lark.AppID, cfg.Lark.AppSecret,
		larkws.WithEventHandler(eventDispatcher),
	)
	wsErrCh := make(chan error, 1)
	go func() { wsErrCh <- wsClient.Start(rootCtx) }()
	logger.Info("lark ws starting…")

	// 10. Wait for termination.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-sigCh:
		logger.Info("shutdown signal", "signal", sig.String())
	case err := <-oauthErrCh:
		logger.Error("oauth http server exited", "err", err)
	case err := <-wsErrCh:
		logger.Error("lark ws client exited", "err", err)
	}

	// Graceful shutdown.
	shutCtx, shutCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutCancel()
	if err := oauthSrv.Shutdown(shutCtx); err != nil {
		logger.Warn("oauth shutdown err", "err", err.Error())
	}
	cancelRoot()
	if err := mcpMgr.Close(); err != nil {
		logger.Warn("mcp close err", "err", err.Error())
	}
	if err := rdb.Close(); err != nil {
		logger.Warn("redis close err", "err", err.Error())
	}
	logger.Info("bot shut down cleanly")
	return nil
}

// buildLLMRegistry constructs the Provider registry from config.
func buildLLMRegistry(ctx context.Context, cfg *config.Config, logger *slog.Logger) (*llm.Registry, error) {
	factory := func(ctx context.Context, p llm.ModelProfile) (llm.Provider, error) {
		switch p.Type {
		case "claude":
			return providers.NewClaude(ctx, p)
		case "openai", "openai_compatible":
			return providers.NewOpenAI(ctx, p)
		default:
			return nil, fmt.Errorf("unsupported provider type %q", p.Type)
		}
	}
	profiles := make([]llm.ModelProfile, 0, len(cfg.LLM.Providers))
	for alias, pc := range cfg.LLM.Providers {
		profiles = append(profiles, llm.ModelProfile{
			Alias:          alias,
			Type:           pc.Type,
			Model:          pc.Model,
			APIKeyEnv:      pc.APIKeyEnv,
			BaseURL:        pc.BaseURL,
			EnableThinking: pc.EnableThinking,
			MaxTokens:      pc.MaxTokens,
			Temperature:    pc.Temperature,
		})
	}
	return llm.NewRegistry(ctx, cfg.LLM.DefaultProvider, profiles, factory, logger)
}

// larkNotifier adapts *lark.Sender to oauth.UserNotifier.
type larkNotifier struct{ sender *larklocal.Sender }

func (n *larkNotifier) NotifyUser(ctx context.Context, openID, message string) error {
	// We don't know the p2p chat id without an API call; Feishu accepts open_id
	// as ReceiveId when ReceiveIdType=open_id. Route via Sender.SendPlainCard
	// is chat-id-only; for simplicity the OAuth confirm message is sent as a
	// raw text via a dedicated helper.
	return n.sender.SendPlainCardByOpenID(ctx, openID, message)
}
