package web

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/highclip/two-feet/app/internal/auth"
	"github.com/highclip/two-feet/app/internal/live"
	"github.com/highclip/two-feet/app/internal/search"
	"github.com/highclip/two-feet/app/internal/store"
	"github.com/highclip/two-feet/app/internal/web/views"
)

const (
	minTitle        = 5
	maxTitle        = 80
	maxBlurb        = 280
	minCapacity     = 2
	spaceLimit      = 5
	spaceWindow     = 10 * time.Minute
	spaceAction     = "create_space"
	startGrace      = 5 * time.Minute // a host can start a space they've just sat down in
	formTime        = "2006-01-02T15:04"
	maxSpaceFormLen = 8 << 10

	// undoWindow is the 5 seconds the toast shows, plus room for a slow network.
	undoWindow = 8 * time.Second
)

var uuidShape = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func (s *Server) registerSpaces(mux *http.ServeMux) {
	mux.Handle("GET /e/{slug}/spaces/new", auth.RequirePerson(http.HandlerFunc(s.newSpaceForm)))
	mux.Handle("POST /e/{slug}/spaces", auth.RequirePerson(http.HandlerFunc(s.createSpace)))
	mux.HandleFunc("GET /e/{slug}/spaces/{space}", s.spacePage)
	mux.HandleFunc("GET /e/{slug}/spaces/{space}/seats", s.spaceSeats)
	mux.Handle("GET /e/{slug}/spaces/{space}/edit", auth.RequirePerson(http.HandlerFunc(s.editSpaceForm)))
	mux.Handle("POST /e/{slug}/spaces/{space}/edit", auth.RequirePerson(http.HandlerFunc(s.updateSpace)))
	mux.Handle("POST /e/{slug}/spaces/{space}/cancel", auth.RequirePerson(http.HandlerFunc(s.cancelSpace)))
	mux.Handle("POST /e/{slug}/spaces/{space}/undo", auth.RequirePerson(http.HandlerFunc(s.undoCancelSpace)))
	s.registerSignups(mux)
}

// undoWindows remembers what happened moments ago: which spaces were cancelled, who left, and what
// their toast said. It lives in memory, like the live hub: losing it in a restart only ends an Undo
// that had seconds left.
type undoWindows struct {
	mu        sync.Mutex
	startedAt map[string]undoEntry
	now       func() time.Time
}

type undoEntry struct {
	at   time.Time
	note string
}

func newUndoWindows() *undoWindows {
	return &undoWindows{startedAt: map[string]undoEntry{}, now: time.Now}
}

func (u *undoWindows) start(spaceID string) { u.startNote(spaceID, "") }

// startNote opens a window that carries a line of text for a page loaded while it's open.
func (u *undoWindows) startNote(key, note string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	now := u.now()
	for id, entry := range u.startedAt {
		if now.Sub(entry.at) >= undoWindow {
			delete(u.startedAt, id)
		}
	}
	u.startedAt[key] = undoEntry{at: now, note: note}
}

func (u *undoWindows) open(spaceID string) bool {
	_, ok := u.entry(spaceID)
	return ok
}

// note returns the window's text, or "" once it has closed.
func (u *undoWindows) note(key string) string {
	entry, _ := u.entry(key)
	return entry.note
}

func (u *undoWindows) entry(key string) (undoEntry, bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	entry, ok := u.startedAt[key]
	if !ok || u.now().Sub(entry.at) >= undoWindow {
		return undoEntry{}, false
	}
	return entry, true
}

func (u *undoWindows) close(spaceID string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	delete(u.startedAt, spaceID)
}

func spacesURL(eventSlug string) string { return "/e/" + eventSlug + "/spaces" }

func spaceURL(eventSlug, spaceSlug string) string {
	return spacesURL(eventSlug) + "/" + spaceSlug
}

func eventLocation(e store.Event) *time.Location {
	if loc, err := time.LoadLocation(e.Timezone); err == nil {
		return loc
	}
	return time.UTC
}

func canManageSpace(p store.Person, sp store.Space) bool {
	return p.ID == sp.HostID || auth.HasRole(p, store.RoleOrganizer)
}

func (s *Server) forbidden(w http.ResponseWriter, r *http.Request, e store.Event, text string) {
	s.render(w, r, http.StatusForbidden, views.Message(s.page(r, e, "Not yours to change"), text))
}

