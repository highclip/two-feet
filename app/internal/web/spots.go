package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/highclip/two-feet/app/internal/auth"
	"github.com/highclip/two-feet/app/internal/live"
	"github.com/highclip/two-feet/app/internal/store"
	"github.com/highclip/two-feet/app/internal/web/views"
)

const (
	slotLength    = 15 * time.Minute
	longestSpace  = 45 // minutes
	formDefault   = 30 // minutes, what the create form picks unless told otherwise
	nextHereCount = 2
	maxCheckInLen = 1 << 10

	unknownCodeText = "That code isn't from this event, so try another one at the venue."
)

func (s *Server) registerSpots(mux *http.ServeMux) {
	mux.HandleFunc("GET /e/{slug}/spots/{qr}/board", s.spotBoard)
	mux.Handle("POST /e/{slug}/checkin", auth.RequirePerson(http.HandlerFunc(s.checkIn)))
}

func eventURL(eventSlug string) string { return "/e/" + eventSlug }

func spotURL(eventSlug, qrSlug string) string {
	return eventURL(eventSlug) + "?spot=" + url.QueryEscape(qrSlug)
}

// canCheckIn is the check_in_at_spot permission.
func canCheckIn(p store.Person) bool {
	return p.DisplayName != "" && auth.HasRole(p, store.RoleAttendee, store.RoleSponsorRep, store.RoleOrganizer)
}

// opensThePage reports whether a browser is really showing this spot to someone. Opening a code
// checks you in on a GET, so an image or frame on another site, a link from one, or a prefetch
// mustn't be able to move you. A scanned code arrives with Sec-Fetch-Site none, and the return from
// sign-in with same-origin. Browsers without fetch metadata send neither header and count as
// opening it.
func opensThePage(r *http.Request) bool {
	if dest := r.Header.Get("Sec-Fetch-Dest"); dest != "" && dest != "document" {
		return false
	}
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "none" && site != "same-origin" {
		return false
	}
	return !strings.Contains(r.Header.Get("Sec-Purpose"), "prefetch") && r.Header.Get("Purpose") != "prefetch"
}

// spotPage is what a table's QR code opens. It's public; opening it signed in also checks you in.
func (s *Server) spotPage(w http.ResponseWriter, r *http.Request, event store.Event) {
	ctx := r.Context()
	spot, err := s.store.SpotInEvent(ctx, event.ID, r.URL.Query().Get("spot"))
	if errors.Is(err, store.ErrNotFound) {
		s.render(w, r, http.StatusNotFound, views.UnknownCode(s.page(r, event, "Unknown code"), unknownCodeText, eventURL(event.Slug)))
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	zones, err := s.store.ZoneNames(ctx, event.ID)
	if err != nil {
		s.serverError(w, err)
		return
	}

	now := s.now()
	viewer := currentPerson(ctx)
	view := views.SpotView{Spot: spot, ZoneName: zones[spot.ZoneID]}
	if viewer == nil {
		view.SignInURL = auth.SignInURL(spotURL(event.Slug, spot.QRSlug))
	} else {
		if canCheckIn(*viewer) && opensThePage(r) {
			if err := s.store.CheckIn(ctx, viewer.ID, spot.ZoneID, now); err != nil {
				s.serverError(w, err)
				return
			}
			view.CheckedIn = true
		}
		counts, err := s.store.ZoneCounts(ctx, event.ID, now)
		if err != nil {
			s.serverError(w, err)
			return
		}
		view.ZoneCount = views.ZoneCountLabel(counts[spot.ZoneID])
	}
	view.Board, err = s.boardView(ctx, event, spot, viewer, now)
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.render(w, r, http.StatusOK, views.SpotPage(s.livePage(r, event, spot.Name, live.SpotTopic(spot.ID)), view))
}

// spotBoard is the page's now and next, refetched whenever the spot's spaces change. It never
// checks anyone in: only opening the code does.
func (s *Server) spotBoard(w http.ResponseWriter, r *http.Request) {
	event, ok := s.event(w, r)
	if !ok {
		return
	}
	spot, err := s.store.SpotInEvent(r.Context(), event.ID, r.PathValue("qr"))
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, unknownCodeText, http.StatusNotFound)
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	board, err := s.boardView(r.Context(), event, spot, currentPerson(r.Context()), s.now())
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.render(w, r, http.StatusOK, views.SpotBoardBody(board))
}

// boardView gathers the space happening now, the next ones today, and the way to start another.
func (s *Server) boardView(ctx context.Context, e store.Event, spot store.Spot, viewer *store.Person, now time.Time) (views.SpotBoard, error) {
	loc := eventLocation(e)
	year, month, day := now.In(loc).Date()
	schedule, err := s.store.SpotSchedule(ctx, spot.ID, now, time.Date(year, month, day+1, 0, 0, 0, 0, loc))
	if err != nil {
		return views.SpotBoard{}, err
	}
	board := views.SpotBoard{URL: eventURL(e.Slug) + "/spots/" + url.PathEscape(spot.QRSlug) + "/board", WallURL: eventURL(e.Slug) + "/wall"}
	for _, sp := range schedule {
		if sp.StartsAt.After(now) && len(board.Next) == nextHereCount {
			break
		}
		v, err := s.spotSpaceView(ctx, e, spot, sp, viewer, now)
		if err != nil {
			return views.SpotBoard{}, err
		}
		if sp.StartsAt.After(now) {
			board.Next = append(board.Next, v)
		} else if board.Now == nil {
			board.Now = &v
		}
	}
	if viewer == nil || auth.HasRole(*viewer, store.RoleAttendee, store.RoleSponsorRep) {
		board.Start = startHere(e, spot, schedule, viewer, now, loc)
	}
	return board, nil
}

