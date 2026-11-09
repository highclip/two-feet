package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/highclip/two-feet/app/internal/auth"
	"github.com/highclip/two-feet/app/internal/live"
	"github.com/highclip/two-feet/app/internal/search"
	"github.com/highclip/two-feet/app/internal/store"
	"github.com/highclip/two-feet/app/internal/telemetry"
	"github.com/highclip/two-feet/app/internal/web/views"
)

const (
	// matchTimeout bounds a search, so a cluster that has stopped answering costs a short wait and
	// then the "still looking" note instead of a hung fragment.
	matchTimeout   = 3 * time.Second
	startsSoonMins = 5
	maxSearchQuery = 200
)

func (s *Server) registerMatches(mux *http.ServeMux) {
	mux.Handle("GET /e/{slug}/posts/{id}/matches", auth.RequirePerson(http.HandlerFunc(s.postMatches)))
	mux.Handle("GET /e/{slug}/posts/{id}/matches/new", auth.RequirePerson(http.HandlerFunc(s.newPostMatches)))
	mux.Handle("GET /e/{slug}/search", auth.RequirePerson(http.HandlerFunc(s.searchPage)))
}

// canSeeMatches is the see_matches permission, which D25 also applies to search.
func canSeeMatches(p store.Person) bool {
	return auth.HasRole(p, store.RoleAttendee, store.RoleSponsorRep)
}

func searchURL(slug string) string { return "/e/" + slug + "/search" }

func matchesURL(slug, postID string) string { return postURL(slug, postID) + "/matches" }

func startFromPostURL(slug, postID string) string {
	return spacesURL(slug) + "/new?post=" + postID
}

// postIsOpen is false once a post has expired, even before the sweep has marked it.
func postIsOpen(post store.Post, now time.Time) bool {
	return post.Status == store.PostOpen && post.ExpiresAt.After(now)
}

// matchablePost loads the {id} post for its author, and otherwise writes the response itself. Anyone
// else gets the same answer as for no post at all, as on the post's page.
func (s *Server) matchablePost(w http.ResponseWriter, r *http.Request) (store.Event, store.Person, store.Post, bool) {
	event, ok := s.event(w, r)
	if !ok {
		return store.Event{}, store.Person{}, store.Post{}, false
	}
	person, _ := auth.Current(r.Context())
	post, err := s.store.PostByID(r.Context(), r.PathValue("id"))
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		s.serverError(w, err)
		return store.Event{}, store.Person{}, store.Post{}, false
	}
	if err != nil || post.AuthorID != person.ID {
		http.Error(w, "That post isn't here. Go back and try another.", http.StatusNotFound)
		return store.Event{}, store.Person{}, store.Post{}, false
	}
	if !canSeeMatches(person) {
		http.Error(w, "Matches are for attendees and sponsor reps. Sign in as one to see them.", http.StatusForbidden)
		return store.Event{}, store.Person{}, store.Post{}, false
	}
	return event, person, post, true
}

// matchPost runs the search under a span and a deadline.
func (s *Server) matchPost(ctx context.Context, person store.Person, post store.Post) (search.Results, error) {
	ctx, span := telemetry.Start(ctx, "match")
	defer span.End()
	ctx, cancel := context.WithTimeout(ctx, matchTimeout)
	defer cancel()
	return s.matcher.ForPost(ctx, person, post, time.Now())
}

// postMatches is the fragment that replaces the outlines on a post's page. A search that fails
// leaves the post itself untouched, and says to look again.
func (s *Server) postMatches(w http.ResponseWriter, r *http.Request) {
	event, person, post, ok := s.matchablePost(w, r)
	if !ok {
		return
	}
	v := views.MatchesView{StartURL: startFromPostURL(event.Slug, post.ID)}
	if !postIsOpen(post, time.Now()) {
		s.render(w, r, http.StatusOK, views.MatchList(v))
		return
	}
	results, err := s.matchPost(r.Context(), person, post)
	if err != nil {
		s.log.Error("match post", "err", err)
		v.Retry = postURL(event.Slug, post.ID)
		s.render(w, r, http.StatusOK, views.MatchList(v))
		return
	}
	v.Top, v.More = splitDeal(results, s.cardsFor(event, post.Kind, v.StartURL, "Start a space on this"))
	v.NewURL = matchesURL(event.Slug, post.ID) + "/new?seen=" + url.QueryEscape(seenKeys(v.Top, v.More))
	s.render(w, r, http.StatusOK, views.MatchList(v))
}

