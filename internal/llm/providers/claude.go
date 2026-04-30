// Package providers holds concrete Provider implementations wrapping eino-ext
// ChatModels. These are thin factory functions; all streaming logic lives in
// the llm package.
package providers

import (
	"context"
	"fmt"
	"os"

	einoclaude "github.com/cloudwego/eino-ext/components/model/claude"

	"github.com/anomalyco/ssh-bot/internal/llm"
)

// NewClaude constructs a Claude-backed Provider from the given ModelProfile.
func NewClaude(ctx context.Context, p llm.ModelProfile) (llm.Provider, error) {
	key := os.Getenv(p.APIKeyEnv)
	if key == "" {
		return nil, fmt.Errorf("claude api key env %q not set", p.APIKeyEnv)
	}
	cfg := &einoclaude.Config{
		APIKey:      key,
		Model:       p.Model,
		MaxTokens:   p.MaxTokens,
		Temperature: p.Temperature,
	}
	if p.BaseURL != "" {
		bu := p.BaseURL
		cfg.BaseURL = &bu
	}
	if p.EnableThinking {
		// Budget of 4096 tokens is a sane default for M3 UX.
		cfg.Thinking = &einoclaude.Thinking{Enable: true, BudgetTokens: 4096}
	}
	cm, err := einoclaude.NewChatModel(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create claude model: %w", err)
	}
	return llm.WrapEino(p.Alias, cm, p.EnableThinking), nil
}
