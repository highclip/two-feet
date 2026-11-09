package web

import (
	"errors"
	"fmt"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/highclip/two-feet/app/internal/auth"
	"github.com/highclip/two-feet/app/internal/search"
	"github.com/highclip/two-feet/app/internal/store"
	"github.com/highclip/two-feet/app/internal/telemetry"
	"github.com/highclip/two-feet/app/internal/web/views"
)

const (
	minPostLength = 10
	maxPostLength = 280
	postLimit     = 5
	postWindow    = 10 * time.Minute
	postAction    = "post"

	placeholderCount  = 12
	placeholderLength = 70
)

var postKinds = []string{store.PostStuck, store.PostCurious, store.PostOffer}

func (s *Server) registerPosts(mux *http.ServeMux) {
	mux.Handle("GET /e/{slug}/posts/new", auth.RequirePerson(http.HandlerFunc(s.newPost)))
	mux.Handle("POST /e/{slug}/posts", auth.RequirePerson(http.HandlerFunc(s.createPost)))
	mux.Handle("GET /e/{slug}/posts/{id}", auth.RequirePerson(http.HandlerFunc(s.showPost)))
	mux.Handle("POST /e/{slug}/posts/{id}/resolve", auth.RequirePerson(http.HandlerFunc(s.resolvePost)))
}

// canPost is the create_post and resolve_own_post permission.
func canPost(p store.Person) bool {
	return auth.HasRole(p, store.RoleAttendee, store.RoleSponsorRep)
}

func postsURL(slug string) string { return "/e/" + slug + "/posts" }

func postURL(slug, id string) string { return postsURL(slug) + "/" + id }

// composer builds the posting form. It's empty for anyone who can't post, and the view then draws
// nothing.
func (s *Server) composer(r *http.Request, event store.Event, person store.Person) views.PostComposer {
	if !canPost(person) {
		return views.PostComposer{}
	}
	return views.PostComposer{
		Action:       postsURL(event.Slug),
		Kind:         store.PostStuck,
		Placeholders: s.placeholders(r),
	}
}

// placeholders are real posts from the room, shortened to fit the box. A failure here costs the
// page nothing but the variety.
func (s *Server) placeholders(r *http.Request) []string {
	texts, err := s.store.RecentPostTexts(r.Context(), time.Now(), placeholderCount)
	if err != nil {
		s.log.Error("load placeholders", "err", err)
		return nil
	}
	out := make([]string, 0, len(texts))
	for _, t := range texts {
		out = append(out, shorten(t, placeholderLength))
	}
	return out
}

func shorten(text string, limit int) string {
	if utf8.RuneCountInString(text) <= limit {
		return text
	}
	return strings.TrimSpace(string([]rune(text)[:limit-1])) + "…"
}

func (s *Server) newPost(w http.ResponseWriter, r *http.Request) {
	event, ok := s.event(w, r)
	if !ok {
		return
	}
	person, _ := auth.Current(r.Context())
	if person.DisplayName == "" {
		http.Redirect(w, r, onboardingURL(event.Slug, r.URL.RequestURI()), http.StatusSeeOther) // #nosec G710 -- a fixed local path; the request URI is only a query-escaped parameter
		return
	}
	if !canPost(person) {
		s.forbiddenPost(w, r, event)
		return
	}
	s.renderComposer(w, r, event, http.StatusOK, s.composer(r, event, person))
}

func (s *Server) renderComposer(w http.ResponseWriter, r *http.Request, event store.Event, status int, c views.PostComposer) {
	s.render(w, r, status, views.NewPost(s.page(r, event, "Post"), c, "/e/"+event.Slug))
}

func (s *Server) forbiddenPost(w http.ResponseWriter, r *http.Request, event store.Event) {
	s.render(w, r, http.StatusForbidden, views.Message(s.page(r, event, "Not for organizers"),
		"Organizers can't post here. Sign in as an attendee to post a problem."))
}

