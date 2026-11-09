package web_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/highclip/two-feet/app/internal/search"
)

// fakeTopics stands in for the background job, so these tests never go near a news site.
type fakeTopics struct{ list []search.Topic }

func (f *fakeTopics) Current() []search.Topic { return f.list }

const newSpacePath = "/e/cnh/spaces/new"

var someTopics = []search.Topic{
	{Title: "Kubernetes 1.40 ships a new scheduler", Source: "Hacker News", Related: 3},
	{Title: "Tail sampling in the OTel collector", Source: "Hacker News", Related: 1},
	{Title: "Postgres on Kubernetes, five years in", Source: "Hacker News", Related: 0},
}

// topicStrip is the Wall's strip on its own, so a check can't be satisfied by the ticker beside it.
func topicStrip(t *testing.T, body string) string {
	t.Helper()
	_, rest, found := strings.Cut(body, `class="wall-topics"`)
	require.True(t, found, "the Wall has no topic strip")
	strip, _, found := strings.Cut(rest, "</section>")
	require.True(t, found)
	return strip
}

func TestS12_WallShowsTopicsMostRelatedFirstWithSourceAndHowManyPostsRelate(t *testing.T) {
	f := newFixture(t)
	f.topics.list = someTopics

	for _, target := range []string{wallPath, boardPath} {
		strip := topicStrip(t, f.wallBody(t, target))

		first := strings.Index(strip, "Kubernetes 1.40")
		second := strings.Index(strip, "Tail sampling")
		third := strings.Index(strip, "Postgres on Kubernetes")
		require.GreaterOrEqual(t, first, 0)
		assert.Less(t, first, second)
		assert.Less(t, second, third)
		assert.Equal(t, 3, strings.Count(strip, "wall-topic-title"))
		assert.Equal(t, 3, strings.Count(strip, "Hacker News"))
		assert.Contains(t, strip, "3 posts in the room")
		assert.Contains(t, strip, "1 post in the room")
		assert.Contains(t, strip, "0 posts in the room")
	}
}

func TestS12_StartASpaceFormOffersTheSameTopics(t *testing.T) {
	f := newFixture(t)
	f.topics.list = someTopics
	f.onboard(t, "user_a", "Ansel")

	w := f.get(newSpacePath, as("user_a"))

	require.Equal(t, http.StatusOK, w.Code)
	body := w.Body.String()
	for _, topic := range someTopics {
		assert.Contains(t, body, `data-fill-title="`+topic.Title+`"`)
	}
	assert.Contains(t, body, "3 posts in the room")
	assert.Regexp(t, `name="title"[^>]*required`, body, "the title is still an ordinary field to edit")
	assert.Equal(t, 3, strings.Count(body, `type="button" class="suggestion"`), "a tap fills the field and doesn't submit the form")
}

func TestS12_TheFormKeepsItsSuggestionsWhenASubmissionIsRefused(t *testing.T) {
	f := newFixture(t)
	f.topics.list = someTopics
	f.onboard(t, "user_a", "Ansel")
	form := f.spaceForm(t, "Long table", soon(t, 30), "30", "8")
	form.Set("title", "no")

	w := f.post("/e/cnh/spaces", form, as("user_a"))

	require.Equal(t, http.StatusUnprocessableEntity, w.Code)
	assert.Contains(t, w.Body.String(), `data-fill-title="Tail sampling in the OTel collector"`)
}

func TestS12_EditingASpaceOffersNoSuggestions(t *testing.T) {
	f := newFixture(t)
	f.topics.list = someTopics
	f.onboard(t, "user_host", "Host")
	path := f.createSpace(t, "user_host", f.spaceForm(t, "Long table", soon(t, 30), "30", "8"))

	w := f.get(path+"/edit", as("user_host"))

	require.Equal(t, http.StatusOK, w.Code)
	assert.NotContains(t, w.Body.String(), "data-fill-title")
}

func TestS12_TopicTextIsEscapedOnTheWallAndTheForm(t *testing.T) {
	f := newFixture(t)
	f.topics.list = []search.Topic{{Title: `<img src=x onerror="a()"> K8s`, Source: "Hacker News"}}
	f.onboard(t, "user_a", "Ansel")

	for name, body := range map[string]string{
		"wall": f.wallBody(t, boardPath),
		"form": f.get(newSpacePath, as("user_a")).Body.String(),
	} {
		t.Run(name, func(t *testing.T) {
			assert.Contains(t, body, "&lt;img")
			assert.NotContains(t, body, "<img src=x")
		})
	}
}

func TestS12_NoListMeansNoStripAndNoSuggestions(t *testing.T) {
	f := newFixture(t)
	f.onboard(t, "user_a", "Ansel")

	assert.NotContains(t, f.wallBody(t, wallPath), "wall-topic")
	assert.NotContains(t, f.wallBody(t, boardPath), "wall-topic")
	form := f.get(newSpacePath, as("user_a")).Body.String()
	assert.NotContains(t, form, "suggestion")
	assert.NotContains(t, form, "data-fill-title")
}

func TestS12_StripShowsNoPersonNamesAndNoPostTextFromTheRoom(t *testing.T) {
	f := newFixture(t)
	f.topics.list = someTopics
	f.onboard(t, "user_a", "Ansel Rosario")
	const postText = "our collector drops spans under load"
	require.Equal(t, http.StatusSeeOther, f.post("/e/cnh/posts",
		url.Values{"kind": {"stuck"}, "text": {postText}}, as("user_a")).Code)

	for name, opts := range map[string][]requestOpt{"visitor": nil, "signed in": {as("user_a")}} {
		t.Run(name, func(t *testing.T) {
			strip := topicStrip(t, f.wallBody(t, boardPath, opts...))
			assert.NotContains(t, strip, "Ansel")
			assert.NotContains(t, strip, "Rosario")
			assert.NotContains(t, strip, postText)
		})
	}
}

func TestS12_TheWallStillWorksBesideTheStrip(t *testing.T) {
	f := newFixture(t)
	f.topics.list = someTopics

	board := f.wallBody(t, boardPath)

	assert.NotContains(t, board, "hx-trigger", "the strip adds no listeners to the refetched fragment")
	assert.NotContains(t, board, "sse-swap")
}
