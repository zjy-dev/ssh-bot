package providers

import (
	"context"
	"fmt"
	"os"

	einoopenai "github.com/cloudwego/eino-ext/components/model/openai"

	"github.com/anomalyco/ssh-bot/internal/llm"
)

// NewOpenAI constructs an OpenAI-backed Provider. Works for vanilla OpenAI
// and any OpenAI-compatible endpoint (e.g. DeepSeek, Together) via BaseURL.
// The profile type "openai" uses the API as-is; "openai_compatible" simply
// means the caller has set BaseURL.
func NewOpenAI(ctx context.Context, p llm.ModelProfile) (llm.Provider, error) {
	key := os.Getenv(p.APIKeyEnv)
	if key == "" {
		return nil, fmt.Errorf("openai api key env %q not set", p.APIKeyEnv)
	}
	cfg := &einoopenai.ChatModelConfig{
		APIKey:      key,
		Model:       p.Model,
		Temperature: p.Temperature,
	}
	if p.MaxTokens > 0 {
		mt := p.MaxTokens
		cfg.MaxTokens = &mt
	}
	if p.BaseURL != "" {
		cfg.BaseURL = p.BaseURL
	}
	cm, err := einoopenai.NewChatModel(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create openai model: %w", err)
	}
	return llm.WrapEino(p.Alias, cm, p.EnableThinking), nil
}
