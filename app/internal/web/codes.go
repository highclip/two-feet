package web

import (
	"fmt"
	"net/http"

	"github.com/highclip/two-feet/app/internal/auth"
	"github.com/highclip/two-feet/app/internal/store"
	"github.com/highclip/two-feet/app/internal/web/views"
)

const stickersPerSheet = 12

func (s *Server) registerCodes(mux *http.ServeMux) {
	mux.Handle("GET /e/{slug}/codes", auth.RequirePerson(http.HandlerFunc(s.codesPage)))
}

// landing is what a visitor sees at /e/{slug}.
func (s *Server) landing(w http.ResponseWriter, r *http.Request, event store.Event) {
	s.render(w, r, http.StatusOK, views.Landing(s.page(r, event, "Two Feet"), views.LandingView{
		EventName: event.Name,
		SignInURL: auth.SignInURL("/e/" + event.Slug),
		WallURL:   "/e/" + event.Slug + "/wall",
	}))
}

// codesPage is the organizer's print sheet (print_spot_qr_codes): one sign per spot, then the badge
// stickers. Each code encodes only a public URL, so a printed sign carries nothing about whoever
// printed it.
func (s *Server) codesPage(w http.ResponseWriter, r *http.Request) {
	event, ok := s.event(w, r)
	if !ok {
		return
	}
	person, _ := auth.Current(r.Context())
	if !auth.HasRole(person, store.RoleOrganizer) {
		s.render(w, r, http.StatusForbidden, views.Message(s.page(r, event, "Organizers only"),
			"Only organizers can print the codes. Ask one if a sign has gone missing."))
		return
	}
	sheet, err := s.buildCodes(r, event)
	if err != nil {
		s.serverError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store") // the list of every spot's code stays out of shared caches
	s.render(w, r, http.StatusOK, views.CodesPage(s.page(r, event, "QR codes · "+event.Name), sheet))
}

func (s *Server) buildCodes(r *http.Request, event store.Event) (views.CodeSheet, error) {
	spots, err := s.store.Spots(r.Context(), event.ID)
	if err != nil {
		return views.CodeSheet{}, err
	}
	sheet := views.CodeSheet{HomeURL: "/e/" + event.Slug, Stickers: stickersPerSheet}
	for _, spot := range spots {
		path, size, err := eventQRPath(s.publicURL + spotURL(event.Slug, spot.QRSlug))
		if err != nil {
			return views.CodeSheet{}, fmt.Errorf("sign for spot %s: %w", spot.ID, err)
		}
		sheet.Signs = append(sheet.Signs, views.CodeSign{Spot: spot.Name, QR: views.QR{Path: path, Size: size}})
	}
	path, size, err := eventQRPath(s.publicURL + "/e/" + event.Slug)
	if err != nil {
		return views.CodeSheet{}, fmt.Errorf("badge sticker: %w", err)
	}
	sheet.Sticker = views.QR{Path: path, Size: size}
	return sheet, nil
}
