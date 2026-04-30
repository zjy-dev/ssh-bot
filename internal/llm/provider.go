package llm

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/cloudwego/eino/components/model"
)

// Factory constructs a Provider for a given ModelProfile. It's injected by
// cmd/bot at startup so that llm has no dependency on config.
type Factory func(ctx context.Context, profile ModelProfile) (Provider, error)

// ModelProfile mirrors config.ModelProfile without the import cycle.
type ModelProfile struct {
	Alias          string
	Type           string
	Model          string
	APIKeyEnv      string
	BaseURL        string
	EnableThinking bool
	MaxTokens      int
	Temperature    *float32
}

// Registry holds all configured providers keyed by their alias.
type Registry struct {
	providers map[string]Provider
	defaultP  string
}

// NewRegistry builds a Registry from a set of ModelProfiles. Profiles whose
// API-key env var is missing at startup are logged as warnings and excluded;
// the bot is operable in degraded mode as long as the default profile loads.
func NewRegistry(ctx context.Context, defaultAlias string, profiles []ModelProfile, factory Factory, logger *slog.Logger) (*Registry, error) {
	if logger == nil {
		logger = slog.Default()
	}
	r := &Registry{providers: map[string]Provider{}, defaultP: defaultAlias}
	for _, p := range profiles {
		if p.APIKeyEnv != "" && os.Getenv(p.APIKeyEnv) == "" {
			logger.Warn("llm provider disabled: api key env missing",
				"alias", p.Alias, "env", p.APIKeyEnv)
			continue
		}
		prov, err := factory(ctx, p)
		if err != nil {
			logger.Warn("llm provider failed to initialize; disabling",
				"alias", p.Alias, "err", err.Error())
			continue
		}
		r.providers[p.Alias] = prov
		logger.Info("llm provider registered", "alias", p.Alias, "type", p.Type, "model", p.Model)
	}
	if _, ok := r.providers[defaultAlias]; !ok {
		return nil, fmt.Errorf("default llm provider %q is not available (check API key env var)", defaultAlias)
	}
	return r, nil
}

// Get returns the provider registered under alias, or ok=false if absent.
func (r *Registry) Get(alias string) (Provider, bool) {
	p, ok := r.providers[alias]
	return p, ok
}

// Default returns the default-alias provider.
func (r *Registry) Default() Provider {
	return r.providers[r.defaultP]
}

// Names returns all registered provider aliases in insertion order.
// Used by /model command.
func (r *Registry) Names() []string {
	names := make([]string, 0, len(r.providers))
	for k := range r.providers {
		names = append(names, k)
	}
	return names
}

// Resolve returns the provider for alias; falls back to default when alias is
// empty or unknown. Returns (nil, false) if even the default is missing
// (defensive — should not happen after NewRegistry passes).
func (r *Registry) Resolve(alias string) (Provider, bool) {
	if alias != "" {
		if p, ok := r.providers[alias]; ok {
			return p, true
		}
	}
	p := r.Default()
	return p, p != nil
}

// Thin helper: convert a generic *model.BaseChatModel into a Provider via the
// EinoAdapter. Concrete factories call this after constructing an eino model.
func WrapEino(alias string, m model.BaseChatModel, enableThinking bool) Provider {
	return NewEinoAdapter(alias, m, enableThinking)
}
