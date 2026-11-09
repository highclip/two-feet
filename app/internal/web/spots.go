package web

import (
	"errors"
	"net/http"

	"github.com/highclip/two-feet/app/internal/auth"
	"github.com/highclip/two-feet/app/internal/store"
	"github.com/highclip/two-feet/app/internal/web/views"
)

func (s *Server) registerSpots(*http.ServeMux) {}

// spotPage is what a table's QR code opens. S2 adds now, next and check-in.
func (s *Server) spotPage(w http.ResponseWriter, r *http.Request, event store.Event) {
	spot, err := s.store.SpotByQRSlug(r.Context(), r.URL.Query().Get("spot"))
	if errors.Is(err, store.ErrNotFound) {
		s.render(w, r, http.StatusNotFound, views.Message(s.page(r, event, "Unknown code"),
			"That code isn't from this event. Try another code at the venue."))
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	_, signedIn := auth.Current(r.Context())
	s.render(w, r, http.StatusOK, views.SpotPlaceholder(s.page(r, event, spot.Name), spot, signedIn,
		auth.SignInURL(r.URL.RequestURI()), "/e/"+event.Slug+"/spaces/new?spot="+spot.ID))
}
