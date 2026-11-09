package web

import (
	"net/http"

	"github.com/highclip/two-feet/app/internal/auth"
	"github.com/highclip/two-feet/app/internal/store"
	"github.com/highclip/two-feet/app/internal/web/views"
)

func (s *Server) registerCodes(*http.ServeMux) {}

// landing is what a visitor sees at /e/{slug}. S11 replaces it with the full landing page.
func (s *Server) landing(w http.ResponseWriter, r *http.Request, event store.Event) {
	s.render(w, r, http.StatusOK, views.Landing(s.page(r, event, "Two Feet"), auth.SignInURL("/e/"+event.Slug)))
}
