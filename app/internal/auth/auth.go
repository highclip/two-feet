// Package auth verifies Clerk sessions, loads the current person and guards attendee routes.
package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/highclip/two-feet/app/internal/store"
)

// syncEvery is how often a signed-in person's role is copied from Clerk again.
const syncEvery = 2 * time.Minute

// ErrInvalidSession means the token is missing, expired or not signed by Clerk.
var ErrInvalidSession = errors.New("invalid session")

// Metadata is what an organizer sets on a Clerk user's public metadata from the dashboard.
type Metadata struct {
	Role    string `json:"role"`
	Sponsor string `json:"sponsor"`
}

// Provider is the part of Clerk the app uses, so handler tests can run without a network.
type Provider interface {
	// VerifySession returns the Clerk user id for a session token, or ErrInvalidSession.
	VerifySession(ctx context.Context, token string) (clerkUserID string, err error)
	// UserMetadata returns the public metadata for a Clerk user.
	UserMetadata(ctx context.Context, clerkUserID string) (Metadata, error)
}

// Authenticator resolves each request's session to a person.
type Authenticator struct {
	store    *store.Store
	provider Provider
	log      *slog.Logger

	mu     sync.Mutex
	synced map[string]time.Time
}

// New returns an Authenticator.
func New(s *store.Store, p Provider, log *slog.Logger) *Authenticator {
	return &Authenticator{store: s, provider: p, log: log, synced: map[string]time.Time{}}
}

type contextKey struct{}

// Middleware puts the current person in the request context when the session is valid. Requests
// without a valid session carry on as visitors; RequirePerson turns those away where it matters.
func (a *Authenticator) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := sessionToken(r)
		if token == "" {
			next.ServeHTTP(w, r)
			return
		}
		clerkUserID, err := a.provider.VerifySession(r.Context(), token)
		if err != nil {
			if !errors.Is(err, ErrInvalidSession) {
				a.log.Error("verify session", "err", err)
			}
			next.ServeHTTP(w, r)
			return
		}
		person, err := a.person(r.Context(), clerkUserID)
		if err != nil {
			a.log.Error("load person", "err", err)
			http.Error(w, "Something went wrong on our side. Try again in a moment.", http.StatusInternalServerError)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), contextKey{}, person)))
	})
}

// person returns the row for a Clerk user, creating it on first sight and copying the role and
// sponsor from Clerk at most every syncEvery.
func (a *Authenticator) person(ctx context.Context, clerkUserID string) (store.Person, error) {
	existing, err := a.store.PersonByClerkID(ctx, clerkUserID)
	exists := err == nil
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return store.Person{}, err
	}
	if exists && !a.dueForSync(clerkUserID) {
		return existing, nil
	}

	meta, err := a.provider.UserMetadata(ctx, clerkUserID)
	if err != nil {
		// Clerk being down shouldn't lock anyone out. An existing person keeps their role and a
		// new one starts as an attendee, the least privileged role, until the next sync.
		a.log.Error("read clerk metadata", "err", err)
		if exists {
			return existing, nil
		}
		meta = Metadata{Role: store.RoleAttendee}
	}
	person, err := a.store.EnsurePerson(ctx, clerkUserID, meta.Role, meta.Sponsor)
	if err != nil {
		return store.Person{}, fmt.Errorf("ensure person: %w", err)
	}
	return person, nil
}

func (a *Authenticator) dueForSync(clerkUserID string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if last, ok := a.synced[clerkUserID]; ok && time.Since(last) < syncEvery {
		return false
	}
	a.synced[clerkUserID] = time.Now()
	return true
}

// sessionToken reads Clerk's session JWT from the Authorization header or the __session cookie that
// clerk-js keeps fresh in the browser.
func sessionToken(r *http.Request) string {
	if h := r.Header.Get("Authorization"); h != "" {
		return strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
	}
	if c, err := r.Cookie("__session"); err == nil {
		return c.Value
	}
	return ""
}

// Current returns the signed-in person, if there is one.
func Current(ctx context.Context) (store.Person, bool) {
	p, ok := ctx.Value(contextKey{}).(store.Person)
	return p, ok
}

// RequirePerson guards an attendee route. A page load is redirected to sign-in and comes back here
// afterwards; htmx calls, API calls and form posts get 401.
func RequirePerson(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := Current(r.Context()); ok {
			next.ServeHTTP(w, r)
			return
		}
		isPage := (r.Method == http.MethodGet || r.Method == http.MethodHead) &&
			r.Header.Get("HX-Request") == "" &&
			!strings.HasPrefix(r.URL.Path, "/api/") &&
			!strings.Contains(r.Header.Get("Accept"), "application/json")
		if isPage {
			http.Redirect(w, r, SignInURL(r.URL.RequestURI()), http.StatusSeeOther) // #nosec G710 -- always /sign-in; the request URI is only a query-escaped parameter
			return
		}
		http.Error(w, "Your session has ended. Sign in again to carry on.", http.StatusUnauthorized)
	})
}

// HasRole reports whether a person has one of the roles.
func HasRole(p store.Person, roles ...string) bool {
	return slices.Contains(roles, p.Role)
}

// SignInURL returns the sign-in page, set to come back to a same-origin path afterwards.
func SignInURL(returnTo string) string {
	if !SafeReturn(returnTo) {
		return "/sign-in"
	}
	return "/sign-in?return=" + url.QueryEscape(returnTo)
}

// SafeReturn reports whether a return path stays on this site. Browsers strip tabs and newlines and
// treat a backslash as a slash, so those could turn a path into another origin.
func SafeReturn(path string) bool {
	if !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") {
		return false
	}
	if strings.ContainsFunc(path, func(c rune) bool { return c < 0x20 || c == 0x7f || c == '\\' }) {
		return false
	}
	u, err := url.Parse(path)
	return err == nil && u.Scheme == "" && u.Host == ""
}