// mayCreateSpace is the create_space permission. Someone who hasn't chosen a name yet is sent to
// choose one when loading a page; a form post from them is refused.
func (s *Server) mayCreateSpace(w http.ResponseWriter, r *http.Request, e store.Event, p store.Person) bool {
	if p.DisplayName == "" {
		if r.Method == http.MethodGet {
			http.Redirect(w, r, onboardingURL(e.Slug, r.URL.RequestURI()), http.StatusSeeOther) // #nosec G710 -- a fixed local path; the request URI is only a query-escaped parameter
			return false
		}
		s.forbidden(w, r, e, "Choose a name first, then start your space.")
		return false
	}
	if !auth.HasRole(p, store.RoleAttendee, store.RoleSponsorRep) {
		s.forbidden(w, r, e, "Organizers run the room, so spaces are started by attendees and sponsor reps. Ask one of them to start it.")
		return false
	}
	return true
}

// spaceForRequest loads the {space} in the {slug} event, writing the 404 itself.
func (s *Server) spaceForRequest(w http.ResponseWriter, r *http.Request, e store.Event) (store.Space, bool) {
	sp, err := s.store.SpaceBySlug(r.Context(), e.ID, r.PathValue("space"))
	if errors.Is(err, store.ErrNotFound) {
		s.render(w, r, http.StatusNotFound, views.Message(s.page(r, e, "Not found"),
			"That space isn't here. Check the link or look at what's on near you."))
		return store.Space{}, false
	}
	if err != nil {
		s.serverError(w, err)
		return store.Space{}, false
	}
	return sp, true
}

// managedSpace loads the event, space and person for a host or organizer action, enforcing
// edit_or_cancel_space.
func (s *Server) managedSpace(w http.ResponseWriter, r *http.Request) (store.Event, store.Space, store.Person, bool) {
	e, ok := s.event(w, r)
	if !ok {
		return store.Event{}, store.Space{}, store.Person{}, false
	}
	sp, ok := s.spaceForRequest(w, r, e)
	if !ok {
		return store.Event{}, store.Space{}, store.Person{}, false
	}
	person, _ := auth.Current(r.Context())
	if !canManageSpace(person, sp) {
		s.forbidden(w, r, e, "Only this space's host or an organizer can change it. Ask them if something needs fixing.")
		return store.Event{}, store.Space{}, store.Person{}, false
	}
	return e, sp, person, true
}

func (s *Server) spotOptions(ctx context.Context, e store.Event) ([]views.SpotOption, []store.Spot, error) {
	spots, err := s.store.Spots(ctx, e.ID)
	if err != nil {
		return nil, nil, err
	}
	zones, err := s.store.ZoneNames(ctx, e.ID)
	if err != nil {
		return nil, nil, err
	}
	options := make([]views.SpotOption, 0, len(spots))
	for _, spot := range spots {
		options = append(options, views.SpotOption{ID: spot.ID, Label: views.SpotLabel(spot, zones[spot.ZoneID])})
	}
	return options, spots, nil
}

func newSpaceBlank(e store.Event) views.SpaceForm {
	return views.SpaceForm{
		Action:  spacesURL(e.Slug),
		Heading: "Start a space",
		Lede:    "Whoever comes are the right people.",
		Submit:  "Start this space",
		Minutes: "30",
	}
}

// newSpaceForm is the create form, linked to with ?spot=<id>&starts=<RFC3339>&post=<id>.
func (s *Server) newSpaceForm(w http.ResponseWriter, r *http.Request) {
	e, ok := s.event(w, r)
	if !ok {
		return
	}
	person, _ := auth.Current(r.Context())
	if !s.mayCreateSpace(w, r, e, person) {
		return
	}
	options, _, err := s.spotOptions(r.Context(), e)
	if err != nil {
		s.serverError(w, err)
		return
	}
	loc := eventLocation(e)
	form := newSpaceBlank(e)
	form.Spots = options
	form.SpotID = r.URL.Query().Get("spot")
	starts := time.Now().Truncate(15 * time.Minute).Add(15 * time.Minute)
	if t, err := time.Parse(time.RFC3339, r.URL.Query().Get("starts")); err == nil {
		starts = t
	}
	form.Starts = starts.In(loc).Format(formTime)
	if minutes := r.URL.Query().Get("minutes"); minutes == "15" || minutes == "30" || minutes == "45" {
		form.Minutes = minutes
	}
	if post := r.URL.Query().Get("post"); uuidShape.MatchString(post) {
		summary, err := s.store.PostForSpace(r.Context(), post, time.Now())
		switch {
		case err == nil:
			form.PostID = post
			form.Title = titleFromPost(summary.Text)
		case !errors.Is(err, store.ErrNotFound):
			s.serverError(w, err)
			return
		}
	}
	s.render(w, r, http.StatusOK, views.SpaceFormPage(s.page(r, e, "Start a space"), form))
}

