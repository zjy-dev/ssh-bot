package contract_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/anomalyco/ssh-bot/internal/lark"
	"github.com/anomalyco/ssh-bot/internal/llm"
	"github.com/anomalyco/ssh-bot/internal/session"
	"github.com/anomalyco/ssh-bot/internal/tool"
	"github.com/anomalyco/ssh-bot/internal/tool/builtin"
)

func TestUS3_ClearShortCircuitsWithoutLLM(t *testing.T) {
	baseStore := session.NewMemoryStore()
	store := &trackingStore{inner: baseStore}
	seed := &session.Session{Key: "p2p:ou_cmd", UserOpenID: "ou_cmd", ChatID: "oc_p2p_cmd", ChatType: "p2p"}
	require.NoError(t, baseStore.Save(context.Background(), seed.Key, seed))

	sender := newFakeSender()
	provider := &countingProvider{name: "claude", events: []llm.StreamEvent{{Type: llm.EventMessageEnd}}}
	locker := &countingLocker{ok: false}
	h := newHandlerForTest(store, locker, sender, mustRegistry(builtin.NewDatetime()), newLLMRegistry(provider))

	ev := &lark.MessageEvent{ChatID: "oc_p2p_cmd", ChatType: "p2p", SenderOpenID: "ou_cmd", Text: "/clear"}
	require.NoError(t, h.Handle(context.Background(), ev))
	require.Equal(t, 0, provider.Calls())
	require.Equal(t, 0, locker.Calls(), "commands must run before lock acquisition")
	require.Equal(t, 1, store.DeleteCalls())
	require.Contains(t, sender.LastText(), "已清空上下文")

	got, err := baseStore.Get(context.Background(), seed.Key)
	require.NoError(t, err)
	require.Nil(t, got)
}

func TestUS3_ModelInvalidReturnsListingWithoutLLM(t *testing.T) {
	store := &trackingStore{inner: session.NewMemoryStore()}
	sender := newFakeSender()
	provider := &countingProvider{name: "claude", events: []llm.StreamEvent{{Type: llm.EventMessageEnd}}}
	locker := &countingLocker{ok: false}
	h := newHandlerForTest(store, locker, sender, mustRegistry(builtin.NewDatetime()), newLLMRegistry(provider))

	ev := &lark.MessageEvent{ChatID: "oc_p2p_cmd", ChatType: "p2p", SenderOpenID: "ou_cmd", Text: "/model invalid"}
	require.NoError(t, h.Handle(context.Background(), ev))
	require.Equal(t, 0, provider.Calls())
	require.Equal(t, 0, locker.Calls())
	require.Contains(t, sender.LastText(), "未知模型")
	require.Contains(t, sender.LastText(), "claude")
}

func TestUS3_ToolsListsAllRegisteredToolsWithoutLLM(t *testing.T) {
	store := &trackingStore{inner: session.NewMemoryStore()}
	sender := newFakeSender()
	provider := &countingProvider{name: "claude", events: []llm.StreamEvent{{Type: llm.EventMessageEnd}}}
	locker := &countingLocker{ok: false}
	reg := mustRegistry(builtin.NewDatetime(), noopTool{name: "web_search"}, noopTool{name: "web_fetch"})
	h := newHandlerForTest(store, locker, sender, reg, newLLMRegistry(provider))

	ev := &lark.MessageEvent{ChatID: "oc_p2p_cmd", ChatType: "p2p", SenderOpenID: "ou_cmd", Text: "/tools"}
	require.NoError(t, h.Handle(context.Background(), ev))
	require.Equal(t, 0, provider.Calls())
	require.Equal(t, 0, locker.Calls())
	require.Contains(t, sender.LastText(), "datetime")
	require.Contains(t, sender.LastText(), "web_search")
	require.Contains(t, sender.LastText(), "web_fetch")
}

func TestUS3_ModelSwitchPersistsWithoutInvokingLLM(t *testing.T) {
	baseStore := session.NewMemoryStore()
	store := &trackingStore{inner: baseStore}
	sender := newFakeSender()
	provider := &countingProvider{name: "claude", events: []llm.StreamEvent{{Type: llm.EventMessageEnd}}}
	openai := &countingProvider{name: "gpt", events: []llm.StreamEvent{{Type: llm.EventMessageEnd}}}
	llms := registryWithProviders(provider, openai)
	locker := &countingLocker{ok: false}
	h := newHandlerForTest(store, locker, sender, mustRegistry(builtin.NewDatetime()), llms)

	ev := &lark.MessageEvent{ChatID: "oc_p2p_cmd", ChatType: "p2p", SenderOpenID: "ou_cmd", Text: "/model gpt"}
	require.NoError(t, h.Handle(context.Background(), ev))
	require.Equal(t, 0, provider.Calls())
	require.Equal(t, 0, openai.Calls())
	require.Equal(t, 0, locker.Calls())
	require.Contains(t, sender.LastText(), "gpt")

	sess, err := baseStore.Get(context.Background(), "p2p:ou_cmd")
	require.NoError(t, err)
	require.NotNil(t, sess)
	require.Equal(t, "gpt", sess.Provider)
	require.GreaterOrEqual(t, store.GetCalls(), 1)
	require.GreaterOrEqual(t, store.SaveCalls(), 1)
}

func registryWithProviders(providers ...llm.Provider) *llm.Registry {
	profiles := make([]llm.ModelProfile, 0, len(providers))
	providerMap := make(map[string]llm.Provider, len(providers))
	for _, p := range providers {
		providerMap[p.Name()] = p
		profiles = append(profiles, llm.ModelProfile{Alias: p.Name(), Type: "fake"})
	}
	reg, err := llm.NewRegistry(context.Background(), providers[0].Name(), profiles, func(_ context.Context, p llm.ModelProfile) (llm.Provider, error) {
		return providerMap[p.Alias], nil
	}, nil)
	if err != nil {
		panic(err)
	}
	return reg
}

var _ tool.Tool = noopTool{}
