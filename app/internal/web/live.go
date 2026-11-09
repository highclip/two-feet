package web

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/highclip/two-feet/app/internal/auth"
	"github.com/highclip/two-feet/app/internal/live"
)

const (
	heartbeatEvery  = 15 * time.Second
	liveWriteWindow = 20 * time.Second // drops a client that has stopped reading
	maxTopics       = 16
)

var topicID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// liveStream is the one SSE endpoint: /e/{slug}/live?topics=event,spot:<id>. The event, spot and
// space topics are open to visitors because the Wall and spot pages are public, so what's published
// there must carry no names. A person topic is only ever that person's own.
func (s *Server) liveStream(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.event(w, r); !ok {
		return
	}
	topics, ok := parseTopics(r.URL.Query().Get("topics"))
	if !ok {
		http.Error(w, "Those live topics aren't valid. Reload the page.", http.StatusBadRequest)
		return
	}
	person, signedIn := auth.Current(r.Context())
	for _, t := range topics {
		if id, isPerson := strings.CutPrefix(t, "person:"); isPerson && (!signedIn || id != person.ID) {
			http.Error(w, "You can only follow your own updates.", http.StatusForbidden)
			return
		}
	}

	fragments, cancel := s.hub.Subscribe(r.Context(), topics)
	defer cancel()

	rc := http.NewResponseController(w)
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	// Deadline errors are ignored: a wrapped writer may not support them, and the stream still works.
	_ = rc.SetWriteDeadline(time.Now().Add(liveWriteWindow))
	if _, err := fmt.Fprint(w, "retry: 2000\n\n"); err != nil {
		return
	}
	if err := rc.Flush(); err != nil {
		s.log.Error("live flush", "err", err)
		return
	}

	beat := time.NewTicker(heartbeatEvery)
	defer beat.Stop()
	for {
		var err error
		select {
		case <-r.Context().Done():
			return
		case f, open := <-fragments:
			if !open {
				return
			}
			_ = rc.SetWriteDeadline(time.Now().Add(liveWriteWindow))
			err = writeFragment(w, f)
		case <-beat.C:
			_ = rc.SetWriteDeadline(time.Now().Add(liveWriteWindow))
			_, err = fmt.Fprint(w, ": heartbeat\n\n")
		}
		if err != nil {
			return
		}
		if err := rc.Flush(); err != nil {
			return
		}
	}
}

// parseTopics splits and checks the topic list. It reports false for an empty or oversized list or
// any topic that isn't one of the shapes in spec.live.
func parseTopics(raw string) ([]string, bool) {
	if raw == "" {
		return nil, false
	}
	topics := strings.Split(raw, ",")
	if len(topics) > maxTopics {
		return nil, false
	}
	for _, t := range topics {
		if t == live.EventTopic {
			continue
		}
		kind, id, found := strings.Cut(t, ":")
		if !found || !topicID.MatchString(id) || (kind != "spot" && kind != "space" && kind != "person") {
			return nil, false
		}
	}
	return topics, true
}

// writeFragment frames a fragment as one SSE message. Each line of HTML needs its own data: field,
// and the event name can't contain a line break or it would forge extra fields.
func writeFragment(w io.Writer, f live.Fragment) error {
	var b bytes.Buffer
	event := strings.NewReplacer("\r", "", "\n", "").Replace(f.Event)
	if event != "" {
		fmt.Fprintf(&b, "event: %s\n", event)
	}
	html := bytes.ReplaceAll(f.HTML, []byte("\r\n"), []byte("\n"))
	html = bytes.ReplaceAll(html, []byte("\r"), []byte("\n"))
	for line := range bytes.SplitSeq(html, []byte("\n")) {
		b.WriteString("data: ")
		b.Write(line)
		b.WriteByte('\n')
	}
	b.WriteByte('\n')
	_, err := w.Write(b.Bytes())
	return err
}