func (s *Server) createPost(w http.ResponseWriter, r *http.Request) {
	event, ok := s.event(w, r)
	if !ok {
		return
	}
	person, _ := auth.Current(r.Context())
	if person.DisplayName == "" {
		http.Redirect(w, r, onboardingURL(event.Slug, "/e/"+event.Slug), http.StatusSeeOther)
		return
	}
	if !canPost(person) {
		s.forbiddenPost(w, r, event)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "That post was too long. Shorten it and try again.", http.StatusRequestEntityTooLarge)
		return
	}

	form := s.composer(r, event, person)
	form.Kind = r.PostFormValue("kind")
	form.Text = r.PostFormValue("text")

	text, problem := validatePost(form.Kind, form.Text)
	if problem != "" {
		if !slices.Contains(postKinds, form.Kind) {
			form.Kind = store.PostStuck
		}
		form.Error = problem
		s.renderComposer(w, r, event, http.StatusUnprocessableEntity, form)
		return
	}

	allowed, retryAt := s.limiter.Allow(person.ID, postAction, postLimit, postWindow)
	if !allowed {
		w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(time.Until(retryAt).Seconds()))))
		form.Error = "That's 5 posts in 10 minutes. You can post again at " + clock(event, roundUpToMinute(retryAt)) + "."
		s.renderComposer(w, r, event, http.StatusTooManyRequests, form)
		return
	}

	post, err := s.store.CreatePost(r.Context(), person.ID, form.Kind, text, search.ExtractTags(text))
	if err != nil {
		s.serverError(w, fmt.Errorf("create post: %w", err))
		return
	}
	telemetry.RecordPost(r.Context(), post.Kind)
	s.sync.PostOpened(r.Context(), post)
	http.Redirect(w, r, postURL(event.Slug, post.ID), http.StatusSeeOther) // #nosec G710 -- a fixed local path around a database-generated uuid
}

// validatePost cleans a post down to one line. problem is the sentence to show the person when
// something needs fixing, and empty otherwise.
func validatePost(kind, raw string) (text, problem string) {
	if !slices.Contains(postKinds, kind) {
		return "", "Choose stuck, curious or can help, then post again."
	}
	text = strings.Join(strings.Fields(raw), " ")
	if strings.ContainsFunc(text, unicode.IsControl) {
		return "", "That post has characters we can't show. Use plain text."
	}
	switch n := utf8.RuneCountInString(text); {
	case n < minPostLength:
		return "", "Say a little more. A post needs at least 10 characters."
	case n > maxPostLength:
		return "", fmt.Sprintf("Keep it to 280 characters. That's %d.", n)
	}
	return text, ""
}

// roundUpToMinute keeps a "try again at" time from being earlier than the real one.
func roundUpToMinute(t time.Time) time.Time {
	if rounded := t.Truncate(time.Minute); !rounded.Equal(t) {
		return rounded.Add(time.Minute)
	}
	return t
}

// clock formats a time as the event's local time.
func clock(event store.Event, t time.Time) string {
	if loc, err := time.LoadLocation(event.Timezone); err == nil {
		t = t.In(loc)
	}
	return t.Format("3:04 PM")
}

// showPost is the author's own page for a post. Anyone else gets the same answer as for no post at
// all, so post ids can't be probed.
func (s *Server) showPost(w http.ResponseWriter, r *http.Request) {
	event, ok := s.event(w, r)
	if !ok {
		return
	}
	person, _ := auth.Current(r.Context())
	post, err := s.store.PostByID(r.Context(), r.PathValue("id"))
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		s.serverError(w, err)
		return
	}
	if err != nil || post.AuthorID != person.ID {
		s.render(w, r, http.StatusNotFound, views.Message(s.page(r, event, "Not found"),
			"That post isn't here. It may have been yours on another account."))
		return
	}
	s.render(w, r, http.StatusOK, views.PostPage(s.page(r, event, "Your post"), views.PostView{
		Post:       post,
		Posted:     clock(event, post.CreatedAt),
		HomeURL:    "/e/" + event.Slug,
		ResolveURL: postURL(event.Slug, post.ID) + "/resolve",
		StartURL:   "/e/" + event.Slug + "/spaces/new?post=" + post.ID,
	}))
}

// resolvePost closes the author's own post and takes it out of matches.
func (s *Server) resolvePost(w http.ResponseWriter, r *http.Request) {
	event, ok := s.event(w, r)
	if !ok {
		return
	}
	person, _ := auth.Current(r.Context())
	post, err := s.store.PostByID(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		s.render(w, r, http.StatusNotFound, views.Message(s.page(r, event, "Not found"),
			"That post isn't here. Go back and try another."))
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	if post.AuthorID != person.ID || !canPost(person) {
		s.render(w, r, http.StatusForbidden, views.Message(s.page(r, event, "Not your post"),
			"Only the person who posted this can close it."))
		return
	}
	resolved, err := s.store.ResolvePost(r.Context(), post.ID, person.ID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		s.serverError(w, err)
		return
	}
	if err == nil {
		s.sync.PostClosed(r.Context(), resolved)
	}
	http.Redirect(w, r, postURL(event.Slug, post.ID), http.StatusSeeOther) // #nosec G710 -- a fixed local path around a database-generated uuid
}
