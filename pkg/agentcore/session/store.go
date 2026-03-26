package session

import (
	"sync"

	"github.com/logeable/agent/pkg/agentcore/provider"
)

// Store is the session interface required by the extracted loop.
//
// What:
// It stores conversation history plus an optional summary for each session key.
//
// Why:
// Agents need memory across turns. Even this minimal extraction benefits from
// keeping storage behind an interface so we can start with memory and later swap
// in JSONL or a database without rewriting the loop.
type Store interface {
	AddMessage(sessionKey, role, content string)
	AddFullMessage(sessionKey string, msg provider.Message)
	GetHistory(sessionKey string) []provider.Message
	GetSummary(sessionKey string) string
	SetSummary(sessionKey, summary string)
}

// MemoryStore is the simplest useful Store implementation.
//
// What:
// Everything is kept in RAM in Go maps.
//
// Why:
// This is the easiest possible place to start when teaching how the loop works.
// It removes persistence concerns so readers can focus on the control flow first.
type MemoryStore struct {
	mu        sync.RWMutex
	history   map[string][]provider.Message
	summaries map[string]string
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		history:   make(map[string][]provider.Message),
		summaries: make(map[string]string),
	}
}

// AddMessage is a convenience helper for simple role/content entries.
func (s *MemoryStore) AddMessage(sessionKey, role, content string) {
	s.AddFullMessage(sessionKey, provider.Message{Role: role, Content: content})
}

// AddFullMessage appends a fully populated message to a session transcript.
func (s *MemoryStore) AddFullMessage(sessionKey string, msg provider.Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.history[sessionKey] = append(s.history[sessionKey], msg)
}

// GetHistory returns a copy rather than the internal slice.
//
// Why:
// Returning internal slices makes it easy for callers to accidentally mutate
// shared state. Copying is simpler and safer for a teaching-oriented project.
func (s *MemoryStore) GetHistory(sessionKey string) []provider.Message {
	s.mu.RLock()
	defer s.mu.RUnlock()
	history := s.history[sessionKey]
	out := make([]provider.Message, len(history))
	copy(out, history)
	return out
}

// GetSummary returns the saved summary for a session, if any.
func (s *MemoryStore) GetSummary(sessionKey string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.summaries[sessionKey]
}

// SetSummary replaces the session summary.
//
// Why:
// Summaries are a compact form of long-term memory. The minimal loop does not
// generate them yet, but the interface includes them because real agent runtimes
// almost always need this escape hatch once history grows.
func (s *MemoryStore) SetSummary(sessionKey, summary string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.summaries[sessionKey] = summary
}
