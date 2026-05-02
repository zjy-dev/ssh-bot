// Package config loads the bot's configuration from YAML + env vars.
package config

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/viper"
)

// Config is the top-level configuration struct.
//
// Structure mirrors configs/config.example.yaml and contracts/go-interfaces.md#config.
type Config struct {
	Server     ServerConfig      `mapstructure:"server"`
	Lark       LarkConfig        `mapstructure:"lark"`
	Redis      RedisConfig       `mapstructure:"redis"`
	LLM        LLMConfig         `mapstructure:"llm"`
	Tools      map[string]any    `mapstructure:"tools"`
	MCPServers []MCPServerConfig `mapstructure:"mcp_servers"`
	OAuth      OAuthConfig       `mapstructure:"oauth"`
}

type ServerConfig struct {
	LogLevel      string `mapstructure:"log_level"`
	OAuthHTTPAddr string `mapstructure:"oauth_http_addr"`
	PublicBaseURL string `mapstructure:"public_base_url"`
}

type LarkConfig struct {
	AppID             string `mapstructure:"app_id"`
	AppSecret         string `mapstructure:"app_secret"`
	EncryptKey        string `mapstructure:"encrypt_key"`
	VerificationToken string `mapstructure:"verification_token"`
	BotOpenID         string `mapstructure:"bot_open_id"`
}

type RedisConfig struct {
	Addr       string        `mapstructure:"addr"`
	Password   string        `mapstructure:"password"`
	DB         int           `mapstructure:"db"`
	SessionTTL time.Duration `mapstructure:"session_ttl"`
}

type LLMConfig struct {
	DefaultProvider string                  `mapstructure:"default_provider"`
	MaxSteps        int                     `mapstructure:"max_steps"`
	Providers       map[string]ModelProfile `mapstructure:"providers"`
}

// ModelProfile describes one selectable LLM provider configuration.
// See data-model.md §5.
type ModelProfile struct {
	Type           string   `mapstructure:"type"`
	Model          string   `mapstructure:"model"`
	APIKeyEnv      string   `mapstructure:"api_key_env"`
	BaseURL        string   `mapstructure:"base_url"`
	EnableThinking bool     `mapstructure:"enable_thinking"`
	MaxTokens      int      `mapstructure:"max_tokens"`
	Temperature    *float32 `mapstructure:"temperature"`
}

type MCPServerConfig struct {
	Name              string            `mapstructure:"name"`
	Enabled           bool              `mapstructure:"enabled"`
	Transport         string            `mapstructure:"transport"`
	Command           string            `mapstructure:"command"`
	Args              []string          `mapstructure:"args"`
	Env               map[string]string `mapstructure:"env"`
	URL               string            `mapstructure:"url"`
	Headers           map[string]string `mapstructure:"headers"`
	InitializeTimeout time.Duration     `mapstructure:"initialize_timeout"`
}

type OAuthConfig struct {
	EncryptionKeyEnv string   `mapstructure:"encryption_key_env"`
	StateKeyEnv      string   `mapstructure:"state_key_env"`
	Scopes           []string `mapstructure:"scopes"`
}

// Load reads path as YAML, interpolates ${VAR} env references, and unmarshals
// into a Config. Returns an error on parse failure or missing required fields.
func Load(path string) (*Config, error) {
	v := viper.New()
	v.SetConfigFile(path)
	// Allow $VAR / ${VAR} interpolation in YAML string values by expanding env.
	if err := v.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("read config %q: %w", path, err)
	}
	expandAll(v)
	setDefaults(v)

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", err)
	}
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("config invalid: %w", err)
	}
	return &cfg, nil
}

func setDefaults(v *viper.Viper) {
	v.SetDefault("server.log_level", "info")
	v.SetDefault("server.oauth_http_addr", "127.0.0.1:8080")
	v.SetDefault("redis.addr", "localhost:6379")
	v.SetDefault("redis.db", 0)
	v.SetDefault("redis.session_ttl", 24*time.Hour)
	v.SetDefault("llm.max_steps", 12)
}

// expandAll walks the settings map and replaces string values of the form
// ${VAR} (or $VAR) with os.ExpandEnv results. It skips numeric/bool values.
func expandAll(v *viper.Viper) {
	settings := v.AllSettings()
	expanded := expandMap(settings)
	// Rewrite: viper has no direct "replace settings" API, so re-apply keys.
	for k, val := range expanded {
		v.Set(k, val)
	}
}

func expandMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = expandValue(v)
	}
	return out
}

func expandValue(v any) any {
	switch vv := v.(type) {
	case string:
		return os.ExpandEnv(vv)
	case map[string]any:
		return expandMap(vv)
	case []any:
		out := make([]any, len(vv))
		for i, e := range vv {
			out[i] = expandValue(e)
		}
		return out
	default:
		return v
	}
}

// LookupToolMap returns the named tool sub-config as a string-keyed map.
func LookupToolMap(m map[string]any, key string) map[string]any {
	if m == nil {
		return nil
	}
	raw, ok := m[key]
	if !ok {
		return nil
	}
	out, _ := raw.(map[string]any)
	return out
}

// StringValue returns a string from a dynamic config map or a fallback.
func StringValue(m map[string]any, key, fallback string) string {
	if m == nil {
		return fallback
	}
	v, _ := m[key].(string)
	if v == "" {
		return fallback
	}
	return v
}

// IntValue returns an int from a dynamic config map or a fallback.
func IntValue(m map[string]any, key string, fallback int) int {
	if m == nil {
		return fallback
	}
	switch v := m[key].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	default:
		return fallback
	}
}

// validate checks critical required fields. Missing provider API keys are NOT
// fatal — the provider factory disables the profile instead (see
// internal/llm/provider.go). This keeps the bot operable in "degraded" mode.
func (c *Config) validate() error {
	if c.Lark.AppID == "" || c.Lark.AppSecret == "" {
		return fmt.Errorf("lark.app_id and lark.app_secret are required")
	}
	if c.Lark.BotOpenID == "" {
		return fmt.Errorf("lark.bot_open_id is required (used to detect @-mentions)")
	}
	if c.Redis.Addr == "" {
		return fmt.Errorf("redis.addr is required")
	}
	if c.LLM.DefaultProvider == "" {
		return fmt.Errorf("llm.default_provider is required")
	}
	if _, ok := c.LLM.Providers[c.LLM.DefaultProvider]; !ok {
		return fmt.Errorf("llm.default_provider %q is not in llm.providers", c.LLM.DefaultProvider)
	}
	if c.Server.PublicBaseURL == "" {
		return fmt.Errorf("server.public_base_url is required (used to build OAuth redirect_uri)")
	}
	return nil
}
