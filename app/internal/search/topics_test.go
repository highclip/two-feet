package search_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/highclip/two-feet/app/internal/search"
	"github.com/highclip/two-feet/app/internal/store"
	"github.com/highclip/two-feet/app/internal/store/storetest"
)

// fakeSource stands in for the news site: no test here reaches the network.
type fakeSource struct {
	mu       sync.Mutex
	stories  []search.Story
	err      error
	calls    int
	block    bool
	deadline time.Duration // how long the last call was given
}

func (f *fakeSource) Stories(ctx context.Context) ([]search.Story, error) {
	f.mu.Lock()
	f.calls++
	if d, ok := ctx.Deadline(); ok {
		f.deadline = time.Until(d)
	}
	stories, err, block := f.stories, f.err, f.block
	f.mu.Unlock()
	if block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return stories, err
}

func (f *fakeSource) set(stories []search.Story, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stories, f.err = stories, err
}

func (f *fakeSource) lastDeadline() time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.deadline
}

func (f *fakeSource) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// fakeRelater counts related posts by looking for a word in the story's title.
type fakeRelater struct {
	mu     sync.Mutex
	counts map[string]int
	err    error
}

func (f *fakeRelater) fail(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

func (f *fakeRelater) RelatedPosts(_ context.Context, text string, _ []string, _ time.Time) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return 0, f.err
	}
	for word, n := range f.counts {
		if strings.Contains(text, word) {
			return n, nil
		}
	}
	return 0, nil
}

func story(title string) search.Story { return search.Story{Title: title, Source: "Hacker News"} }

// inBackground starts the job in a synctest bubble and runs check once its first refresh is done.
func inBackground(t *testing.T, source *fakeSource, relater *fakeRelater, check func(topics *search.Topics)) {
	t.Helper()
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		topics := search.NewTopics(source, relater, quietLog())
		go topics.Run(ctx)
		synctest.Wait()
		check(topics)
		cancel()
		synctest.Wait()
	})
}

func titles(list []search.Topic) []string {
	out := make([]string, 0, len(list))
	for _, topic := range list {
		out = append(out, topic.Title)
	}
	return out
}

func TestS12_AFetchKeepsAtMostFiveTopicsMostRelatedPostsFirst(t *testing.T) {
	source := &fakeSource{stories: []search.Story{
		story("Kubernetes one"), story("Kubernetes two"), story("Kubernetes three"),
		story("Kubernetes four"), story("Kubernetes five"), story("Kubernetes six"),
	}}
	relater := &fakeRelater{counts: map[string]int{"six": 9, "two": 4, "four": 1}}

	inBackground(t, source, relater, func(topics *search.Topics) {
		got := topics.Current()
		require.Len(t, got, 5)
		assert.Equal(t, []string{"Kubernetes six", "Kubernetes two", "Kubernetes four", "Kubernetes one", "Kubernetes three"}, titles(got))
		assert.Equal(t, 9, got[0].Related)
		assert.Equal(t, 0, got[4].Related)
		assert.Equal(t, "Hacker News", got[0].Source)
	})
}

func TestS12_TiedTopicsKeepTheMostPopularStoryFirst(t *testing.T) {
	source := &fakeSource{stories: []search.Story{
		{Title: "Quiet Terraform story", Source: "Hacker News", Points: 2},
		{Title: "Loud Terraform story", Source: "Hacker News", Points: 300},
	}}

	inBackground(t, source, &fakeRelater{}, func(topics *search.Topics) {
		assert.Equal(t, []string{"Loud Terraform story", "Quiet Terraform story"}, titles(topics.Current()))
	})
}

func TestS12_TopicTextIsPlainAndCutTo80CharactersAndStoriesWithNoVocabularyTagAreLeftOut(t *testing.T) {
	long := "Kubernetes " + strings.Repeat("scheduling ", 20)
	source := &fakeSource{stories: []search.Story{
		story("A cosy guide to sourdough starters"),
		story("<b>Why we left</b> K8s &amp; came back"),
		story("Postgres" + string(rune(0x202e)) + "\tinternals\n<script>alert(1)</script>"),
		story(long),
	}}

	inBackground(t, source, &fakeRelater{}, func(topics *search.Topics) {
		got := titles(topics.Current())
		require.Len(t, got, 3)
		assert.NotContains(t, got, "A cosy guide to sourdough starters")
		assert.Contains(t, got, "Why we left K8s & came back")
		assert.Contains(t, got, "Postgres internals alert(1)")
		for _, title := range got {
			assert.LessOrEqual(t, utf8.RuneCountInString(title), 80, title)
			assert.NotContains(t, title, "<")
		}
		for _, title := range got {
			if strings.HasPrefix(title, "Kubernetes scheduling") {
				assert.True(t, strings.HasSuffix(title, "scheduling"), "cut at a word: %q", title)
			}
		}
	})
}

