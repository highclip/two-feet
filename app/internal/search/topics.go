package search

import (
	"cmp"
	"context"
	"fmt"
	"html"
	"log/slog"
	"slices"
	"strings"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	// TopicsEvery is how often the list of topics is fetched again.
	TopicsEvery = 15 * time.Minute

	topicsFetchTimeout = 5 * time.Second
	// topicsRankTimeout bounds counting the room, which asks Elasticsearch once per story.
	topicsRankTimeout = time.Minute

	maxTopics        = 5
	maxTopicRunes    = 80
	minTopicRunes    = 5 // shorter than a space's title can be
	maxRankedStories = 20
)

// Story is a headline from somewhere online.
type Story struct {
	Title  string
	Source string
	Points int
}

// StorySource fetches recent stories.
type StorySource interface {
	Stories(ctx context.Context) ([]Story, error)
}

// PostRelater counts the open posts in the room that relate to some text.
type PostRelater interface {
	RelatedPosts(ctx context.Context, text string, tags []string, now time.Time) (int, error)
}

// Topic is a story people could start a space about. It says what is being discussed and how many
// posts relate to it, and has nowhere to put a name or a post's words.
type Topic struct {
	Title   string
	Source  string
	Related int
}

// TopicLister gives the current topics, most related first, without waiting on anything.
type TopicLister interface {
	Current() []Topic
}

// Topics is the list of topics and the background job that keeps it fresh. The list lives in memory
// only, and requests only ever read it.
type Topics struct {
	source   StorySource
	relater  PostRelater
	starters []Story
	log      *slog.Logger
	list     atomic.Pointer[[]Topic]
}

// NewTopics returns a job that hasn't fetched anything yet. Start it with Run.
func NewTopics(source StorySource, relater PostRelater, log *slog.Logger) *Topics {
	return &Topics{source: source, relater: relater, log: log}
}

// Starting adds stories that are always candidates, so the list has something to say even when the
// source has nothing cloud native at that moment. Call it before Run.
func (t *Topics) Starting(stories []Story) *Topics {
	t.starters = stories
	return t
}

// Current is the last good list, empty until one has loaded.
func (t *Topics) Current() []Topic {
	if list := t.list.Load(); list != nil {
		return *list
	}
	return nil
}

// Run fetches at once and then every TopicsEvery, until ctx ends.
func (t *Topics) Run(ctx context.Context) {
	t.refresh(ctx)
	ticker := time.NewTicker(TopicsEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			t.refresh(ctx)
		}
	}
}

// refresh replaces the list only when it has a whole new one. Anything less keeps the last good list.
func (t *Topics) refresh(ctx context.Context) {
	stories, err := t.fetch(ctx)
	if err != nil {
		t.log.Error("fetch topics", "err", err)
		// The starter list only stands in when there's nothing better to keep showing.
		if len(t.Current()) > 0 || len(t.starters) == 0 {
			return
		}
	}
	candidates := topicCandidates(append(slices.Clone(t.starters), stories...))
	if len(candidates) == 0 {
		t.log.Info("no new topics to show")
		return
	}
	list, err := t.rank(ctx, candidates)
	if err != nil {
		t.log.Error("rank topics", "err", err)
		return
	}
	t.list.Store(&list)
}

func (t *Topics) fetch(ctx context.Context) ([]Story, error) {
	ctx, cancel := context.WithTimeout(ctx, topicsFetchTimeout)
	defer cancel()
	return t.source.Stories(ctx)
}

func (t *Topics) rank(ctx context.Context, candidates []topicCandidate) ([]Topic, error) {
	ctx, cancel := context.WithTimeout(ctx, topicsRankTimeout)
	defer cancel()
	now := time.Now()
	list := make([]Topic, 0, len(candidates))
	for _, c := range candidates {
		related, err := t.relater.RelatedPosts(ctx, c.text, c.tags, now)
		if err != nil {
			return nil, fmt.Errorf("count posts related to a story: %w", err)
		}
		list = append(list, Topic{Title: fitTopicTitle(c.text), Source: c.source, Related: related})
	}
	// Stable, so the most popular story leads when the room is split evenly.
	slices.SortStableFunc(list, func(a, b Topic) int { return cmp.Compare(b.Related, a.Related) })
	return list[:min(len(list), maxTopics)], nil
}

// topicCandidate is a story worth counting the room for.
type topicCandidate struct {
	text   string
	source string
	tags   []string
	points int
}

// topicCandidates keeps the stories that share a vocabulary tag, once each, most popular first.
func topicCandidates(stories []Story) []topicCandidate {
	seen := map[string]bool{}
	var out []topicCandidate
	for _, s := range stories {
		text := plainText(s.Title)
		key := strings.ToLower(text)
		tags := vocabularyTags(text)
		if len(tags) == 0 || seen[key] || utf8.RuneCountInString(text) < minTopicRunes {
			continue
		}
		seen[key] = true
		out = append(out, topicCandidate{text: text, source: s.Source, tags: tags, points: s.Points})
	}
	slices.SortStableFunc(out, func(a, b topicCandidate) int { return cmp.Compare(b.points, a.points) })
	return out[:min(len(out), maxRankedStories)]
}

// plainText turns a headline from elsewhere into one line of plain text: entities decoded, markup and
// invisible characters gone, whitespace collapsed.
func plainText(s string) string {
	s = htmlTag.ReplaceAllString(html.UnescapeString(s), " ")
	s = strings.Map(func(r rune) rune {
		switch {
		case unicode.IsControl(r):
			return ' '
		case unicode.In(r, unicode.Cf, unicode.Co, unicode.Cs):
			return -1
		}
		return r
	}, s)
	return strings.Join(strings.Fields(s), " ")
}

// fitTopicTitle cuts text to what a space's title can hold, at a word so it reads cleanly.
func fitTopicTitle(text string) string {
	if utf8.RuneCountInString(text) <= maxTopicRunes {
		return text
	}
	runes := []rune(text)[:maxTopicRunes+1]
	if cut := strings.LastIndex(string(runes), " "); cut >= minTopicRunes {
		return string(runes)[:cut]
	}
	return string(runes[:maxTopicRunes])
}