// fromSpotPage reports whether an htmx request came from a spot page, so the seats it gets back can
// sit inside that page's board. It only picks a sync attribute, so a forged header changes nothing.
func fromSpotPage(r *http.Request, e store.Event) bool {
	page, err := url.Parse(r.Header.Get("HX-Current-URL"))
	return err == nil && page.Path == eventURL(e.Slug) && page.Query().Get("spot") != ""
}

// spotSpaceView is a space as this spot page shows it. A visitor's Join signs them in and brings
// them back here, so they're checked in, and the live refetch has to keep doing that.
func (s *Server) spotSpaceView(ctx context.Context, e store.Event, spot store.Spot, sp store.Space, viewer *store.Person, now time.Time) (views.SpaceView, error) {
	v, err := s.spaceView(ctx, e, sp, viewer)
	if err != nil {
		return views.SpaceView{}, err
	}
	v.Now = now
	v.OnSpotBoard = true
	v.SeatsURL += "?spot=" + url.QueryEscape(spot.QRSlug)
	if viewer == nil {
		v.SignInURL = auth.SignInURL(spotURL(e.Slug, spot.QRSlug))
	}
	return v, nil
}

func startHere(e store.Event, spot store.Spot, schedule []store.Space, viewer *store.Person, now time.Time, loc *time.Location) *views.StartHere {
	slot, fits := nextFreeSlot(schedule, now)
	start := &views.StartHere{Slot: slot, Fits: fits, Location: loc}
	if viewer == nil {
		start.URL = auth.SignInURL(spotURL(e.Slug, spot.QRSlug))
		return start
	}
	query := url.Values{"spot": {spot.ID}, "starts": {slot.UTC().Format(time.RFC3339)}}
	if fits < formDefault {
		query.Set("minutes", strconv.Itoa(fits))
	}
	start.URL = spacesURL(e.Slug) + "/new?" + query.Encode()
	return start
}

// nextFreeSlot finds the first quarter hour after now with at least one slot free at this spot,
// and how many minutes fit there. schedule is soonest first and doesn't overlap itself.
func nextFreeSlot(schedule []store.Space, now time.Time) (time.Time, int) {
	slot := now.Truncate(slotLength).Add(slotLength)
	for _, sp := range schedule {
		if !sp.StartsAt.Before(slot.Add(slotLength)) {
			break
		}
		if sp.EndsAt().After(slot) {
			slot = sp.EndsAt().Add(slotLength - time.Nanosecond).Truncate(slotLength)
		}
	}
	fits := longestSpace
	for _, sp := range schedule {
		if sp.StartsAt.Before(slot) {
			continue
		}
		fits = min(fits, int(sp.StartsAt.Sub(slot).Minutes())/15*15)
		break
	}
	return slot, fits
}

// zonePrompt is the "where are you?" part of the home screen.
func (s *Server) zonePrompt(ctx context.Context, event store.Event, person store.Person) (views.ZonePrompt, error) {
	now := s.now()
	zones, err := s.store.ZoneNames(ctx, event.ID)
	if err != nil {
		return views.ZonePrompt{}, err
	}
	counts, err := s.store.ZoneCounts(ctx, event.ID, now)
	if err != nil {
		return views.ZonePrompt{}, err
	}
	prompt := views.ZonePrompt{Action: "/e/" + event.Slug + "/checkin", Stale: person.ZoneID != nil}
	if zone, ok := store.FreshZone(person, now); ok {
		prompt.Current = zones[zone]
		prompt.Stale = false
	}
	for id, name := range zones {
		prompt.Zones = append(prompt.Zones, views.ZoneChoice{ID: id, Name: name, Count: views.ZoneCountLabel(counts[id])})
	}
	sort.Slice(prompt.Zones, func(i, j int) bool { return prompt.Zones[i].Name < prompt.Zones[j].Name })
	return prompt, nil
}

// checkIn is the manual "I'm in" zone picker on the home screen.
func (s *Server) checkIn(w http.ResponseWriter, r *http.Request) {
	event, ok := s.event(w, r)
	if !ok {
		return
	}
	person, _ := auth.Current(r.Context())
	if !canCheckIn(person) {
		s.forbidden(w, r, event, "Choose a name first, then say where you are.")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxCheckInLen)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "That form was too long. Reload and try again.", http.StatusRequestEntityTooLarge)
		return
	}
	zones, err := s.store.ZoneNames(r.Context(), event.ID)
	if err != nil {
		s.serverError(w, err)
		return
	}
	zone := r.PostFormValue("zone")
	if _, known := zones[zone]; !known {
		s.render(w, r, http.StatusUnprocessableEntity, views.Message(s.page(r, event, "Unknown zone"),
			"That zone isn't part of this event. Go back and pick one from the list."))
		return
	}
	if err := s.store.CheckIn(r.Context(), person.ID, zone, s.now()); err != nil {
		s.serverError(w, err)
		return
	}
	http.Redirect(w, r, "/e/"+event.Slug, http.StatusSeeOther)
}
