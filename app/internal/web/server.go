// Package web holds the HTTP handlers, one file per area. Each area registers its own routes in a
// register<Area> method, so slices never edit main.go or a shared router.
package web

import (
	"context"
	"embed"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/a-h/templ"

	"github.com/highclip/two-feet/app/internal/auth"
	"github.com/highclip/two-feet/app/internal/live"
	"github.com/highclip/two-feet/app/internal/search"
	"github.com/highclip/two-feet/app/internal/store"
	"github.com/highclip/two-feet/app/internal/telemetry"
	"github.com/highclip/two-feet/app/internal/web/views"
)

//go:embed static
var staticFiles embed.FS

// Deps is everything the handlers depend on.
type Deps struct {
	Store     *store.Store
	Hub       *live.Hub
	Search    search.Indexer
	Finder    search.Finder // reads the index for matches; nil finds nothing
	Limiter   *Limiter
	Auth      *auth.Authenticator
	Log       *slog.Logger
	Version   string
	EventSlug string
	ClerkKey  string // publishable key
	// Ready reports whether the process can serve, for /readyz.
	Ready func(context.Context) error
}

// Server is the app's HTTP surface.
type Server struct {
	store     *store.Store
	hub       *live.Hub
	search    search.Indexer
	sync      *search.Sync
	matcher   *search.Matcher
	limiter   *Limiter
	authn     *auth.Authenticator
	log       *slog.Logger
	version   string
	eventSlug string
	clerkKey  string
	ready     func(context.Context) error
	undo      *undoWindows
}

// New returns a Server.
func New(d Deps) *Server {
	return &Server{
		store: d.Store, hub: d.Hub, search: d.Search, limiter: d.Limiter, authn: d.Auth, log: d.Log,
		sync:    search.NewSync(d.Store, d.Search, d.Log),
		matcher: search.NewMatcher(d.Store, d.Finder),
		version: d.Version, eventSlug: d.EventSlug, clerkKey: d.ClerkKey, ready: d.Ready,
		undo: newUndoWindows(),
	}
}

// Handler returns the full middleware chain and routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, "ok %s\n", s.version)
	})
	mux.HandleFunc("GET /readyz", s.readyz)
	static, err := fs.Sub(staticFiles, "static")
	if err != nil {
		// The directory is embedded at build time, so this can only be a programming error.
		s.log.Error("static files", "err", err)
	} else {
		mux.Handle("GET /static/", http.StripPrefix("/static/", cacheFor(time.Hour, http.FileServerFS(static))))
	}

	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/e/"+s.eventSlug, http.StatusSeeOther)
	})
	mux.HandleFunc("GET /sign-in", s.signIn)
	mux.HandleFunc("GET /e/{slug}", s.eventPage)
	mux.Handle("GET /e/{slug}/onboarding", auth.RequirePerson(http.HandlerFunc(s.onboardingForm)))
	mux.Handle("POST /e/{slug}/onboarding", auth.RequirePerson(http.HandlerFunc(s.onboardingSubmit)))
	mux.Handle("POST /e/{slug}/discoverable", auth.RequirePerson(http.HandlerFunc(s.setDiscoverable)))
	mux.HandleFunc("GET /e/{slug}/live", s.liveStream)

	s.registerSpots(mux)
	s.registerSpaces(mux)
	s.registerPosts(mux)
	s.registerMatches(mux)
	s.registerSponsors(mux)
	s.registerFeed(mux)
	s.registerWall(mux)
	s.registerCodes(mux)

	// Refuses cross-site state changes using Sec-Fetch-Site and Origin (spec.security).
	sameOrigin := http.NewCrossOriginProtection()
	return telemetry.HTTP(securityHeaders(sameOrigin.Handler(s.authn.Middleware(telemetry.Route(mux)))))
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		// A spot's URL carries its qr_slug, so it stays out of Referer headers to other sites.
		h.Set("Referrer-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}

func cacheFor(d time.Duration, next http.Handler) http.Handler {
	value := fmt.Sprintf("public, max-age=%d", int(d.Seconds()))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", value)
		next.ServeHTTP(w, r)
	})
}