func TestS12_AVocabularyTagIsRequiredSoAnyOtherWordIsNotEnough(t *testing.T) {
	// "Scheduling" becomes a tag on a post, but it is not one of the technologies the vocabulary knows.
	source := &fakeSource{stories: []search.Story{story("Scheduling meetings across timezones")}}

	inBackground(t, source, &fakeRelater{}, func(topics *search.Topics) {
		assert.Empty(t, topics.Current())
	})
}

func TestS12_RepeatedAndTooShortHeadlinesAppearAtMostOnce(t *testing.T) {
	source := &fakeSource{stories: []search.Story{
		story("Kafka at scale"), story("kafka AT Scale"), story("Helm"),
	}}

	inBackground(t, source, &fakeRelater{}, func(topics *search.Topics) {
		assert.Equal(t, []string{"Kafka at scale"}, titles(topics.Current()), "a title under 5 characters couldn't be saved")
	})
}

func TestS12_AFailedOrEmptyFetchKeepsTheLastGoodListAndNothingWaitsOnIt(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		source := &fakeSource{stories: []search.Story{story("Kubernetes stays on the list")}}
		relater := &fakeRelater{}
		topics := search.NewTopics(source, relater, quietLog())
		go topics.Run(ctx)
		synctest.Wait()
		want := titles(topics.Current())
		require.Equal(t, []string{"Kubernetes stays on the list"}, want)

		next := func() {
			time.Sleep(search.TopicsEvery)
			synctest.Wait()
		}

		source.set(nil, errors.New("hacker news is down"))
		next()
		assert.Equal(t, want, titles(topics.Current()), "after a failure")

		source.set(nil, nil)
		next()
		assert.Equal(t, want, titles(topics.Current()), "after an empty fetch")

		source.set([]search.Story{story("A cosy guide to sourdough")}, nil)
		next()
		assert.Equal(t, want, titles(topics.Current()), "after a fetch with nothing relevant")

		source.set([]search.Story{story("Kubernetes replaces the list")}, nil)
		relater.fail(errors.New("elasticsearch is down"))
		next()
		assert.Equal(t, want, titles(topics.Current()), "when the room can't be counted")

		relater.fail(nil)
		next()
		assert.Equal(t, []string{"Kubernetes replaces the list"}, titles(topics.Current()), "a good fetch still replaces it")

		source.mu.Lock()
		source.block = true
		source.mu.Unlock()
		time.Sleep(search.TopicsEvery + time.Second)
		synctest.Wait()
		before := time.Now()
		assert.Equal(t, []string{"Kubernetes replaces the list"}, titles(topics.Current()), "while a fetch hangs")
		assert.Equal(t, before, time.Now(), "reading the list never waits on a fetch")

		cancel()
		synctest.Wait()
	})
}

func TestS12_NoListHasEverLoadedSoThereIsNothingToShow(t *testing.T) {
	source := &fakeSource{err: errors.New("hacker news is down")}

	inBackground(t, source, &fakeRelater{}, func(topics *search.Topics) {
		assert.Empty(t, topics.Current())
	})
}

func TestS12_TheJobFetchesAtStartupAndEvery15MinutesWithAFiveSecondTimeoutInTheBackground(t *testing.T) {
	assert.Equal(t, 15*time.Minute, search.TopicsEvery)

	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		source := &fakeSource{stories: []search.Story{story("Kubernetes news")}}
		topics := search.NewTopics(source, &fakeRelater{}, quietLog())

		assert.Zero(t, source.callCount(), "building the job fetches nothing")
		go topics.Run(ctx)
		synctest.Wait()
		assert.Equal(t, 1, source.callCount(), "fetches at startup")
		assert.Equal(t, 5*time.Second, source.lastDeadline())

		time.Sleep(15*time.Minute - time.Second)
		synctest.Wait()
		assert.Equal(t, 1, source.callCount(), "not before 15 minutes")

		time.Sleep(time.Second)
		synctest.Wait()
		assert.Equal(t, 2, source.callCount(), "again at 15 minutes")

		time.Sleep(15 * time.Minute)
		synctest.Wait()
		assert.Equal(t, 3, source.callCount())

		cancel()
		synctest.Wait()
	})
}

