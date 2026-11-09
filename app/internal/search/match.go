package search

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/highclip/two-feet/app/internal/store"
)

// perType is how many of each kind of result a request gets (spec.search_index.query).
const perType = 3

// candidatesPerType is how many hits of each kind to check against Postgres. It's more than perType
// because the index can lag: hits Postgres rejects mustn't use up the slots of the ones below them.
const candidatesPerType = perType * 4

// matchTypes are what a post is matched against; findTypes also include posts, for the search box.
var (
	matchTypes = []string{TypeSpace, TypePerson, TypeSponsor}
	findTypes  = []string{TypeSpace, TypePerson, TypePost, TypeSponsor}
)

// Finder returns the best hits of each type, ordered best first.
type Finder interface {
	SearchEach(ctx context.Context, q Query, types []string, each int) ([]Hit, error)
}

// Match is one thing to point someone at, derived on request and never stored. WalkMinutes is set
// only on spaces and sponsors, and only when the asker has a fresh check-in: nothing about a person
// or a post may say where anyone is (D29). The struct for its Kind is the one that's set.
type Match struct {
	Kind            string
	TargetID        string
	Score           float64
	Reasons         []string
	WalkMinutes     *int
	StartsInMinutes *int
	Space           *SpaceMatch
	Person          *PersonMatch
	Sponsor         *SponsorMatch
	Post            *PostMatch
}

// SpaceMatch is a space and the asker's own signup, if any.
type SpaceMatch struct {
	store.Space
	SignupStatus string
}

// PersonMatch says who someone is and what they've posted that fits. Kind and Quote are empty when
// only their tags matched.
type PersonMatch struct {
	ID    string
	Name  string
	Kind  string
	Quote string
	Tags  []string
}

// SponsorMatch is a sponsor. Booth is nil until the venue mapping gives it one (D22).
type SponsorMatch struct {
	ID     string
	Name   string
	Solves string
	Booth  *store.Booth
}

// PostMatch is someone's post. AuthorName is empty when they've opted out of being found.
type PostMatch struct {
	ID         string
	Kind       string
	Text       string
	AuthorName string
}

// Results are matches by kind, best first.
type Results struct {
	Spaces   []Match
	People   []Match
	Posts    []Match
	Sponsors []Match
}

// Empty reports whether nothing was found.
func (r Results) Empty() bool {
	return len(r.Spaces)+len(r.People)+len(r.Posts)+len(r.Sponsors) == 0
}

// All is every match, the best of each kind first: a space, a person, a post and a sponsor, then
// the second best of each, and so on.
func (r Results) All() []Match {
	var out []Match
	lists := [][]Match{r.Spaces, r.People, r.Posts, r.Sponsors}
	for round := range perType {
		for _, list := range lists {
			if round < len(list) {
				out = append(out, list[round])
			}
		}
	}
	return out
}

// Matcher turns search hits into matches, checking each against Postgres.
type Matcher struct {
	store  *store.Store
	finder Finder
}

// NewMatcher returns a Matcher. A nil finder finds nothing.
func NewMatcher(st *store.Store, finder Finder) *Matcher {
	return &Matcher{store: st, finder: finder}
}

// ForPost finds the spaces, people and sponsors that fit one of the asker's posts.
func (m *Matcher) ForPost(ctx context.Context, asker store.Person, post store.Post, now time.Time) (Results, error) {
	return m.run(ctx, asker, post.Text, post.Tags, matchTypes, now)
}

// Find runs the same matching on typed text, and includes posts.
func (m *Matcher) Find(ctx context.Context, asker store.Person, text string, now time.Time) (Results, error) {
	return m.run(ctx, asker, text, ExtractTags(text), findTypes, now)
}

