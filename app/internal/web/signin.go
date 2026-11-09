package web

import (
	"net/http"
	"net/url"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/highclip/two-feet/app/internal/auth"
	"github.com/highclip/two-feet/app/internal/store"
	"github.com/highclip/two-feet/app/internal/web/views"
)

const (
	maxDisplayName = 40
	maxTags        = 3
	maxTagLength   = 32
)

// eventPage is GET /e/{slug}, the address on every QR code. Someone signed in without a name goes
// to onboarding first, carrying the code they scanned, so no one reaches a spot page nameless.
func (s *Server) eventPage(w http.ResponseWriter, r *http.Request) {
	event, ok := s.event(w, r)
	if !ok {
		return
	}
	person, signedIn := auth.Current(r.Context())
	if signedIn && person.DisplayName == "" {
		http.Redirect(w, r, onboardingURL(event.Slug, r.URL.RequestURI()), http.StatusSeeOther) // #nosec G710 -- a fixed local path; the request URI is only a query-escaped parameter
		return
	}
	switch {
	case r.URL.Query().Has("spot"):
		s.spotPage(w, r, event)
	case !signedIn:
		s.landing(w, r, event)
	default:
		s.home(w, r, event, person)
	}
}

// onboardingURL keeps the return path unless it's just the event's own page, which is where
// onboarding ends up anyway.
func onboardingURL(eventSlug, returnTo string) string {
	base := "/e/" + eventSlug + "/onboarding"
	if returnTo == "/e/"+eventSlug {
		return base
	}
	return base + "?return=" + url.QueryEscape(returnTo)
}

// signIn renders Clerk's sign-in on the page. It comes back to a same-origin path afterwards.
// A return path only survives when this page was reached from our own origin or directly: app.js
// forwards someone already signed in to it from our origin, which would let another site check
// them in at a table by linking here.
func (s *Server) signIn(w http.ResponseWriter, r *http.Request) {
	returnTo := r.URL.Query().Get("return")
	site := r.Header.Get("Sec-Fetch-Site")
	if !auth.SafeReturn(returnTo) || site == "cross-site" || site == "same-site" {
		returnTo = "/"
	}
	s.render(w, r, http.StatusOK, views.SignIn(s.page(r, store.Event{}, "Sign in"), returnTo))
}

func (s *Server) onboardingForm(w http.ResponseWriter, r *http.Request) {
	event, ok := s.event(w, r)
	if !ok {
		return
	}
	person, _ := auth.Current(r.Context())
	returnTo := returnOr(r.URL.Query().Get("return"), "/e/"+event.Slug)
	if person.DisplayName != "" {
		http.Redirect(w, r, returnTo, http.StatusSeeOther) // #nosec G710 -- returnOr only passes paths that satisfy auth.SafeReturn
		return
	}
	s.render(w, r, http.StatusOK, views.Onboarding(s.page(r, event, "Welcome"), views.OnboardingForm{
		Action: "/e/" + event.Slug + "/onboarding",
		Return: returnTo,
	}))
}

func (s *Server) onboardingSubmit(w http.ResponseWriter, r *http.Request) {
	event, ok := s.event(w, r)
	if !ok {
		return
	}
	person, _ := auth.Current(r.Context())
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "That form was too long. Shorten it and try again.", http.StatusRequestEntityTooLarge)
		return
	}

	form := views.OnboardingForm{
		Action:      "/e/" + event.Slug + "/onboarding",
		Return:      returnOr(r.PostFormValue("return"), "/e/"+event.Slug),
		DisplayName: r.PostFormValue("display_name"),
		Tags:        r.PostFormValue("tags"),
	}
	name, tags, problem := validateOnboarding(form.DisplayName, form.Tags)
	if problem != "" {
		form.Error = problem
		s.render(w, r, http.StatusUnprocessableEntity, views.Onboarding(s.page(r, event, "Welcome"), form))
		return
	}
	if _, err := s.store.CompleteOnboarding(r.Context(), person.ID, name, tags); err != nil {
		s.serverError(w, err)
		return
	}
	s.sync.Person(r.Context(), person.ID)
	http.Redirect(w, r, form.Return, http.StatusSeeOther)
}

// setDiscoverable is the home screen's "Don't show me in matches" toggle. Any signed-in person can
// change their own setting, so RequirePerson on the route is the whole role check.
func (s *Server) setDiscoverable(w http.ResponseWriter, r *http.Request) {
	event, ok := s.event(w, r)
	if !ok {
		return
	}
	person, _ := auth.Current(r.Context())
	r.Body = http.MaxBytesReader(w, r.Body, 1<<10)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "That request was too long. Reload the page and try again.", http.StatusRequestEntityTooLarge)
		return
	}
	if err := s.store.SetDiscoverable(r.Context(), person.ID, r.PostFormValue("discoverable") == "true"); err != nil {
		s.serverError(w, err)
		return
	}
	s.sync.Person(r.Context(), person.ID)
	http.Redirect(w, r, "/e/"+event.Slug, http.StatusSeeOther)
}

func returnOr(returnTo, fallback string) string {
	if auth.SafeReturn(returnTo) {
		return returnTo
	}
	return fallback
}

// validateOnboarding cleans the submitted name and tags. problem is the sentence to show the person
// when something needs fixing, and empty otherwise.
func validateOnboarding(rawName, rawTags string) (name string, tags []string, problem string) {
	name = strings.TrimSpace(rawName)
	if name == "" {
		return "", nil, "Add a name so people know who they're talking to."
	}
	if utf8.RuneCountInString(name) > maxDisplayName {
		return "", nil, "That name is over 40 characters. Try a shorter one."
	}
	if strings.ContainsFunc(name, unicode.IsControl) {
		return "", nil, "That name has characters we can't show. Use plain text."
	}

	tags = []string{}
	for raw := range strings.SplitSeq(rawTags, ",") {
		tag := strings.ToLower(strings.TrimSpace(raw))
		if tag == "" || slices.Contains(tags, tag) {
			continue
		}
		if utf8.RuneCountInString(tag) > maxTagLength || strings.ContainsFunc(tag, unicode.IsControl) {
			return "", nil, "Keep each tag to 32 plain characters."
		}
		tags = append(tags, tag)
	}
	if len(tags) > maxTags {
		return "", nil, "Pick up to 3 tags for now."
	}
	return name, tags, ""
}
