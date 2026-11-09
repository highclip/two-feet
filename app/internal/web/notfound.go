package web

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/highclip/two-feet/app/internal/store"
	"github.com/highclip/two-feet/app/internal/web/views"
)

const notFoundText = "That page isn't here. The event home has everything that's on."

// notFound is the GET catch-all. It also gets GETs on POST-only paths, which would otherwise be a
// bare 405. htmx calls get a plain status, so a whole page can't be swapped into a fragment.
func (s *Server) notFound(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("HX-Request") != "" {
		http.NotFound(w, r)
		return
	}
	if target, ok := s.nearMiss(r.URL); ok {
		http.Redirect(w, r, target, http.StatusSeeOther) // #nosec G710 -- the path is always this event's home; the query stays on it
		return
	}
	s.render(w, r, http.StatusNotFound, views.UnknownCode(s.page(r, store.Event{}, "Not found"), notFoundText, eventURL(s.eventSlug)))
}

// nearMiss returns where a URL was clearly headed. Only this event's own URLs count, so the
// redirect can't be pointed anywhere else.
func (s *Server) nearMiss(u *url.URL) (string, bool) {
	home := eventURL(s.eventSlug)
	path := strings.TrimSuffix(u.Path, "/")
	if path == "/e" {
		return home, true
	}
	rest, ok := strings.CutPrefix(path, home)
	if !ok {
		return "", false
	}
	switch rest {
	case "":
		// A code's link carries ?spot=, which has to survive the trailing slash being dropped.
		if u.RawQuery != "" {
			return home + "?" + u.RawQuery, true
		}
		return home, true
	case "/spaces", "/posts", "/checkin", "/discoverable":
		return home, true
	}
	return "", false
}
