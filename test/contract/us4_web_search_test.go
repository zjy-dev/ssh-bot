package contract_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/anomalyco/ssh-bot/internal/tool/builtin"
)

func TestUS4_WebSearchRequiresQuery(t *testing.T) {
	t.Setenv("TAVILY_API_KEY", "tvly-test")
	tool := builtin.NewWebSearch(builtin.WebSearchConfig{APIKeyEnv: "TAVILY_API_KEY"})
	_, err := tool.Call(context.Background(), json.RawMessage(`{"max_results":3}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "query")
}

func TestUS4_WebSearchFormatsResultList(t *testing.T) {
	t.Setenv("TAVILY_API_KEY", "tvly-test")
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		require.Equal(t, "golang", body["query"])
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results": []map[string]any{{
				"title":   "Go",
				"url":     "https://go.dev",
				"content": "The Go programming language website.",
			}},
		})
	}))
	defer ts.Close()

	tool := builtin.NewWebSearch(builtin.WebSearchConfig{APIKeyEnv: "TAVILY_API_KEY", Endpoint: ts.URL, Client: ts.Client()})
	res, err := tool.Call(context.Background(), json.RawMessage(`{"query":"golang","max_results":1}`))
	require.NoError(t, err)
	require.Contains(t, res.Content, "1. **Go** — https://go.dev")
	require.Contains(t, res.Content, "The Go programming language website.")
}
