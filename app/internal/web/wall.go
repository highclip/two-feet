package web

import (
	"cmp"
	"context"
	"fmt"
	"math"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	qrcode "github.com/skip2/go-qrcode"

	"github.com/highclip/two-feet/app/internal/live"
	"github.com/highclip/two-feet/app/internal/search"
	"github.com/highclip/two-feet/app/internal/store"
	"github.com/highclip/two-feet/app/internal/web/views"
)

const (
	wallSlot    = 15 * time.Minute
	wallSlots   = 8
	wallMinutes = wallSlots * 15

	wallMinSpan     = 15 // a note narrower than one slot can't be read
	wallDotsMinSpan = 30 // seat dots need about two slots of width
	wallSoonMinutes = 30 // how far ahead a note gets a countdown chip
	wallRedMinutes  = 5

	wallDenseRows = 5 // more rows than this leaves room for one line of title

	wallTickerPosts = 3
	wallTickerChars = 70
	wallTopTags     = 8
	wallTagsEvery   = time.Minute
	wallTagsTimeout = time.Second

	// wallChanged makes open Walls refetch their board, so no card needs an SSE listener of its own.
	wallChanged = "wall-changed"
)

func (s *Server) registerWall(mux *http.ServeMux) {
	mux.HandleFunc("GET /e/{slug}/wall", s.wallPage)
	mux.HandleFunc("GET /e/{slug}/wall/board", s.wallBoard)
}

// publishWallChanged tells open Walls to refetch. The board carries no names, and this carries
// nothing at all.
func (s *Server) publishWallChanged() {
	s.hub.Publish(live.EventTopic, live.Fragment{Event: wallChanged})
}

// wallPage is the projector view, public (view_wall). It follows the event topic only: never a
// person's own, even when whoever opened it is signed in.
func (s *Server) wallPage(w http.ResponseWriter, r *http.Request) {
	e, ok := s.event(w, r)
	if !ok {
		return
	}
	board, err := s.buildWall(r.Context(), e)
	if err != nil {
		s.serverError(w, err)
		return
	}
	page := views.Page{Title: "The Wall · " + e.Name, Version: s.version, LiveURL: live.URL(e.Slug, live.EventTopic)}
	s.render(w, r, http.StatusOK, views.WallPage(page, "/e/"+e.Slug+"/wall/board", board))
}

// wallBoard is everything on the Wall that changes, refetched as one fragment.
func (s *Server) wallBoard(w http.ResponseWriter, r *http.Request) {
	e, ok := s.event(w, r)
	if !ok {
		return
	}
	board, err := s.buildWall(r.Context(), e)
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.render(w, r, http.StatusOK, views.WallBoardContents(board))
}

func (s *Server) buildWall(ctx context.Context, e store.Event) (views.WallBoard, error) {
	now := time.Now()
	loc := eventLocation(e)
	window := now.Truncate(wallSlot)

	spots, err := s.store.Spots(ctx, e.ID)
	if err != nil {
		return views.WallBoard{}, err
	}
	zones, err := s.store.ZoneNames(ctx, e.ID)
	if err != nil {
		return views.WallBoard{}, err
	}
	spaces, err := s.store.SpacesBetween(ctx, e.ID, now, window.Add(wallMinutes*time.Minute))
	if err != nil {
		return views.WallBoard{}, err
	}
	posts, err := s.store.LatestPostLines(ctx, now, wallTickerPosts)
	if err != nil {
		return views.WallBoard{}, err
	}

	qrPath, qrSize, err := eventQRPath(s.publicURL + "/e/" + e.Slug)
	if err != nil {
		s.log.Error("wall qr", "err", err) // the Wall still works without it
	}

	board := views.WallBoard{
		QRPath:   qrPath,
		QRSize:   qrSize,
		Clock:    now.In(loc).Format("3:04"),
		Date:     now.In(loc).Format("Monday, Jan 2"),
		NowPct:   100 * now.Sub(window).Minutes() / wallMinutes,
		Empty:    len(spaces) == 0,
		StartURL: spacesURL(e.Slug) + "/new",
		Dense:    len(spots) > wallDenseRows,
		Tags:     wallTagViews(s.wallTags.get(ctx, now)),
	}
	for i := range wallSlots {
		board.Slots = append(board.Slots, window.Add(time.Duration(i)*wallSlot).In(loc).Format("3:04"))
	}
	for _, p := range posts {
		board.Posts = append(board.Posts, views.WallPost{Kind: p.Kind, Text: shorten(p.Text, wallTickerChars)})
	}

	slices.SortStableFunc(spots, func(a, b store.Spot) int {
		return cmp.Or(cmp.Compare(zones[a.ZoneID], zones[b.ZoneID]), cmp.Compare(a.Name, b.Name))
	})
	bySpot := map[string][]store.Space{}
	for _, sp := range spaces {
		bySpot[sp.SpotID] = append(bySpot[sp.SpotID], sp)
	}
	for _, spot := range spots {
		board.Rows = append(board.Rows, views.WallRow{
			Spot:  spot.Name,
			Zone:  zones[spot.ZoneID],
			Notes: placeWallNotes(bySpot[spot.ID], window, now),
		})
	}
	return board, nil
}

