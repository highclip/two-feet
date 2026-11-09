package web

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestLimiterAllowsUpToLimitThenSaysWhenToRetry(t *testing.T) {
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	l := NewLimiter()
	l.now = func() time.Time { return now }

	for range 5 {
		ok, _ := l.Allow("p1", "post", 5, 10*time.Minute)
		assert.True(t, ok)
	}
	ok, retryAt := l.Allow("p1", "post", 5, 10*time.Minute)
	assert.False(t, ok)
	assert.Equal(t, now.Add(10*time.Minute), retryAt)

	other, _ := l.Allow("p2", "post", 5, 10*time.Minute)
	assert.True(t, other, "limits are per person")
	action, _ := l.Allow("p1", "join", 5, 10*time.Minute)
	assert.True(t, action, "limits are per action")

	now = now.Add(10*time.Minute + time.Second)
	ok, _ = l.Allow("p1", "post", 5, 10*time.Minute)
	assert.True(t, ok, "the window slides")
}

func TestLimiterReleaseGivesBackTheLatestAttempt(t *testing.T) {
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	l := NewLimiter()
	l.now = func() time.Time { return now }

	for range 3 {
		ok, _ := l.Allow("p1", "space", 2, time.Minute)
		if ok {
			l.Release("p1", "space")
		}
	}
	ok, _ := l.Allow("p1", "space", 1, time.Minute)
	assert.True(t, ok, "released attempts don't count")
	ok, _ = l.Allow("p1", "space", 1, time.Minute)
	assert.False(t, ok, "the one kept attempt does")

	l.Release("nobody", "space") // nothing to give back is fine
}