// titleFromPost fits a post into a title, cutting at a word so it reads cleanly.
func titleFromPost(text string) string {
	collapsed := strings.Join(strings.Fields(text), " ")
	if utf8.RuneCountInString(collapsed) <= maxTitle {
		return collapsed
	}
	runes := []rune(collapsed)[:maxTitle+1]
	if cut := strings.LastIndex(string(runes), " "); cut >= minTitle {
		return string(runes)[:cut]
	}
	return string(runes[:maxTitle])
}

// readSpaceForm copies a submitted form into a views.SpaceForm. It reports false if it had to answer
// the request itself.
func (s *Server) readSpaceForm(w http.ResponseWriter, r *http.Request, form *views.SpaceForm) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxSpaceFormLen)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "That form was too long. Shorten it and try again.", http.StatusRequestEntityTooLarge)
		return false
	}
	form.Title = r.PostFormValue("title")
	form.Blurb = r.PostFormValue("blurb")
	form.SpotID = r.PostFormValue("spot")
	form.Starts = r.PostFormValue("starts")
	form.Minutes = r.PostFormValue("minutes")
	form.Capacity = r.PostFormValue("capacity")
	form.PostID = r.PostFormValue("post")
	return true
}

// validateSpace turns a form into a store.SpaceInput. problem is the sentence to show the person.
func validateSpace(form views.SpaceForm, spots []store.Spot, loc *time.Location) (in store.SpaceInput, problem string) {
	in.Title = strings.TrimSpace(form.Title)
	if n := utf8.RuneCountInString(in.Title); n < minTitle || n > maxTitle {
		return in, "Give it a title of 5 to 80 characters."
	}
	if strings.ContainsFunc(in.Title, unicode.IsControl) {
		return in, "That title has characters we can't show. Use plain text."
	}
	in.Blurb = strings.TrimSpace(strings.ReplaceAll(form.Blurb, "\r\n", "\n"))
	if utf8.RuneCountInString(in.Blurb) > maxBlurb {
		return in, "Keep your line to 280 characters."
	}
	if strings.ContainsFunc(in.Blurb, func(c rune) bool { return unicode.IsControl(c) && c != '\n' }) {
		return in, "That line has characters we can't show. Use plain text."
	}
	found := false
	for _, spot := range spots {
		if spot.ID == form.SpotID {
			found = true
			break
		}
	}
	if !found {
		return in, "Pick a spot from the list."
	}
	in.SpotID = form.SpotID
	starts, err := time.ParseInLocation(formTime, form.Starts, loc)
	if err != nil {
		return in, "Pick a start time."
	}
	in.StartsAt = starts
	switch form.Minutes {
	case "15", "30", "45":
		in.Minutes, _ = strconv.Atoi(form.Minutes) // #nosec G104 -- one of the three literals above
	default:
		return in, "Pick 15, 30 or 45 minutes."
	}
	if raw := strings.TrimSpace(form.Capacity); raw != "" {
		capacity, err := strconv.Atoi(raw)
		if err != nil || capacity < minCapacity {
			return in, "Capacity needs to be at least 2."
		}
		in.Capacity = capacity
	}
	return in, ""
}

