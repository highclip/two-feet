package search_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/highclip/two-feet/app/internal/search"
)

// flaky fails its first failures calls.
type flaky struct {
	mu       sync.Mutex
	failures int
	calls    []string
}

func (f *flaky) record(call string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call)
	if f.failures > 0 {
		f.failures--
		return errors.New("connection refused")
	}
	return nil
}

func (f *flaky) Index(_ context.Context, d search.Doc) error { return f.record("index " + d.ID) }

func (f *flaky) Delete(_ context.Context, _, id string) error { return f.record("delete " + id) }

func (f *flaky) seen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func fast() search.QueueOptions { return search.QueueOptions{Backoff: time.Millisecond} }

func TestQueueRetriesAFailedCallThreeTimes(t *testing.T) {
	inner := &flaky{failures: 3}
	q := search.NewQueue(inner, quietLog(), fast())

	require.NoError(t, q.Index(context.Background(), search.Doc{Type: search.TypePost, ID: "a"}))
	q.Close(context.Background())

	assert.Len(t, inner.seen(), 4, "one call and three retries")
	assert.False(t, q.Failed())
}

func TestQueueGivesUpAfterThreeRetriesAndLogsWithoutThePostText(t *testing.T) {
	inner := &flaky{failures: 100}
	var logs bytes.Buffer
	q := search.NewQueue(inner, slog.New(slog.NewTextHandler(&logs, nil)), fast())

	err := q.Index(context.Background(), search.Doc{Type: search.TypePost, ID: "a", Text: "a very private problem"})
	require.NoError(t, err, "a failed index call never reaches the request")
	q.Close(context.Background())

	assert.Len(t, inner.seen(), 4)
	assert.True(t, q.Failed())
	assert.Contains(t, logs.String(), "post")
	assert.NotContains(t, logs.String(), "private problem")
}

func TestQueueReturnsBeforeTheCallFinishes(t *testing.T) {
	release := make(chan struct{})
	q := search.NewQueue(blocking{release}, quietLog(), fast())

	start := time.Now()
	require.NoError(t, q.Index(context.Background(), search.Doc{ID: "a"}))
	assert.Less(t, time.Since(start), 100*time.Millisecond)

	close(release)
	q.Close(context.Background())
}

type blocking struct{ release chan struct{} }

func (b blocking) Index(context.Context, search.Doc) error {
	<-b.release
	return nil
}

func (b blocking) Delete(context.Context, string, string) error { return nil }

func TestQueueKeepsCallsForOneDocumentInOrder(t *testing.T) {
	inner := &flaky{}
	q := search.NewQueue(inner, quietLog(), fast())
	ctx := context.Background()

	require.NoError(t, q.Index(ctx, search.Doc{Type: search.TypePost, ID: "same"}))
	require.NoError(t, q.Delete(ctx, search.TypePost, "same"))
	q.Close(ctx)

	assert.Equal(t, []string{"index same", "delete same"}, inner.seen())
}

func TestQueueSurvivesACancelledRequestContext(t *testing.T) {
	inner := &contextChecking{}
	q := search.NewQueue(inner, quietLog(), fast())
	ctx, cancel := context.WithCancel(context.Background())

	require.NoError(t, q.Index(ctx, search.Doc{ID: "a"}))
	cancel() // the request ended
	q.Close(context.Background())

	assert.NoError(t, inner.err)
}

type contextChecking struct{ err error }

func (c *contextChecking) Index(ctx context.Context, _ search.Doc) error {
	time.Sleep(5 * time.Millisecond)
	c.err = ctx.Err()
	return nil
}

func (c *contextChecking) Delete(context.Context, string, string) error { return nil }

func TestQueueFlushWaitsForWorkAlreadyQueued(t *testing.T) {
	release := make(chan struct{})
	q := search.NewQueue(blocking{release}, quietLog(), fast())
	t.Cleanup(func() { q.Close(context.Background()) })
	ctx := context.Background()
	require.NoError(t, q.Index(ctx, search.Doc{Type: search.TypePost, ID: "a"}))

	short, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	require.Error(t, q.Flush(short), "the write is still running")

	close(release)
	require.NoError(t, q.Flush(ctx))
}

func TestQueueFlushFailsOnceClosed(t *testing.T) {
	q := search.NewQueue(&flaky{}, quietLog(), fast())
	q.Close(context.Background())
	assert.Error(t, q.Flush(context.Background()))
}

func TestQueueRefusesNothingAfterClose(t *testing.T) {
	q := search.NewQueue(&flaky{}, quietLog(), fast())
	q.Close(context.Background())
	assert.NoError(t, q.Index(context.Background(), search.Doc{ID: "late"}))
}
