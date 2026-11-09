package web

import (
	"sync"
	"time"
)

// Limiter is an in-memory sliding-window rate limiter, per person and action.
type Limiter struct {
	mu   sync.Mutex
	hits map[string][]time.Time
	now  func() time.Time
}

// NewLimiter returns an empty Limiter.
func NewLimiter() *Limiter {
	return &Limiter{hits: map[string][]time.Time{}, now: time.Now}
}

// Allow records an attempt and reports whether it's within limit attempts per window. When it
// isn't, retryAt says when the person can try again and the attempt isn't counted.
func (l *Limiter) Allow(personID, action string, limit int, window time.Duration) (ok bool, retryAt time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	key := personID + "\x00" + action
	now := l.now()
	kept := l.hits[key][:0]
	for _, t := range l.hits[key] {
		if now.Sub(t) < window {
			kept = append(kept, t)
		}
	}
	if len(kept) >= limit {
		l.hits[key] = kept
		return false, kept[len(kept)-limit].Add(window)
	}
	l.hits[key] = append(kept, now)
	return true, time.Time{}
}

// Release gives back the latest attempt, for a request that turned out to change nothing.
func (l *Limiter) Release(personID, action string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	key := personID + "\x00" + action
	if n := len(l.hits[key]); n > 0 {
		l.hits[key] = l.hits[key][:n-1]
	}
}