// refusal turns a store refusal into the status and sentence to show, or reports false for an
// error that isn't a refusal.
func refusal(err error, loc *time.Location) (status int, text string, ok bool) {
	var taken *store.SpotTakenError
	var over *store.OverCapacityError
	var below *store.BelowGoingError
	switch {
	case errors.As(err, &taken):
		next := "Pick another time."
		if taken.Alternative != "" {
			next = "Try " + taken.Alternative + "."
		}
		return http.StatusConflict, fmt.Sprintf("%s is taken until %s. %s", taken.Spot, taken.Until.In(loc).Format("3:04"), next), true
	case errors.As(err, &over):
		return http.StatusUnprocessableEntity, fmt.Sprintf("%s seats %d. Pick %d or fewer.", over.Spot, over.Seats, over.Seats), true
	case errors.As(err, &below):
		return http.StatusUnprocessableEntity, fmt.Sprintf("%d people are going. Keep the capacity at %d or more.", below.Going, below.Going), true
	}
	return 0, "", false
}

func (s *Server) createSpace(w http.ResponseWriter, r *http.Request) {
	e, ok := s.event(w, r)
	if !ok {
		return
	}
	person, _ := auth.Current(r.Context())
	if !s.mayCreateSpace(w, r, e, person) {
		return
	}
	options, spots, err := s.spotOptions(r.Context(), e)
	if err != nil {
		s.serverError(w, err)
		return
	}
	form := newSpaceBlank(e)
	if !s.readSpaceForm(w, r, &form) {
		return
	}
	form.Spots = options
	loc := eventLocation(e)
	page := s.page(r, e, "Start a space")
	refuse := func(status int, text string) {
		form.Error = text
		s.render(w, r, status, views.SpaceFormPage(page, form))
	}

	in, problem := validateSpace(form, spots, loc)
	if problem == "" && in.StartsAt.Before(time.Now().Add(-startGrace)) {
		problem = "That time has passed. Pick a later one."
	}
	if problem != "" {
		refuse(http.StatusUnprocessableEntity, problem)
		return
	}

	var tags []string
	if uuidShape.MatchString(form.PostID) {
		summary, err := s.store.PostForSpace(r.Context(), form.PostID, time.Now())
		switch {
		case err == nil:
			in.FromPostID = &form.PostID
			tags = summary.Tags
		case !errors.Is(err, store.ErrNotFound):
			s.serverError(w, err)
			return
		}
	}

	allowed, retryAt := s.limiter.Allow(person.ID, spaceAction, spaceLimit, spaceWindow)
	if !allowed {
		refuse(http.StatusTooManyRequests, fmt.Sprintf("You've started a lot of spaces just now. You can start another at %s.", retryAt.In(loc).Format("3:04")))
		return
	}
	space, err := s.store.CreateSpace(r.Context(), person.ID, in)
	if err != nil {
		s.limiter.Release(person.ID, spaceAction)
		if status, text, ok := refusal(err, loc); ok {
			refuse(status, text)
			return
		}
		s.serverError(w, err)
		return
	}

	s.indexSpace(r.Context(), space, tags)
	s.publishSpaceChanged(r.Context(), space.ID)
	s.publishSpacesListChanged(space.SpotID)
	http.Redirect(w, r, spaceURL(e.Slug, space.Slug), http.StatusSeeOther) // #nosec G710 -- the slug is generated by the server
}

func (s *Server) spaceView(ctx context.Context, e store.Event, sp store.Space, viewer *store.Person) (views.SpaceView, error) {
	v := views.SpaceView{
		Space:    sp,
		Now:      time.Now(),
		Location: eventLocation(e),
		BaseURL:  spaceURL(e.Slug, sp.Slug),
		SeatsURL: spaceURL(e.Slug, sp.Slug) + "/seats",
	}
	if viewer == nil {
		v.SignInURL = auth.SignInURL(v.BaseURL)
		return v, nil
	}
	going, err := s.store.GoingTo(ctx, sp.ID)
	if err != nil {
		return views.SpaceView{}, err
	}
	v.Going = going
	v.ShowNames = true
	v.CanManage = canManageSpace(*viewer, sp)
	v.IsHost = viewer.ID == sp.HostID
	v.CanJoin = auth.HasRole(*viewer, store.RoleAttendee, store.RoleSponsorRep)
	if viewer.DisplayName == "" {
		v.SignInURL = onboardingURL(e.Slug, v.BaseURL)
	}
	if v.Signup, err = s.store.SignupOf(ctx, sp.ID, viewer.ID); err != nil {
		return views.SpaceView{}, err
	}
	return v, nil
}

