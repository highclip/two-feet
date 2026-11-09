package web

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/highclip/two-feet/app/internal/auth"
	"github.com/highclip/two-feet/app/internal/search"
	"github.com/highclip/two-feet/app/internal/store"
	"github.com/highclip/two-feet/app/internal/web/views"
)

const (
	maxSolves = 200
	// sponsorIndexTimeout bounds the reindex so an edit reaches matches, or gives up, inside the 5 seconds S7 allows.
	sponsorIndexTimeout = 4 * time.Second
)

func (s *Server) registerSponsors(mux *http.ServeMux) {
	mux.Handle("GET /e/{slug}/sponsors/{id}/edit", auth.RequirePerson(http.HandlerFunc(s.sponsorEditForm)))
	mux.Handle("POST /e/{slug}/sponsors/{id}", auth.RequirePerson(http.HandlerFunc(s.sponsorSave)))
}

func sponsorURL(eventSlug, sponsorID string) string {
	return "/e/" + eventSlug + "/sponsors/" + sponsorID
}

// sponsorEditURL is where a person edits their sponsor, and empty for anyone who has no sponsor to edit.
func sponsorEditURL(eventSlug string, p store.Person) string {
	if p.Role != store.RoleSponsorRep || p.SponsorID == nil {
		return ""
	}
	return sponsorURL(eventSlug, *p.SponsorID) + "/edit"
}

// canEditSponsor is the edit_own_sponsor permission: a sponsor_rep, and only for their own sponsor.
func canEditSponsor(p store.Person, sponsorID string) bool {
	return p.Role == store.RoleSponsorRep && p.SponsorID != nil && *p.SponsorID == sponsorID
}

// editableSponsor loads the {id} sponsor if the current person may edit it, and otherwise writes the
// response itself.
func (s *Server) editableSponsor(w http.ResponseWriter, r *http.Request) (store.Event, store.Sponsor, bool) {
	event, ok := s.event(w, r)
	if !ok {
		return store.Event{}, store.Sponsor{}, false
	}
	person, _ := auth.Current(r.Context())
	if !canEditSponsor(person, r.PathValue("id")) {
		s.render(w, r, http.StatusForbidden, views.Message(s.page(r, event, "Not yours to edit"),
			"Only a sponsor's own rep can edit its text. Head back to the home screen."))
		return store.Event{}, store.Sponsor{}, false
	}
	sponsor, err := s.store.SponsorByID(r.Context(), r.PathValue("id"))
	if err != nil {
		s.serverError(w, err)
		return store.Event{}, store.Sponsor{}, false
	}
	return event, sponsor, true
}

func sponsorForm(event store.Event, sponsor store.Sponsor) views.SponsorForm {
	return views.SponsorForm{
		Name:   sponsor.Name,
		Solves: sponsor.Solves,
		Action: sponsorURL(event.Slug, sponsor.ID),
		Home:   "/e/" + event.Slug,
	}
}

func (s *Server) sponsorEditForm(w http.ResponseWriter, r *http.Request) {
	event, sponsor, ok := s.editableSponsor(w, r)
	if !ok {
		return
	}
	form := sponsorForm(event, sponsor)
	form.Saved = r.URL.Query().Has("saved")
	s.render(w, r, http.StatusOK, views.SponsorEdit(s.page(r, event, sponsor.Name), form))
}

func (s *Server) sponsorSave(w http.ResponseWriter, r *http.Request) {
	event, sponsor, ok := s.editableSponsor(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "That form was too long. Shorten it and try again.", http.StatusRequestEntityTooLarge)
		return
	}
	solves, problem := validateSolves(r.PostFormValue("solves"))
	if problem != "" {
		form := sponsorForm(event, sponsor)
		form.Solves = r.PostFormValue("solves")
		form.Error = problem
		s.render(w, r, http.StatusUnprocessableEntity, views.SponsorEdit(s.page(r, event, sponsor.Name), form))
		return
	}
	updated, err := s.store.UpdateSponsorSolves(r.Context(), sponsor.ID, solves)
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.reindexSponsor(r.Context(), updated)
	http.Redirect(w, r, sponsorURL(event.Slug, updated.ID)+"/edit?saved=1", http.StatusSeeOther)
}

// reindexSponsor runs after the commit. A failure is logged and never fails the rep's request.
func (s *Server) reindexSponsor(ctx context.Context, sponsor store.Sponsor) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), sponsorIndexTimeout)
	defer cancel()
	zoneID := ""
	booth, err := s.store.SponsorBooth(ctx, sponsor.ID, "")
	switch {
	case err == nil:
		zoneID = booth.ZoneID
	case !errors.Is(err, store.ErrNotFound):
		s.log.Error("reindex sponsor", "sponsor", sponsor.ID, "err", err)
		return
	}
	if err := s.search.Index(ctx, search.SponsorDoc(sponsor.ID, sponsor.Name, sponsor.Solves, zoneID)); err != nil {
		s.log.Error("reindex sponsor", "sponsor", sponsor.ID, "err", err)
	}
}

// validateSolves folds the text onto one line. problem is the sentence to show the rep, and empty
// when the text is fine.
func validateSolves(raw string) (solves, problem string) {
	solves = strings.Join(strings.Fields(raw), " ")
	switch {
	case solves == "":
		return "", "Add a line about what you solve so people can find you."
	case utf8.RuneCountInString(solves) > maxSolves:
		return "", "That's over 200 characters. Try a shorter line."
	case strings.ContainsFunc(solves, unicode.IsControl):
		return "", "That has characters we can't show. Use plain text."
	}
	return solves, ""
}