// eventQRPath draws a QR code as one SVG path, so the Wall adds a single node however dense the code
// is. size is its width in modules, without a quiet zone: the page's padding is that.
func eventQRPath(target string) (path string, size int, err error) {
	code, err := qrcode.New(target, qrcode.Medium)
	if err != nil {
		return "", 0, fmt.Errorf("encode qr: %w", err)
	}
	code.DisableBorder = true
	modules := code.Bitmap()
	var b strings.Builder
	for y, row := range modules {
		for x, dark := range row {
			if dark {
				fmt.Fprintf(&b, "M%d %dh1v1h-1z", x, y)
			}
		}
	}
	return b.String(), len(modules), nil
}

func wallTagViews(tags []search.TagCount) []views.WallTag {
	out := make([]views.WallTag, 0, len(tags))
	for _, t := range tags {
		out = append(out, views.WallTag{Tag: t.Tag, Count: t.Count})
	}
	return out
}

// placeWallNotes turns one spot's spaces, in start order, into notes positioned by the minute within
// the window. Notes are copied field by field so nothing but title and counts can reach the page.
func placeWallNotes(spaces []store.Space, window, now time.Time) []views.WallNote {
	notes := make([]views.WallNote, 0, len(spaces))
	for i, sp := range spaces {
		from := wallMinute(sp.StartsAt, window)
		to := max(wallMinute(sp.EndsAt(), window), from+wallMinSpan)
		if from > wallMinutes-wallMinSpan {
			from = wallMinutes - wallMinSpan
		}
		to = min(to, wallMinutes)
		if i+1 < len(spaces) {
			to = min(to, max(wallMinute(spaces[i+1].StartsAt, window), from+1))
		}
		chip, hot := wallChip(sp.StartsAt, now)
		notes = append(notes, views.WallNote{
			ID:       sp.ID,
			Title:    sp.Title,
			Kind:     sp.Kind,
			Going:    sp.Going,
			Capacity: sp.Capacity,
			From:     from,
			To:       to,
			ShowDots: to-from >= wallDotsMinSpan,
			Chip:     chip,
			ChipHot:  hot,
		})
	}
	return notes
}

// wallMinute is how far into the window a time falls, in whole minutes, kept inside it.
func wallMinute(t, window time.Time) int {
	return min(max(int(t.Sub(window).Minutes()), 0), wallMinutes)
}

// wallChip is the countdown on a note: red once it's under five minutes or under way, and absent
// while it's still far off.
func wallChip(start, now time.Time) (text string, hot bool) {
	if !now.Before(start) {
		return "NOW", true
	}
	minutes := int(math.Ceil(start.Sub(now).Minutes()))
	if minutes > wallSoonMinutes {
		return "", false
	}
	return fmt.Sprintf("IN %d MIN", minutes), minutes < wallRedMinutes
}

// wallTagStrip caches the top tags, because the board refetches on every signup and Elasticsearch
// only needs asking about once a minute (D26). A failure is remembered for the same minute: the
// strip hides and the Wall carries on.
type wallTagStrip struct {
	counter search.TagCounter
	log     interface{ Error(string, ...any) }

	mu   sync.Mutex
	at   time.Time
	tags []search.TagCount
}

func (t *wallTagStrip) get(ctx context.Context, now time.Time) []search.TagCount {
	if t.counter == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.at.IsZero() && now.Sub(t.at) < wallTagsEvery {
		return t.tags
	}
	ask, cancel := context.WithTimeout(ctx, wallTagsTimeout)
	defer cancel()
	tags, err := t.counter.TopTags(ask, now, wallTopTags)
	if err != nil {
		t.log.Error("wall top tags", "err", err)
		tags = nil
	}
	t.at, t.tags = now, tags
	return tags
}