func (s *Server) readyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.ready(ctx); err != nil {
		s.log.Error("not ready", "err", err)
		http.Error(w, "not ready", http.StatusServiceUnavailable)
		return
	}
	_, _ = fmt.Fprintln(w, "ready")
}

// event loads the {slug} event, writing the 404 page itself when there's no such event.
func (s *Server) event(w http.ResponseWriter, r *http.Request) (store.Event, bool) {
	e, err := s.store.EventBySlug(r.Context(), r.PathValue("slug"))
	if errors.Is(err, store.ErrNotFound) {
		s.render(w, r, http.StatusNotFound, views.Message(s.page(r, store.Event{}, "Not found"),
			"That event isn't here. Check the link or scan a code at the venue."))
		return store.Event{}, false
	}
	if err != nil {
		s.serverError(w, err)
		return store.Event{}, false
	}
	return e, true
}

// page builds what the shared layout needs for a request.
func (s *Server) page(r *http.Request, e store.Event, title string) views.Page {
	p := views.Page{
		Title:       title,
		Version:     s.version,
		ClerkKey:    s.clerkKey,
		ClerkScript: clerkScriptURL(s.clerkKey),
	}
	if e.Timezone != "" {
		if loc, err := time.LoadLocation(e.Timezone); err == nil {
			p.Now = time.Now().In(loc).Format("Mon Jan 2 · 3:04 PM")
		}
	}
	if person, ok := auth.Current(r.Context()); ok {
		p.SignedIn = true
		p.Initial = initial(person.DisplayName)
		if e.Slug != "" {
			p.LiveURL = live.URL(e.Slug, live.PersonTopic(person.ID))
		}
	}
	return p
}

// livePage is page plus the topics this page follows. Every page has one SSE connection, set up by
// the layout, and a signed-in person's own topic is always on it so their notices reach any page.
func (s *Server) livePage(r *http.Request, e store.Event, title string, topics ...string) views.Page {
	p := s.page(r, e, title)
	if person, ok := auth.Current(r.Context()); ok && e.Slug != "" {
		topics = append(topics, live.PersonTopic(person.ID))
	}
	if len(topics) > 0 {
		p.LiveURL = live.URL(e.Slug, topics...)
	}
	return p
}

// initial is the first letter of a display name, for the avatar.
func initial(name string) string {
	r, size := utf8.DecodeRuneInString(name)
	if size == 0 {
		return ""
	}
	return strings.ToUpper(string(r))
}

// render writes a templ component with a status.
func (s *Server) render(w http.ResponseWriter, r *http.Request, status int, c templ.Component) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := c.Render(r.Context(), w); err != nil {
		s.log.Error("render", "err", err, "path", r.URL.Path)
	}
}

func (s *Server) serverError(w http.ResponseWriter, err error) {
	s.log.Error("request failed", "err", err)
	http.Error(w, "Something went wrong on our side. Try again in a moment.", http.StatusInternalServerError)
}

// clerkScriptURL derives clerk-js's URL from the publishable key, which is pk_<env>_<base64 of
// "<frontend api host>$">.
func clerkScriptURL(publishableKey string) string {
	parts := strings.SplitN(publishableKey, "_", 3)
	if len(parts) != 3 {
		return ""
	}
	raw, err := base64.RawStdEncoding.DecodeString(strings.TrimRight(parts[2], "="))
	if err != nil {
		return ""
	}
	host := strings.TrimSuffix(string(raw), "$")
	if host == "" || strings.ContainsAny(host, "/ \"'<>") {
		return ""
	}
	return "https://" + host + "/npm/@clerk/clerk-js@5/dist/clerk.browser.js"
}