func currentPerson(ctx context.Context) *store.Person {
	if person, ok := auth.Current(ctx); ok {
		return &person
	}
	return nil
}

// spacePage is public. Only signed-in people see who is coming (see_space_attendee_names).
func (s *Server) spacePage(w http.ResponseWriter, r *http.Request) {
	e, ok := s.event(w, r)
	if !ok {
		return
	}
	sp, ok := s.spaceForRequest(w, r, e)
	if !ok {
		return
	}
	viewer := currentPerson(r.Context())
	v, err := s.spaceView(r.Context(), e, sp, viewer)
	if err != nil {
		s.serverError(w, err)
		return
	}
	undo := v.CanManage && sp.Status == store.SpaceCancelled && s.undo.open(sp.ID)
	if viewer != nil {
		v.Toast = s.toastAfterRedirect(e, sp, viewer.ID)
	}
	s.render(w, r, http.StatusOK, views.SpacePage(s.livePage(r, e, sp.Title, spaceTopic(sp)), v, undo, spacesURL(e.Slug)+"/new"))
}

func spaceTopic(sp store.Space) string { return live.SpaceTopic(sp.ID) }

// spaceSeats is the page's live section, refetched whenever the space changes.
func (s *Server) spaceSeats(w http.ResponseWriter, r *http.Request) {
	e, ok := s.event(w, r)
	if !ok {
		return
	}
	sp, ok := s.spaceForRequest(w, r, e)
	if !ok {
		return
	}
	viewer := currentPerson(r.Context())
	v, err := s.spaceView(r.Context(), e, sp, viewer)
	if err != nil {
		s.serverError(w, err)
		return
	}
	// A spot page asks with its own code, so a visitor's Join comes back to that spot (S2).
	if code := r.URL.Query().Get("spot"); viewer == nil && code != "" {
		v.SignInURL = auth.SignInURL(spotURL(e.Slug, code))
	}
	s.render(w, r, http.StatusOK, views.SpaceSeats(v))
}

func (s *Server) cancelledRefusal(w http.ResponseWriter, r *http.Request, e store.Event) {
	s.render(w, r, http.StatusConflict, views.Message(s.page(r, e, "Cancelled"),
		"This space was cancelled, so it can't be changed. Start a new one instead."))
}

func editForm(e store.Event, sp store.Space) views.SpaceForm {
	return views.SpaceForm{
		Action:   spaceURL(e.Slug, sp.Slug) + "/edit",
		Heading:  "Edit space",
		Lede:     "Everyone going sees the change.",
		Submit:   "Save changes",
		IsEdit:   true,
		CancelTo: spaceURL(e.Slug, sp.Slug),
	}
}

func (s *Server) editSpaceForm(w http.ResponseWriter, r *http.Request) {
	e, sp, _, ok := s.managedSpace(w, r)
	if !ok {
		return
	}
	if sp.Status != store.SpaceOpen {
		s.cancelledRefusal(w, r, e)
		return
	}
	options, _, err := s.spotOptions(r.Context(), e)
	if err != nil {
		s.serverError(w, err)
		return
	}
	form := editForm(e, sp)
	form.Spots = options
	form.Title = sp.Title
	form.Blurb = sp.Blurb
	form.SpotID = sp.SpotID
	form.Starts = sp.StartsAt.In(eventLocation(e)).Format(formTime)
	form.Minutes = strconv.Itoa(sp.Minutes)
	form.Capacity = strconv.Itoa(sp.Capacity)
	s.render(w, r, http.StatusOK, views.SpaceFormPage(s.page(r, e, "Edit space"), form))
}

