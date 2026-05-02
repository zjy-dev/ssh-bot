package contract_test

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/anomalyco/ssh-bot/internal/lark"
	"github.com/anomalyco/ssh-bot/internal/llm"
	"github.com/anomalyco/ssh-bot/internal/session"
	"github.com/anomalyco/ssh-bot/internal/tool"
)

type countingProvider struct {
	name   string
	mu     sync.Mutex
	calls  int
	events []llm.StreamEvent
}

func (p *countingProvider) Name() string { return p.name }

func (p *countingProvider) Stream(ctx context.Context, _ llm.ChatRequest) (<-chan llm.StreamEvent, error) {
	p.mu.Lock()
	p.calls++
	events := append([]llm.StreamEvent(nil), p.events...)
	p.mu.Unlock()

	ch := make(chan llm.StreamEvent, len(events))
	go func() {
		defer close(ch)
		for _, ev := range events {
			select {
			case <-ctx.Done():
				ch <- llm.StreamEvent{Type: llm.EventError, Err: ctx.Err()}
				return
			case ch <- ev:
			}
		}
	}()
	return ch, nil
}

func (p *countingProvider) Calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

type fakeSender struct {
	mu            sync.Mutex
	textMessages  []string
	initialCards  []string
	threadReplies []string
	patchedBodies [][]byte
	nextMessageID string
}

func newFakeSender() *fakeSender {
	return &fakeSender{nextMessageID: "mid_1"}
}

func (s *fakeSender) SendInitialCard(_ context.Context, chatID string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.initialCards = append(s.initialCards, chatID)
	return s.nextMessageID, nil
}

func (s *fakeSender) SendMessage(_ context.Context, _ string, text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.textMessages = append(s.textMessages, text)
	return nil
}

func (s *fakeSender) Patch(_ context.Context, _ string, cardJSON []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make([]byte, len(cardJSON))
	copy(cp, cardJSON)
	s.patchedBodies = append(s.patchedBodies, cp)
	return nil
}

func (s *fakeSender) ReplyInThread(_ context.Context, _ string, text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.threadReplies = append(s.threadReplies, text)
	return nil
}

func (s *fakeSender) LastText() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.textMessages) == 0 {
		return ""
	}
	return s.textMessages[len(s.textMessages)-1]
}

func mustRegistry(tools ...tool.Tool) *tool.Registry {
	r := tool.NewRegistry()
	for _, tl := range tools {
		if err := r.Register(tl); err != nil {
			panic(err)
		}
	}
	return r
}

func newLLMRegistry(provider llm.Provider) *llm.Registry {
	reg, err := llm.NewRegistry(
		context.Background(),
		provider.Name(),
		[]llm.ModelProfile{{Alias: provider.Name(), Type: "fake"}},
		func(context.Context, llm.ModelProfile) (llm.Provider, error) { return provider, nil },
		nil,
	)
	if err != nil {
		panic(err)
	}
	return reg
}

type unlockedLocker struct{}

func (unlockedLocker) TryAcquire(context.Context, string, time.Duration) (string, bool, error) {
	return "tok", true, nil
}

func (unlockedLocker) Release(context.Context, string, string) error { return nil }

type blockedLocker struct{}

func (blockedLocker) TryAcquire(context.Context, string, time.Duration) (string, bool, error) {
	return "", false, nil
}

func (blockedLocker) Release(context.Context, string, string) error { return nil }

type countingLocker struct {
	mu    sync.Mutex
	calls int
	ok    bool
}

func (l *countingLocker) TryAcquire(context.Context, string, time.Duration) (string, bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls++
	return "tok", l.ok, nil
}

func (l *countingLocker) Release(context.Context, string, string) error { return nil }

func (l *countingLocker) Calls() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.calls
}

type trackingStore struct {
	inner       session.Store
	mu          sync.Mutex
	deleteCalls int
	saveCalls   int
	getCalls    int
}

func (s *trackingStore) Get(ctx context.Context, key string) (*session.Session, error) {
	s.mu.Lock()
	s.getCalls++
	s.mu.Unlock()
	return s.inner.Get(ctx, key)
}

func (s *trackingStore) Save(ctx context.Context, key string, sess *session.Session) error {
	s.mu.Lock()
	s.saveCalls++
	s.mu.Unlock()
	return s.inner.Save(ctx, key, sess)
}

func (s *trackingStore) Delete(ctx context.Context, key string) error {
	s.mu.Lock()
	s.deleteCalls++
	s.mu.Unlock()
	return s.inner.Delete(ctx, key)
}

func (s *trackingStore) DeleteCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.deleteCalls
}

func (s *trackingStore) SaveCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveCalls
}

func (s *trackingStore) GetCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.getCalls
}

type noopTool struct{ name string }

func (t noopTool) Name() string                 { return t.name }
func (t noopTool) Description() string          { return "noop" }
func (t noopTool) InputSchema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (t noopTool) Source() tool.Source          { return tool.SourceBuiltin }
func (t noopTool) Available() bool              { return true }
func (t noopTool) Call(context.Context, json.RawMessage) (tool.Result, error) {
	return tool.Result{Content: "ok"}, nil
}

func newHandlerForTest(store session.Store, locker session.Locker, sender *fakeSender, reg *tool.Registry, llms *llm.Registry) *lark.Handler {
	return lark.NewHandler(store, locker, sender, reg, llms, "ou_bot", "claude", nil)
}