func seenKeys(lists ...[]views.MatchCard) string {
	var keys []string
	for _, list := range lists {
		for _, c := range list {
			keys = append(keys, c.Key)
		}
	}
	return strings.Join(keys, ",")
}

// newPostMatches counts matches the open page doesn't have yet. It only ever offers them.
func (s *Server) newPostMatches(w http.ResponseWriter, r *http.Request) {
	event, person, post, ok := s.matchablePost(w, r)
	if !ok {
		return
	}
	count := 0
	if postIsOpen(post, time.Now()) {
		results, err := s.matchPost(r.Context(), person, post)
		if err != nil {
			s.log.Error("look for new matches", "err", err)
		} else {
			seen := map[string]bool{}
			for key := range strings.SplitSeq(r.URL.Query().Get("seen"), ",") {
				seen[key] = true
			}
			for _, m := range results.All() {
				if !seen[m.Kind+":"+m.TargetID] {
					count++
				}
			}
		}
	}
	s.render(w, r, http.StatusOK, views.NewMatches(count, postURL(event.Slug, post.ID)))
}

// splitDeal puts the best of each kind in the dealt hand and folds away the rest.
func splitDeal(r search.Results, card func(search.Match) views.MatchCard) (top, more []views.MatchCard) {
	dealt := 0
	for _, list := range [][]search.Match{r.Spaces, r.People, r.Posts, r.Sponsors} {
		if len(list) > 0 {
			dealt++
		}
	}
	for i, m := range r.All() {
		if i < dealt {
			top = append(top, card(m))
		} else {
			more = append(more, card(m))
		}
	}
	return top, more
}

// cardsFor flattens matches for the views. startURL and startText are the person card's way
// forward, since people here meet in person rather than message each other.
func (s *Server) cardsFor(event store.Event, tint, startURL, startText string) func(search.Match) views.MatchCard {
	return func(m search.Match) views.MatchCard {
		c := views.MatchCard{Kind: m.Kind, Key: m.Kind + ":" + m.TargetID, Reasons: m.Reasons}
		switch {
		case m.Space != nil:
			fillSpace(&c, event, m, tint, time.Now())
		case m.Person != nil:
			c.Title, c.PostKind, c.Quote, c.Initial = m.Person.Name, m.Person.Kind, m.Person.Quote, initial(m.Person.Name)
			c.StartURL, c.StartText = startURL, startText
		case m.Sponsor != nil:
			c.Title, c.Body, c.Eyebrow = m.Sponsor.Name, m.Sponsor.Solves, "Sponsor"
			if m.Sponsor.Booth != nil {
				c.Booth = views.BoothLine(*m.Sponsor.Booth)
				c.BoothWhere = m.Sponsor.Booth.Where
			}
		case m.Post != nil:
			c.PostKind, c.Quote = m.Post.Kind, m.Post.Text
			c.ByLine = "Someone here"
			if m.Post.AuthorName != "" {
				c.ByLine = m.Post.AuthorName
			}
			c.StartURL, c.StartText = startFromPostURL(event.Slug, m.Post.ID), "Start a space on this"
		}
		return c
	}
}

