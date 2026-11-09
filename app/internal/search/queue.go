package search

import (
	"context"
	"fmt"
	"hash/fnv"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

const (
	retries               = 3
	defaultWorkers        = 4
	defaultCapacity       = 256
	defaultBackoff        = 200 * time.Millisecond
	defaultAttemptTimeout = 20 * time.Second
)

// QueueOptions tunes a Queue. Zero values take the defaults.
type QueueOptions struct {
	Workers        int
	Capacity       int // per worker
	Backoff        time.Duration
	AttemptTimeout time.Duration
}

// Queue is the Indexer the app uses. Index and Delete return at once, so a slow or unreachable
// Elasticsearch can't hold up or fail a request; workers make the calls and retry each one 3 times.
// Calls for one document always go to one worker, so they stay in order.
type Queue struct {
	inner Indexer
	log   *slog.Logger
	opts  QueueOptions

	lanes  []chan job
	done   chan struct{}
	close  sync.Once
	wg     sync.WaitGroup
	failed atomic.Bool
}

type job struct {
	ctx     context.Context // #nosec -- carries the request's trace after its cancellation is removed
	action  string
	docType string
	id      string
	run     func(context.Context) error
}

// NewQueue starts a Queue's workers. Close stops them.
func NewQueue(inner Indexer, log *slog.Logger, opts QueueOptions) *Queue {
	if opts.Workers <= 0 {
		opts.Workers = defaultWorkers
	}
	if opts.Capacity <= 0 {
		opts.Capacity = defaultCapacity
	}
	if opts.Backoff <= 0 {
		opts.Backoff = defaultBackoff
	}
	if opts.AttemptTimeout <= 0 {
		opts.AttemptTimeout = defaultAttemptTimeout
	}
	q := &Queue{inner: inner, log: log, opts: opts, done: make(chan struct{})}
	q.lanes = make([]chan job, opts.Workers)
	for i := range q.lanes {
		q.lanes[i] = make(chan job, opts.Capacity)
		q.wg.Add(1)
		go q.work(q.lanes[i])
	}
	return q
}

// Index queues a document to be written.
func (q *Queue) Index(ctx context.Context, d Doc) error {
	q.enqueue(ctx, "index", d.Type, d.ID, func(ctx context.Context) error { return q.inner.Index(ctx, d) })
	return nil
}

// Delete queues a document to be removed.
func (q *Queue) Delete(ctx context.Context, docType, id string) error {
	q.enqueue(ctx, "delete", docType, id, func(ctx context.Context) error { return q.inner.Delete(ctx, docType, id) })
	return nil
}

// Failed reports whether a write has been lost, so the index needs rebuilding from Postgres.
func (q *Queue) Failed() bool { return q.failed.Load() }

func (q *Queue) markFailed()  { q.failed.Store(true) }
func (q *Queue) clearFailed() { q.failed.Store(false) }

// Close stops taking work and waits for what's queued, until ctx ends.
func (q *Queue) Close(ctx context.Context) {
	q.close.Do(func() { close(q.done) })
	finished := make(chan struct{})
	go func() {
		q.wg.Wait()
		close(finished)
	}()
	select {
	case <-finished:
	case <-ctx.Done():
	}
}

func (q *Queue) enqueue(ctx context.Context, action, docType, id string, run func(context.Context) error) {
	select {
	case <-q.done:
		q.markFailed()
		return
	default:
	}
	j := job{ctx: context.WithoutCancel(ctx), action: action, docType: docType, id: id, run: run}
	select {
	case q.lane(docType, id) <- j:
	default:
		q.markFailed()
		q.log.Error("search queue full", "action", action, "doc_type", docType, "doc_id", id)
	}
}

func (q *Queue) lane(docType, id string) chan job {
	h := fnv.New32a()
	_, _ = fmt.Fprintf(h, "%s:%s", docType, id)
	return q.lanes[int(h.Sum32()%uint32(len(q.lanes)))] // #nosec G115 -- the modulo of a small positive length
}

func (q *Queue) work(lane chan job) {
	defer q.wg.Done()
	for {
		select {
		case j := <-lane:
			q.attempt(j)
		case <-q.done:
			for {
				select {
				case j := <-lane:
					q.attempt(j)
				default:
					return
				}
			}
		}
	}
}

// attempt makes the call and retries it 3 times. The error goes to the log and never to the person
// who posted. Errors from the Elasticsearch client hold no document text.
func (q *Queue) attempt(j job) {
	var err error
	delay := q.opts.Backoff
	for try := 0; ; try++ {
		ctx, cancel := context.WithTimeout(j.ctx, q.opts.AttemptTimeout)
		err = j.run(ctx)
		cancel()
		if err == nil {
			return
		}
		if try == retries {
			break
		}
		time.Sleep(delay)
		delay *= 2
	}
	q.markFailed()
	q.log.Error("search write failed", "action", j.action, "doc_type", j.docType, "doc_id", j.id, "err", err)
}
