package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoad_Minimal(t *testing.T) {
	t.Setenv("LARK_APP_ID", "cli_xxx")
	t.Setenv("LARK_APP_SECRET", "secret_xxx")
	t.Setenv("LARK_BOT_OPEN_ID", "ou_xxx")
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-xxx")

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	err := os.WriteFile(path, []byte(`
server:
  public_base_url: https://bot.example.com

lark:
  app_id: ${LARK_APP_ID}
  app_secret: ${LARK_APP_SECRET}
  bot_open_id: ${LARK_BOT_OPEN_ID}

redis:
  addr: localhost:6379

llm:
  default_provider: claude
  providers:
    claude:
      type: claude
      model: claude-sonnet-4-5
      api_key_env: ANTHROPIC_API_KEY
`), 0o600)
	require.NoError(t, err)

	cfg, err := Load(path)
	require.NoError(t, err)
	require.Equal(t, "cli_xxx", cfg.Lark.AppID)
	require.Equal(t, "secret_xxx", cfg.Lark.AppSecret)
	require.Equal(t, "ou_xxx", cfg.Lark.BotOpenID)
	require.Equal(t, "claude", cfg.LLM.DefaultProvider)
	require.Equal(t, 12, cfg.LLM.MaxSteps) // default applied
	require.Contains(t, cfg.LLM.Providers, "claude")
}

func TestLoad_MissingDefaultProvider(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	err := os.WriteFile(path, []byte(`
server:
  public_base_url: https://bot.example.com
lark:
  app_id: x
  app_secret: x
  bot_open_id: x
redis:
  addr: localhost:6379
llm:
  default_provider: unknown
  providers:
    claude:
      type: claude
      api_key_env: X
`), 0o600)
	require.NoError(t, err)

	_, err = Load(path)
	require.Error(t, err)
	require.Contains(t, err.Error(), "default_provider")
}