func fillSpace(c *views.MatchCard, event store.Event, m search.Match, tint string, now time.Time) {
	sp := m.Space
	c.Title, c.Eyebrow = sp.Title, "Space · "+sp.SpotName
	c.PostKind = sp.Kind
	if c.PostKind == "" {
		c.PostKind = tint
	}
	if m.WalkMinutes != nil {
		c.Walk = views.WalkLabel(*m.WalkMinutes)
	}
	c.StartsAt = sp.StartsAt.UTC().Format(time.RFC3339)
	if m.StartsInMinutes != nil && *m.StartsInMinutes > 0 {
		c.Starts = fmt.Sprintf("starts in %d min", *m.StartsInMinutes)
		c.StartsSoon = *m.StartsInMinutes < startsSoonMins
	} else {
		c.Starts, c.Happening = "Happening now", true
	}
	c.SpaceID, c.Going, c.Capacity, c.Waitlist = sp.ID, sp.Going, sp.Capacity, sp.Waitlist

	page := spaceURL(event.Slug, sp.Slug)
	c.SeatCountURL = page + "/seat-count"
	switch {
	case sp.SignupStatus == store.SignupGoing:
		c.SpaceURL, c.SpaceText = page, "You're going"
	case sp.SignupStatus == store.SignupWaitlist:
		c.SpaceURL, c.SpaceText = page, "You're on the waitlist"
	case !sp.Joinable(now):
		c.SpaceURL, c.SpaceText = page, "See the space"
	default:
		c.JoinURL, c.JoinLabel = page+"/join", "Join"
		// A waitlist means seats are spoken for, even if one has just come free.
		if sp.Going >= sp.Capacity || sp.Waitlist > 0 {
			c.JoinLabel = "Join the waitlist"
		}
	}
}

// searchQuery folds what was typed onto one line and cuts it to the limit.
func searchQuery(raw string) string {
	q := strings.Join(strings.Fields(raw), " ")
	if utf8.RuneCountInString(q) > maxSearchQuery {
		return string([]rune(q)[:maxSearchQuery])
	}
	return q
}

func group(heading string, list []search.Match, card func(search.Match) views.MatchCard) []views.SearchGroup {
	if len(list) == 0 {
		return nil
	}
	g := views.SearchGroup{Heading: heading}
	for _, m := range list {
		g.Cards = append(g.Cards, card(m))
	}
	return []views.SearchGroup{g}
}

// searchPage is the search box from D25: the same hybrid query as matches, over typed text.
func (s *Server) searchPage(w http.ResponseWriter, r *http.Request) {
	event, ok := s.event(w, r)
	if !ok {
		return
	}
	person, _ := auth.Current(r.Context())
	if person.DisplayName == "" {
		http.Redirect(w, r, onboardingURL(event.Slug, r.URL.RequestURI()), http.StatusSeeOther) // #nosec G710 -- a fixed local path; the request URI is only a query-escaped parameter
		return
	}
	if !canSeeMatches(person) {
		s.render(w, r, http.StatusForbidden, views.Message(s.page(r, event, "Not for organizers"),
			"Search is for attendees and sponsor reps. Sign in as one to use it."))
		return
	}
	v := views.SearchView{
		Query: searchQuery(r.URL.Query().Get("q")), Action: searchURL(event.Slug), Home: "/e/" + event.Slug,
		StartURL: spacesURL(event.Slug) + "/new",
	}
	if v.Query != "" {
		ctx, span := telemetry.Start(r.Context(), "match")
		ctx, cancel := context.WithTimeout(ctx, matchTimeout)
		results, err := s.matcher.Find(ctx, person, v.Query, time.Now())
		cancel()
		span.End()
		if err != nil {
			s.log.Error("search", "err", err)
			v.Failed = true
			v.Retry = v.Action + "?q=" + url.QueryEscape(v.Query)
		} else {
			card := s.cardsFor(event, "", v.StartURL, "Start a space")
			v.Groups = append(v.Groups, group("Spaces", results.Spaces, card)...)
			v.Groups = append(v.Groups, group("People", results.People, card)...)
			v.Groups = append(v.Groups, group("Posts", results.Posts, card)...)
			v.Groups = append(v.Groups, group("Sponsors", results.Sponsors, card)...)
		}
	}
	s.render(w, r, http.StatusOK, views.SearchPage(s.livePage(r, event, "Search", live.EventTopic), v))
}
