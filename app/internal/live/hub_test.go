package live_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/highclip/two-feet/app/internal/live"
)

func TestPublishReachesOnlyThatTopic(t *testing.T) {
	h := live.NewHub()
	event, cancelEvent := h.Subscribe(context.Background(), []string{live.EventTopic})
	defer cancelEvent()
	spot, cancelSpot := h.Subscribe(context.Background(), []string{live.SpotTopic("a")})
	defer cancelSpot()

	h.Publish(live.EventTopic, live.Fragment{Event: "feed", HTML: []byte("<p>new</p>")})

	select {
	case f := <-event:
		assert.Equal(t, "feed", f.Event)
	case <-time.After(time.Second):
		t.Fatal("event subscriber got nothing")
	}
	select {
	case f := <-spot:
		t.Fatalf("spot subscriber got %q", f.Event)
	case <-time.After(20 * time.Millisecond):
	}
}

func TestPublishNeverBlocksOnASlowClient(t *testing.T) {
	h := live.NewHub()
	_, cancel := h.Subscribe(context.Background(), []string{live.EventTopic})
	defer cancel()

	done := make(chan struct{})
	go func() {
		for range 1000 {
			h.Publish(live.EventTopic, live.Fragment{Event: "feed"})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Publish blocked on a client that isn't reading")
	}
}

func TestCancelAndContextEndCloseTheChannel(t *testing.T) {
	h := live.NewHub()
	ch, cancel := h.Subscribe(context.Background(), []string{live.EventTopic, live.SpaceTopic("a")})
	cancel()
	cancel()
	_, open := <-ch
	assert.False(t, open)

	ctx, stop := context.WithCancel(context.Background())
	ch2, cancel2 := h.Subscribe(ctx, []string{live.EventTopic})
	defer cancel2()
	stop()
	select {
	case _, open := <-ch2:
		assert.False(t, open)
	case <-time.After(time.Second):
		t.Fatal("channel stayed open after its context ended")
	}
}

func TestCloseEndsStreamsAndLaterCancelIsSafe(t *testing.T) {
	h := live.NewHub()
	ch, cancel := h.Subscribe(context.Background(), []string{live.EventTopic, live.SpotTopic("a")})
	h.Close()
	_, open := <-ch
	require.False(t, open)
	cancel()

	after, cancelAfter := h.Subscribe(context.Background(), []string{live.EventTopic})
	defer cancelAfter()
	_, open = <-after
	assert.False(t, open)
	h.Publish(live.EventTopic, live.Fragment{Event: "feed"})
}

func TestURLEscapesTopics(t *testing.T) {
	assert.Equal(t, "/e/cnh/live?topics=event%2Cspot%3Aabc", live.URL("cnh", live.EventTopic, live.SpotTopic("abc")))
}
