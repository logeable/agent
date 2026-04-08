package orchestration

import (
	"sync"
	"time"
)

const defaultEventBuffer = 16

const (
	EventJobStarted       = "job.started"
	EventJobFinished      = "job.finished"
	EventChildStarted     = "child.started"
	EventChildFinished    = "child.finished"
	EventCodeExecStarted  = "codeexec.started"
	EventCodeExecFinished = "codeexec.finished"
)

// Event is the orchestration-layer counterpart to agentcore loop events.
//
// Why:
// Delegation, automation, and code execution are higher-level runtime
// behaviors. They need their own event stream without changing agentcore's
// stable event contract.
type Event struct {
	Kind    string
	Time    time.Time
	Payload any
}

// Subscription identifies one event-bus subscriber.
type Subscription struct {
	ID uint64
	C  <-chan Event
}

type subscriber struct {
	ch chan Event
}

// EventBus is a lightweight broadcaster for orchestration events.
type EventBus struct {
	mu     sync.RWMutex
	subs   map[uint64]subscriber
	nextID uint64
	closed bool
}

func NewEventBus() *EventBus {
	return &EventBus{
		subs: make(map[uint64]subscriber),
	}
}

func (b *EventBus) Subscribe(buffer int) Subscription {
	if buffer <= 0 {
		buffer = defaultEventBuffer
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	if b.closed {
		ch := make(chan Event)
		close(ch)
		return Subscription{C: ch}
	}

	b.nextID++
	id := b.nextID
	ch := make(chan Event, buffer)
	b.subs[id] = subscriber{ch: ch}
	return Subscription{ID: id, C: ch}
}

func (b *EventBus) Unsubscribe(id uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()

	sub, ok := b.subs[id]
	if !ok {
		return
	}
	delete(b.subs, id)
	close(sub.ch)
}

func (b *EventBus) Emit(evt Event) {
	if evt.Time.IsZero() {
		evt.Time = time.Now()
	}

	b.mu.RLock()
	defer b.mu.RUnlock()

	if b.closed {
		return
	}

	for _, sub := range b.subs {
		select {
		case sub.ch <- evt:
		default:
		}
	}
}

func (b *EventBus) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.closed {
		return
	}
	b.closed = true
	for id, sub := range b.subs {
		close(sub.ch)
		delete(b.subs, id)
	}
}
