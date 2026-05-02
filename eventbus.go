package supervisor

import "sync"

// Event is a single published message fanned out to /events subscribers.
type Event struct {
	Type string // e.g. "state"
	Data []byte // marshaled payload
}

// eventBus is a minimal in-memory pub/sub for the Runner. One publisher
// (Runner.UpdateState), many subscribers (/events SSE handlers). Slow
// subscribers have their pending message dropped rather than blocking
// the publisher — state updates are already idempotent (each event
// carries the full current state), so a dropped delta is harmless.
type eventBus struct {
	mu   sync.Mutex
	subs map[chan Event]struct{}
}

func newEventBus() *eventBus {
	return &eventBus{subs: make(map[chan Event]struct{})}
}

// Subscribe returns a channel that receives future events plus a
// cancel function. The channel is buffered; if a subscriber falls
// behind, newer events are dropped rather than blocking Publish.
func (b *eventBus) Subscribe() (<-chan Event, func()) {
	ch := make(chan Event, 16)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	cancel := func() {
		b.mu.Lock()
		if _, ok := b.subs[ch]; ok {
			delete(b.subs, ch)
			close(ch)
		}
		b.mu.Unlock()
	}
	return ch, cancel
}

// Publish fans an event to all current subscribers. Non-blocking: a
// subscriber whose buffer is full loses this event.
func (b *eventBus) Publish(ev Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs {
		select {
		case ch <- ev:
		default:
			// Subscriber's buffer is full; drop.
		}
	}
}

// closeAll closes every subscriber channel. Called from Runner.Run
// on shutdown so subscribers unblock.
func (b *eventBus) closeAll() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs {
		delete(b.subs, ch)
		close(ch)
	}
}