func (s *Server) updateSpace(w http.ResponseWriter, r *http.Request) {
	e, sp, _, ok := s.managedSpace(w, r)
	if !ok {
		return
	}
	if sp.Status != store.SpaceOpen {
		s.cancelledRefusal(w, r, e)
		return
	}
	options, spots, err := s.spotOptions(r.Context(), e)
	if err != nil {
		s.serverError(w, err)
		return
	}
	form := editForm(e, sp)
	if !s.readSpaceForm(w, r, &form) {
		return
	}
	form.Spots = options
	form.PostID = ""
	loc := eventLocation(e)
	refuse := func(status int, text string) {
		form.Error = text
		s.render(w, r, status, views.SpaceFormPage(s.page(r, e, "Edit space"), form))
	}

	in, problem := validateSpace(form, spots, loc)
	if problem != "" {
		refuse(http.StatusUnprocessableEntity, problem)
		return
	}
	// The form only has minutes, so an untouched start keeps its exact time. Only a changed one has
	// to be in the future: a host can still retitle a space that's under way.
	if in.StartsAt.Equal(sp.StartsAt.Truncate(time.Minute)) {
		in.StartsAt = sp.StartsAt
	} else if in.StartsAt.Before(time.Now().Add(-startGrace)) {
		refuse(http.StatusUnprocessableEntity, "That time has passed. Pick a later one.")
		return
	}

	updated, err := s.store.UpdateSpace(r.Context(), sp.ID, in)
	if err != nil {
		if status, text, ok := refusal(err, loc); ok {
			refuse(status, text)
			return
		}
		if errors.Is(err, store.ErrSpaceCancelled) {
			s.cancelledRefusal(w, r, e)
			return
		}
		s.serverError(w, err)
		return
	}

	s.indexSpace(r.Context(), updated, s.spaceTags(r.Context(), updated))
	// A raised capacity opens seats, and the waitlist has first claim on them.
	promoted, err := s.store.FillOpenSeats(r.Context(), updated.ID, time.Now())
	if err != nil {
		s.log.Error("fill seats after edit", "err", err, "space", updated.ID)
	}
	s.tellPromoted(r.Context(), e, updated.ID, promoted)
	s.publishSpaceChanged(r.Context(), updated.ID)
	s.publishSpacesListChanged(sp.SpotID, updated.SpotID)
	http.Redirect(w, r, spaceURL(e.Slug, updated.Slug), http.StatusSeeOther) // #nosec G710 -- the slug is generated by the server
}

