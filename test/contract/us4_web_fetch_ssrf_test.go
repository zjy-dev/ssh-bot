package contract_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/anomalyco/ssh-bot/internal/tool/builtin"
)

func TestUS4_WebFetchRejectsBadSchemes(t *testing.T) {
	tool := builtin.NewWebFetch(builtin.WebFetchConfig{})
	_, err := tool.Call(context.Background(), json.RawMessage(`{"url":"file:///etc/passwd"}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "scheme")
}

func TestUS4_WebFetchRejectsPrivateTargets(t *testing.T) {
	tool := builtin.NewWebFetch(builtin.WebFetchConfig{})
	_, err := tool.Call(context.Background(), json.RawMessage(`{"url":"https://127.0.0.1/secret"}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "private or loopback")
}
