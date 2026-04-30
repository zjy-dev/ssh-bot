package session

import (
	"context"
	"sync"

	"github.com/anomalyco/ssh-bot/internal/llm"
)

// MemoryStore is an in-memory Store for unit tests (no TTL).
type MemoryStore struct {
	mu   sync.Mutex
	data map[string]*Session
}

// NewMemoryStore creates an empty in-memory store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{data: make(map[string]*Session)}
}

func (m *MemoryStore) Get(_ context.Context, key string) (*Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.data[key]
	if !ok {
		return nil, nil
	}
	return cloneSession(s), nil
}

func (m *MemoryStore) Save(_ context.Context, key string, sess *Session) error {
	if sess == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[key] = cloneSession(sess)
	return nil
}

func (m *MemoryStore) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, key)
	return nil
}

func cloneSession(s *Session) *Session {
	cp := *s
	cp.Messages = append([]llm.Message(nil), s.Messages...)
	return &cp
}