func (m *Matcher) run(ctx context.Context, asker store.Person, text string, tags, types []string, now time.Time) (Results, error) {
	if m.finder == nil || text == "" {
		return Results{}, nil
	}
	tags = withParents(tags)
	hits, err := m.finder.SearchEach(ctx, Query{Text: text, Tags: tags, ExcludeOwner: asker.ID, Now: now}, types, candidatesPerType)
	if err != nil {
		return Results{}, fmt.Errorf("find matches: %w", err)
	}
	h := hydration{store: m.store, asker: asker, text: text, tags: tags, now: now, ids: map[string][]string{}, hits: map[string][]Hit{}}
	for _, hit := range hits {
		h.ids[hit.Type] = append(h.ids[hit.Type], hit.ID)
		h.hits[hit.Type] = append(h.hits[hit.Type], hit)
	}
	return h.results(ctx)
}

// parentTags are broader tags a more specific one implies, so a question about the OTel collector
// reaches someone who only says OpenTelemetry.
var parentTags = map[string]string{"otel-collector": "otel"}

func withParents(tags []string) []string {
	out := slices.Clone(tags)
	for _, tag := range tags {
		if parent, ok := parentTags[tag]; ok && !slices.Contains(out, parent) {
			out = append(out, parent)
		}
	}
	return out
}

// hydration is one request's trip from hits to matches.
type hydration struct {
	store *store.Store
	asker store.Person
	text  string
	tags  []string
	now   time.Time
	ids   map[string][]string
	hits  map[string][]Hit
	walks map[string]int
}

func (h *hydration) results(ctx context.Context) (Results, error) {
	if zone, ok := store.FreshZone(h.asker, h.now); ok && (len(h.ids[TypeSpace]) > 0 || len(h.ids[TypeSponsor]) > 0) {
		walks, err := h.store.WalksFrom(ctx, zone)
		if err != nil {
			return Results{}, err
		}
		h.walks = walks
	}
	var r Results
	var err error
	if r.Spaces, err = h.spaces(ctx); err != nil {
		return Results{}, err
	}
	if r.People, err = h.people(ctx); err != nil {
		return Results{}, err
	}
	if r.Sponsors, err = h.sponsors(ctx); err != nil {
		return Results{}, err
	}
	if r.Posts, err = h.posts(ctx); err != nil {
		return Results{}, err
	}
	return r, nil
}

func (h *hydration) walk(zoneID string) *int {
	minutes, ok := h.walks[zoneID]
	if !ok {
		return nil
	}
	return &minutes
}

// startsIn rounds up, so that a space six minutes away doesn't read five a moment after it's seeded.
func startsIn(startsAt, now time.Time) int {
	return max(0, int(math.Ceil(startsAt.Sub(now).Minutes())))
}

func (h *hydration) spaces(ctx context.Context) ([]Match, error) {
	ids := h.ids[TypeSpace]
	if len(ids) == 0 {
		return nil, nil
	}
	spaces, err := h.store.SpacesByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	signups, err := h.store.SignupStatuses(ctx, h.asker.ID, ids)
	if err != nil {
		return nil, err
	}
	var out []Match
	for _, hit := range h.hits[TypeSpace] {
		if len(out) == perType {
			break
		}
		sp, ok := spaces[hit.ID]
		if !ok || sp.Status != store.SpaceOpen || sp.HostID == h.asker.ID ||
			!sp.EndsAt().After(h.now) || sp.StartsAt.After(h.now.Add(spaceHorizon)) {
			continue
		}
		starts := startsIn(sp.StartsAt, h.now)
		out = append(out, Match{
			Kind: TypeSpace, TargetID: sp.ID, Score: hit.Score,
			Reasons:         Reasons(h.text, h.tags, hit.Tags, sp.Title+". "+sp.Blurb),
			WalkMinutes:     h.walk(sp.ZoneID),
			StartsInMinutes: &starts,
			Space:           &SpaceMatch{Space: sp, SignupStatus: signups[sp.ID]},
		})
	}
	return out, nil
}

