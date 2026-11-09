// Package live is the one server-sent-events hub in the app. Handlers publish rendered HTML
// fragments to topics, and every open page subscribed to a topic receives them.
package live

import (
	"context"
	"net/url"
	"strings"
	"sync"
)

// EventTopic carries new and cancelled spaces, for the near-me feed and the Wall.
const EventTopic = "event"

// SpotTopic carries now and next at one spot.
func SpotTopic(id string) string { return "spot:" + id }

// SpaceTopic carries the count, waitlist and status of one space.
func SpaceTopic(id string) string { return "space:" + id }

// PersonTopic reaches only that person.
func PersonTopic(id string) string { return "person:" + id }

// URL returns the path a page connects to for these topics.
func URL(eventSlug string, topics ...string) string {
	return "/e/" + url.PathEscape(eventSlug) + "/live?topics=" + url.QueryEscape(strings.Join(topics, ","))
}

// Fragment is one piece of rendered HTML. Event is the SSE event name that sse-swap listens for.
type Fragment struct {
	Event string
	HTML  []byte
}

// subscriberBuffer is how many fragments a slow client can fall behind before it starts missing them.
const subscriberBuffer = 32

type subscriber struct {
	ch     chan Fragment
	topics []string
	closed bool // guarded by Hub.mu
}

// Hub fans fragments out to subscribers. The zero value isn't usable; call NewHub.
type Hub struct {
	mu     sync.RWMutex
	topics map[string]map[*subscriber]struct{}
	closed bool
}

// NewHub returns an empty hub.
func NewHub() *Hub {
	return &Hub{topics: map[string]map[*subscriber]struct{}{}}
}

// Publish sends a fragment to every subscriber of a topic. It never blocks: a client whose buffer
// is full misses the fragment, which is safe because pages refetch when they reconnect.
func (h *Hub) Publish(topic string, f Fragment) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for s := range h.topics[topic] {
		select {
		case s.ch <- f:
		default:
		}
	}
}

// Subscribe registers for topics. The channel closes when the context ends, cancel is called or the
// hub closes. Call cancel when finished; calling it more than once is safe.
func (h *Hub) Subscribe(ctx context.Context, topics []string) (<-chan Fragment, func()) {
	s := &subscriber{ch: make(chan Fragment, subscriberBuffer), topics: topics}

	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		close(s.ch)
		return s.ch, func() {}
	}
	for _, t := range topics {
		if h.topics[t] == nil {
			h.topics[t] = map[*subscriber]struct{}{}
		}
		h.topics[t][s] = struct{}{}
	}
	h.mu.Unlock()

	done := make(chan struct{})
	var once sync.Once
	cancel := func() {
		once.Do(func() {
			close(done)
			h.remove(s)
		})
	}
	go func() {
		select {
		case <-ctx.Done():
			cancel()
		case <-done:
		}
	}()
	return s.ch, cancel
}

// remove unsubscribes s and closes its channel, once, under the write lock so no Publish can be
// sending on it.
func (h *Hub) remove(s *subscriber) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, t := range s.topics {
		if subs, ok := h.topics[t]; ok {
			delete(subs, s)
			if len(subs) == 0 {
				delete(h.topics, t)
			}
		}
	}
	if !s.closed {
		s.closed = true
		close(s.ch)
	}
}

// Close ends every subscription, so open streams return before the server shuts down.
func (h *Hub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	for _, subs := range h.topics {
		for s := range subs {
			if !s.closed {
				s.closed = true
				close(s.ch)
			}
		}
	}
	h.topics = map[string]map[*subscriber]struct{}{}
}
