package web

import "github.com/highclip/two-feet/app/internal/web/views"

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