func TestS12_AFetchThatHangsIsGivenUpOnAfterFiveSeconds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		source := &fakeSource{block: true}
		topics := search.NewTopics(source, &fakeRelater{}, quietLog())
		done := make(chan struct{})
		go func() {
			defer close(done)
			topics.Run(ctx)
		}()

		start := time.Now()
		synctest.Wait()
		assert.Equal(t, start, time.Now(), "the job waits on the timeout, not the caller")
		time.Sleep(5 * time.Second)
		synctest.Wait()
		assert.Empty(t, topics.Current())

		cancel()
		<-done
	})
}

func TestS12_TheJobStopsWithItsContext(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		source := &fakeSource{stories: []search.Story{story("Kubernetes news")}}
		done := make(chan struct{})
		go func() {
			defer close(done)
			search.NewTopics(source, &fakeRelater{}, quietLog()).Run(ctx)
		}()
		synctest.Wait()
		cancel()
		<-done
	})
}

// postFinder always returns the posts it's given, as ELSER would: something, related or not.
type postFinder struct {
	ids   []string
	query search.Query
	types []string
	each  int
	err   error
}

func (f *postFinder) SearchEach(_ context.Context, q search.Query, types []string, each int) ([]search.Hit, error) {
	f.query, f.types, f.each = q, types, each
	if f.err != nil {
		return nil, f.err
	}
	hits := make([]search.Hit, 0, len(f.ids))
	for _, id := range f.ids {
		hits = append(hits, search.Hit{Type: search.TypePost, ID: id})
	}
	return hits, nil
}

func createPost(t *testing.T, st *store.Store, authorID, text string) store.Post {
	t.Helper()
	post, err := st.CreatePost(context.Background(), authorID, store.PostStuck, text, search.ExtractTags(text))
	require.NoError(t, err)
	return post
}

func TestS12_RelatedPostsCountsOpenPostsTheMatcherWouldAccept(t *testing.T) {
	st := storetest.Tx(t)
	require.NoError(t, st.Seed(context.Background(), "cnh"))
	author := newAsker(t, st, "user_topic_author", "Writer")
	finder := &postFinder{}
	for i := range 5 {
		post := createPost(t, st, author.ID, fmt.Sprintf("kubernetes upgrade number %d broke our cluster", i))
		finder.ids = append(finder.ids, post.ID)
	}
	expired := createPost(t, st, author.ID, "kubernetes upgrade expired and gone")
	storetest.Exec(t, st, `UPDATE post SET status = 'expired' WHERE id = $1`, expired.ID)
	unrelated := createPost(t, st, author.ID, "where can somebody find decent coffee nearby")
	finder.ids = append(finder.ids, expired.ID, unrelated.ID)

	got, err := search.NewMatcher(st, finder).RelatedPosts(context.Background(), "Kubernetes upgrade pitfalls", []string{"kubernetes"}, time.Now())

	require.NoError(t, err)
	assert.Equal(t, 5, got, "more than a match card's three, and only the open posts sharing something")
	assert.Equal(t, []string{search.TypePost}, finder.types)
	assert.Empty(t, finder.query.ExcludeOwner, "the whole room counts")
	assert.Greater(t, finder.each, 5)
}

func TestS12_RelatedPostsReportsASearchFailure(t *testing.T) {
	st := storetest.Tx(t)

	_, err := search.NewMatcher(st, &postFinder{err: errors.New("down")}).RelatedPosts(context.Background(), "Kubernetes", []string{"kubernetes"}, time.Now())

	require.Error(t, err)
}

func TestKubeConTopicsPassTheFilterAndStandInWhenTheSourceIsDown(t *testing.T) {
	starters, err := search.KubeConStories()
	require.NoError(t, err)
	require.Len(t, starters, 6)

	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		// Kubeflow is last in the file, so reaching the top shows every story got through the filter.
		relater := &fakeRelater{counts: map[string]int{"Kubeflow": 9}}
		topics := search.NewTopics(&fakeSource{err: errors.New("no route to host")}, relater, quietLog()).Starting(starters)
		go topics.Run(ctx)
		synctest.Wait()

		got := topics.Current()
		require.Len(t, got, 5)
		assert.Contains(t, got[0].Title, "Kubeflow")
		assert.Equal(t, 9, got[0].Related)
		assert.Equal(t, "Why Kubernetes can't tell which AI agent took an action", got[1].Title)
		cancel()
		synctest.Wait()
	})
}
