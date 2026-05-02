package contract_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/anomalyco/ssh-bot/internal/oauth"
	"github.com/anomalyco/ssh-bot/internal/tool"
	"github.com/anomalyco/ssh-bot/internal/tool/builtin"
)

func TestUS4_FeishuDocReadReturnsOAuthStartURLWhenMissingCredential(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	enc, err := oauth.NewEncryptor(testKeyB64())
	require.NoError(t, err)
	signer, err := oauth.NewStateSigner(testKeyB64())
	require.NoError(t, err)
	store := oauth.NewStore(rdb, enc)
	srv := oauth.NewServer(oauth.Config{PublicBaseURL: "https://bot.example.com", AppID: "cli", AppSecret: "secret"}, signer, store, nil)

	toolRead := builtin.NewFeishuDocRead(builtin.FeishuDocConfig{Store: store, StartURL: srv.StartURL})
	ctx := tool.WithCallerOpenID(context.Background(), "ou_alice")
	_, err = toolRead.Call(ctx, json.RawMessage(`{"doc_token":"doccn123"}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "/oauth/start?state=")
	require.Contains(t, err.Error(), "https://bot.example.com")
}

func testKeyB64() string {
	b := make([]byte, 32)
	for i := range b {
		b[i] = byte(i + 1)
	}
	return base64.StdEncoding.EncodeToString(b)
}