// bestPost is the post of someone's that shares the most with the question, the newest on a tie.
func bestPost(posts []store.Post, question []string) *store.Post {
	var best *store.Post
	bestScore := -1
	for i := range posts {
		score := 0
		for _, w := range word.FindAllString(posts[i].Text, -1) {
			if matchesQuestion(w, question) {
				score++
			}
		}
		if score > bestScore {
			best, bestScore = &posts[i], score
		}
	}
	return best
}

func unionTags(a []string, posts []store.Post) []string {
	out := append([]string{}, a...)
	for _, p := range posts {
		out = append(out, p.Tags...)
	}
	return out
}

func (h *hydration) people(ctx context.Context) ([]Match, error) {
	ids := h.ids[TypePerson]
	if len(ids) == 0 {
		return nil, nil
	}
	people, err := h.store.MatchablePeople(ctx, ids)
	if err != nil {
		return nil, err
	}
	posts, err := h.store.OpenPostsByAuthors(ctx, ids, h.now)
	if err != nil {
		return nil, err
	}
	question := questionTerms(h.text)
	var out []Match
	for _, hit := range h.hits[TypePerson] {
		if len(out) == perType {
			break
		}
		p, ok := people[hit.ID]
		if !ok || p.ID == h.asker.ID {
			continue
		}
		person := &PersonMatch{ID: p.ID, Name: p.DisplayName, Tags: p.Tags}
		text := strings.Join(p.Tags, ", ")
		if best := bestPost(posts[p.ID], question); best != nil {
			person.Kind, person.Quote, text = best.Kind, best.Text, best.Text
		}
		out = append(out, Match{
			Kind: TypePerson, TargetID: p.ID, Score: hit.Score,
			Reasons: Reasons(h.text, h.tags, unionTags(p.Tags, posts[p.ID]), text),
			Person:  person,
		})
	}
	return out, nil
}

func (h *hydration) sponsors(ctx context.Context) ([]Match, error) {
	ids := h.ids[TypeSponsor]
	if len(ids) == 0 {
		return nil, nil
	}
	sponsors, err := h.store.SponsorsWithBooths(ctx, ids)
	if err != nil {
		return nil, err
	}
	var out []Match
	for _, hit := range h.hits[TypeSponsor] {
		if len(out) == perType {
			break
		}
		row, ok := sponsors[hit.ID]
		if !ok {
			continue
		}
		m := Match{
			Kind: TypeSponsor, TargetID: row.Sponsor.ID, Score: hit.Score,
			Reasons: Reasons(h.text, h.tags, hit.Tags, row.Sponsor.Solves),
			Sponsor: &SponsorMatch{ID: row.Sponsor.ID, Name: row.Sponsor.Name, Solves: row.Sponsor.Solves, Booth: row.Booth},
		}
		if row.Booth != nil {
			m.WalkMinutes = h.walk(row.Booth.ZoneID)
			row.Booth.WalkMinutes = m.WalkMinutes
		}
		out = append(out, m)
	}
	return out, nil
}

func (h *hydration) posts(ctx context.Context) ([]Match, error) {
	ids := h.ids[TypePost]
	if len(ids) == 0 {
		return nil, nil
	}
	posts, err := h.store.OpenPostsByIDs(ctx, ids, h.now)
	if err != nil {
		return nil, err
	}
	var out []Match
	for _, hit := range h.hits[TypePost] {
		if len(out) == perType {
			break
		}
		row, ok := posts[hit.ID]
		if !ok || row.Post.AuthorID == h.asker.ID {
			continue
		}
		post := &PostMatch{ID: row.Post.ID, Kind: row.Post.Kind, Text: row.Post.Text}
		if row.Discoverable {
			post.AuthorName = row.AuthorName
		}
		out = append(out, Match{
			Kind: TypePost, TargetID: row.Post.ID, Score: hit.Score,
			Reasons: Reasons(h.text, h.tags, row.Post.Tags, row.Post.Text),
			Post:    post,
		})
	}
	return out, nil
}
