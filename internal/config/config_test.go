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

func TestLoadDotenvIfExists_LoadsFirstMatchWithoutOverride(t *testing.T) {
	t.Setenv("KEEP_ME", "from-env")
	unsetEnvForTest(t, "SET_ME")

	dir := t.TempDir()
	missing := filepath.Join(dir, "missing.env")
	dotenvPath := filepath.Join(dir, ".env")
	err := os.WriteFile(dotenvPath, []byte("KEEP_ME=from-file\nSET_ME=loaded\n"), 0o600)
	require.NoError(t, err)

	loaded, err := LoadDotenvIfExists(missing, dotenvPath)
	require.NoError(t, err)
	require.Equal(t, dotenvPath, loaded)
	require.Equal(t, "from-env", os.Getenv("KEEP_ME"))
	require.Equal(t, "loaded", os.Getenv("SET_ME"))
}

func TestLoadDotenvIfExists_ParsesQuotedValues(t *testing.T) {
	unsetEnvForTest(t, "DOUBLE")
	unsetEnvForTest(t, "SINGLE")

	dir := t.TempDir()
	dotenvPath := filepath.Join(dir, ".env")
	err := os.WriteFile(dotenvPath, []byte("DOUBLE=\"line\\nvalue\"\nSINGLE=' spaced value '\n"), 0o600)
	require.NoError(t, err)

	loaded, err := LoadDotenvIfExists(dotenvPath)
	require.NoError(t, err)
	require.Equal(t, dotenvPath, loaded)
	require.Equal(t, "line\nvalue", os.Getenv("DOUBLE"))
	require.Equal(t, " spaced value ", os.Getenv("SINGLE"))
}

func TestLoadDotenvIfExists_InvalidLine(t *testing.T) {
	dir := t.TempDir()
	dotenvPath := filepath.Join(dir, ".env")
	err := os.WriteFile(dotenvPath, []byte("NOT_VALID\n"), 0o600)
	require.NoError(t, err)

	_, err = LoadDotenvIfExists(dotenvPath)
	require.Error(t, err)
	require.Contains(t, err.Error(), "missing '='")
}

func unsetEnvForTest(t *testing.T, key string) {
	t.Helper()
	old, existed := os.LookupEnv(key)
	require.NoError(t, os.Unsetenv(key))
	t.Cleanup(func() {
		if existed {
			require.NoError(t, os.Setenv(key, old))
			return
		}
		require.NoError(t, os.Unsetenv(key))
	})
}
