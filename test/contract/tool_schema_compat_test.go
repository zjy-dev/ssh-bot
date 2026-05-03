package contract_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/anomalyco/ssh-bot/internal/tool/builtin"
)

func TestFeishuDocReadInputSchemaIsProviderCompatible(t *testing.T) {
	toolRead := builtin.NewFeishuDocRead(builtin.FeishuDocConfig{})

	var parsed map[string]any
	require.NoError(t, json.Unmarshal(toolRead.InputSchema(), &parsed))
	require.Equal(t, "object", parsed["type"])
	require.NotContains(t, parsed, "oneOf")
	require.NotContains(t, parsed, "anyOf")
	require.NotContains(t, parsed, "allOf")
	require.NotContains(t, parsed, "enum")
	require.NotContains(t, parsed, "not")

	props, ok := parsed["properties"].(map[string]any)
	require.True(t, ok)
	require.Contains(t, props, "url")
	require.Contains(t, props, "doc_token")
}
