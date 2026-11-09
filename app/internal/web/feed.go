package web

import (
	"net/http"

	"github.com/highclip/two-feet/app/internal/store"
	"github.com/highclip/two-feet/app/internal/web/views"
)

func (s *Server) registerFeed(*http.ServeMux) {}

// home is the signed-in home screen. S8 replaces it with the near-me feed.
func (s *Server) home(w http.ResponseWriter, r *http.Request, event store.Event, person store.Person) {
	s.render(w, r, http.StatusOK, views.Home(s.page(r, event, "Two Feet"), person,
		"/e/"+event.Slug+"/spaces/new", "/e/"+event.Slug+"/discoverable", s.composer(r, event, person)))
}
