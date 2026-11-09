package web

import (
	"context"
	"math"
	"net/http"
	"sort"
	"time"

	"github.com/highclip/two-feet/app/internal/auth"
	"github.com/highclip/two-feet/app/internal/live"
	"github.com/highclip/two-feet/app/internal/search"
	"github.com/highclip/two-feet/app/internal/store"
	"github.com/highclip/two-feet/app/internal/web/views"
)

// feedWindow is how far ahead the near-me feed looks.
const feedWindow = 30 * time.Minute

func (s *Server) registerFeed(mux *http.ServeMux) {
	mux.Handle("GET /e/{slug}/feed", auth.RequirePerson(http.HandlerFunc(s.feed)))
}

// home is the signed-in home screen, with the near-me feed.
func (s *Server) home(w http.ResponseWriter, r *http.Request, event store.Event, person store.Person) {
	zone, err := s.zonePrompt(r.Context(), event, person)
	if err != nil {
		s.serverError(w, err)
		return
	}
	feed, err := s.feedView(r.Context(), event, person, s.now())
	if err != nil {
		s.serverError(w, err)
		return
	}
	search := ""
	if canSeeMatches(person) {
		search = searchURL(event.Slug)
	}
	s.render(w, r, http.StatusOK, views.Home(s.livePage(r, event, "Two Feet", live.EventTopic), person,
		"/e/"+event.Slug+"/discoverable", sponsorEditURL(event.Slug, person), search,
		s.composer(r, event, person), zone, feed))
}

// feed is the feed's body, refetched when the set of spaces changes.
func (s *Server) feed(w http.ResponseWriter, r *http.Request) {
	event, ok := s.event(w, r)
	if !ok {
		return
	}
	person, _ := auth.Current(r.Context())
	v, err := s.feedView(r.Context(), event, person, s.now())
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.render(w, r, http.StatusOK, views.FeedBody(v))
}

// feedView gathers the spaces under way or starting within feedWindow, by walk time and then start
// time. Without a fresh check-in there are no walks, so they stay in start order.
func (s *Server) feedView(ctx context.Context, e store.Event, person store.Person, now time.Time) (views.FeedView, error) {
	// A microsecond past the window, because a space starting exactly at its end is still in it.
	spaces, err := s.store.SpacesBetween(ctx, e.ID, now, now.Add(feedWindow+time.Microsecond))
	if err != nil {
		return views.FeedView{}, err
	}
	v := views.FeedView{URL: eventURL(e.Slug) + "/feed", WallURL: eventURL(e.Slug) + "/wall"}
	mayJoin := auth.HasRole(person, store.RoleAttendee, store.RoleSponsorRep)
	if mayJoin {
		v.StartURL = spacesURL(e.Slug) + "/new"
	}

	var walks map[string]int
	zone, located := store.FreshZone(person, now)
	if located {
		v.Located = true
		if walks, err = s.store.WalksFrom(ctx, zone); err != nil {
			return views.FeedView{}, err
		}
	}
	ids := make([]string, len(spaces))
	for i, sp := range spaces {
		ids[i] = sp.ID
	}
	signups, err := s.store.SignupStatuses(ctx, person.ID, ids)
	if err != nil {
		return views.FeedView{}, err
	}

	// spaces arrives in start order, and a stable sort keeps it among equal walks.
	sort.SliceStable(spaces, func(i, j int) bool {
		return walkRank(walks, spaces[i].ZoneID) < walkRank(walks, spaces[j].ZoneID)
	})
	for _, sp := range spaces {
		v.Cards = append(v.Cards, feedCard(e, sp, walks, signups[sp.ID], person, mayJoin, now))
	}
	return v, nil
}

// walkRank puts a zone with no known route after every zone with one.
func walkRank(walks map[string]int, zone string) int {
	if minutes, ok := walks[zone]; ok {
		return minutes
	}
	return math.MaxInt
}

// feedCard draws a space the way a match draws one, without the reasons: a feed card is here because
// of where and when it is.
func feedCard(e store.Event, sp store.Space, walks map[string]int, signup string, person store.Person, mayJoin bool, now time.Time) views.MatchCard {
	starts := search.StartsIn(sp.StartsAt, now)
	m := search.Match{
		Kind: search.TypeSpace, TargetID: sp.ID, StartsInMinutes: &starts,
		Space: &search.SpaceMatch{Space: sp, SignupStatus: signup},
	}
	if minutes, ok := walks[sp.ZoneID]; ok {
		m.WalkMinutes = &minutes
	}
	c := views.MatchCard{Kind: m.Kind, Key: m.Kind + ":" + m.TargetID}
	fillSpace(&c, e, m, "", now)
	page := spaceURL(e.Slug, sp.Slug)
	c.Eyebrow, c.TitleURL = sp.SpotName+" · "+sp.ZoneName, page
	switch {
	case sp.HostID == person.ID:
		c.JoinURL, c.SpaceURL, c.SpaceText = "", page, "You're hosting"
	case !mayJoin && c.JoinURL != "":
		c.JoinURL, c.SpaceURL, c.SpaceText = "", page, "See the space"
	}
	return c
}
