package web

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestUndoWindowOpensOnCancelAndClosesAfterAFewSeconds(t *testing.T) {
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	u := newUndoWindows()
	u.now = func() time.Time { return now }

	assert.False(t, u.open("s1"))
	u.start("s1")
	assert.True(t, u.open("s1"))

	now = now.Add(undoWindow - time.Millisecond)
	assert.True(t, u.open("s1"))
	now = now.Add(2 * time.Millisecond)
	assert.False(t, u.open("s1"))

	u.start("s2")
	assert.Len(t, u.startedAt, 1, "an expired window is dropped on the next cancel")

	u.close("s2")
	assert.False(t, u.open("s2"))
}