func (s *Server) cancelSpace(w http.ResponseWriter, r *http.Request) {
	e, sp, person, ok := s.managedSpace(w, r)
	if !ok {
		return
	}
	told, err := s.store.CancelSpace(r.Context(), sp.ID)
	if errors.Is(err, store.ErrSpaceCancelled) {
		// A double tap, or two people at once. Either way it's what they wanted.
		http.Redirect(w, r, spaceURL(e.Slug, sp.Slug), http.StatusSeeOther) // #nosec G710 -- the slug is generated by the server
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.undo.start(sp.ID)
	if err := s.search.Delete(r.Context(), search.TypeSpace, sp.ID); err != nil {
		s.log.Error("unindex space", "err", err, "space", sp.ID)
	}
	s.notify(r.Context(), told, person.ID, "Cancelled by host: "+sp.Title, spaceURL(e.Slug, sp.Slug))
	s.publishSpaceChanged(r.Context(), sp.ID)
	s.publishSpacesListChanged(sp.SpotID)
	http.Redirect(w, r, spaceURL(e.Slug, sp.Slug), http.StatusSeeOther) // #nosec G710 -- the slug is generated by the server
}

// undoCancelSpace takes a cancel back while the toast is up. People were told at once (D2), so
// they're told it's back on too.
func (s *Server) undoCancelSpace(w http.ResponseWriter, r *http.Request) {
	e, sp, person, ok := s.managedSpace(w, r)
	if !ok {
		return
	}
	if !s.undo.open(sp.ID) {
		// The window also closes when an Undo works, so a second tap can land here with the space
		// already back on. Read it again: sp may have been loaded before that tap finished.
		current, err := s.store.SpaceByID(r.Context(), sp.ID)
		if err != nil {
			s.serverError(w, err)
			return
		}
		if current.Status != store.SpaceCancelled {
			http.Redirect(w, r, spaceURL(e.Slug, sp.Slug), http.StatusSeeOther) // #nosec G710 -- the slug is generated by the server
			return
		}
		s.render(w, r, http.StatusConflict, views.Message(s.page(r, e, "Too late to undo"),
			"It's too late to undo that. Start a new space instead."))
		return
	}
	told, err := s.store.RestoreSpace(r.Context(), sp.ID)
	if errors.Is(err, store.ErrSpaceNotCancelled) {
		// A double tap, or two people at once. Either way it's already back on.
		http.Redirect(w, r, spaceURL(e.Slug, sp.Slug), http.StatusSeeOther) // #nosec G710 -- the slug is generated by the server
		return
	}
	if err != nil {
		if status, text, ok := refusal(err, eventLocation(e)); ok {
			s.render(w, r, status, views.Message(s.page(r, e, "Can't undo"), text))
			return
		}
		s.serverError(w, err)
		return
	}
	s.undo.close(sp.ID)
	s.indexSpace(r.Context(), sp, s.spaceTags(r.Context(), sp))
	s.notify(r.Context(), told, person.ID, "Back on: "+sp.Title, spaceURL(e.Slug, sp.Slug))
	s.publishSpaceChanged(r.Context(), sp.ID)
	s.publishSpacesListChanged(sp.SpotID)
	http.Redirect(w, r, spaceURL(e.Slug, sp.Slug), http.StatusSeeOther) // #nosec G710 -- the slug is generated by the server
}

// notify sends a notice to each person's own topic, skipping whoever did this themselves.
func (s *Server) notify(ctx context.Context, people []string, actorID, text, href string) {
	var html bytes.Buffer
	if err := views.Notice(text, href).Render(ctx, &html); err != nil {
		s.log.Error("render notice", "err", err)
		return
	}
	fragment := live.Fragment{Event: "notice", HTML: html.Bytes()}
	for _, id := range people {
		if id != actorID {
			s.hub.Publish(live.PersonTopic(id), fragment)
		}
	}
}

// publishSpaceChanged tells every open page a space's counts or status changed. S4 calls it after
// a signup. It sends the counts-only fragment, SSE event space-<id>, to space:<id>, spot:<spot_id>
// and event (D17), so it must never carry a name.
func (s *Server) publishSpaceChanged(ctx context.Context, spaceID string) {
	sp, err := s.store.SpaceByID(ctx, spaceID)
	if err != nil {
		s.log.Error("load space to publish", "err", err, "space", spaceID)
		return
	}
	var html bytes.Buffer
	component := views.SeatCount(sp.Going, sp.Capacity, sp.Waitlist, sp.Status == store.SpaceCancelled)
	if err := component.Render(ctx, &html); err != nil {
		s.log.Error("render seat count", "err", err, "space", spaceID)
		return
	}
	fragment := live.Fragment{Event: "space-" + sp.ID, HTML: html.Bytes()}
	s.hub.Publish(live.SpaceTopic(sp.ID), fragment)
	s.hub.Publish(live.SpotTopic(sp.SpotID), fragment)
	s.hub.Publish(live.EventTopic, fragment)
}

// publishSpacesListChanged tells feeds, spot pages and the Wall that the set of spaces changed, so
// they refetch with hx-trigger="sse:spaces-changed".
func (s *Server) publishSpacesListChanged(spotIDs ...string) {
	fragment := live.Fragment{Event: "spaces-changed"}
	s.hub.Publish(live.EventTopic, fragment)
	seen := map[string]bool{}
	for _, id := range spotIDs {
		if !seen[id] {
			seen[id] = true
			s.hub.Publish(live.SpotTopic(id), fragment)
		}
	}
}

// indexSpace writes a space to search after the commit. A failure is logged and never reaches the
// person who made the space.
func (s *Server) indexSpace(ctx context.Context, sp store.Space, tags []string) {
	doc := search.SpaceDoc(sp.ID, sp.Title, sp.Blurb, sp.HostID, sp.ZoneID, sp.StartsAt, sp.Minutes)
	for _, tag := range tags {
		if !slices.Contains(doc.Tags, tag) {
			doc.Tags = append(doc.Tags, tag)
		}
	}
	if err := s.search.Index(ctx, doc); err != nil {
		s.log.Error("index space", "err", err, "space", sp.ID)
	}
}

// spaceTags are the tags of the post a space came from, if it's still open.
func (s *Server) spaceTags(ctx context.Context, sp store.Space) []string {
	if sp.FromPostID == nil {
		return nil
	}
	summary, err := s.store.PostForSpace(ctx, *sp.FromPostID, time.Now())
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			s.log.Error("load tags for space", "err", err, "space", sp.ID)
		}
		return nil
	}
	return summary.Tags
}
