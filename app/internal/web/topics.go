package web

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/highclip/two-feet/app/internal/store"
	"github.com/highclip/two-feet/app/internal/web/views"
)

// wallTopics is how many topics fit the Wall's strip at a size readable across the room.
const wallTopics = 3

// topicViews reads the list the background job keeps. It costs a request nothing but a memory read,
// and is empty until a list has loaded.
func (s *Server) topicViews() []views.Topic {
	if s.topics == nil {
		return nil
	}
	current := s.topics.Current()
	out := make([]views.Topic, 0, len(current))
	for _, t := range current {
		out = append(out, views.Topic{Title: t.Title, Source: t.Source, Related: t.Related})
	}
	return out
}

// offerQuickStart picks a free spot for the form's time, so tapping a topic leaves only a confirm. A
// form that arrived with a spot already chosen keeps it, and a failed lookup only loses the shortcut.
func (s *Server) offerQuickStart(ctx context.Context, e store.Event, form *views.SpaceForm, starts time.Time) {
	if len(form.Suggestions) == 0 || form.SpotID != "" {
		return
	}
	minutes, err := strconv.Atoi(form.Minutes)
	if err != nil {
		return
	}
	spot, zone, err := s.store.FreeSpotAt(ctx, e.ID, starts, minutes)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			s.log.Error("find a free spot", "err", err)
		}
		return
	}
	form.QuickSpotID = spot.ID
	form.QuickSpotLabel = views.SpotLabel(spot, zone)
}

func firstTopics(list []views.Topic, n int) []views.Topic {
	return list[:min(len(list), n)]
}
