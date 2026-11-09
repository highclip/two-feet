package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/clerk/clerk-sdk-go/v2"
	"github.com/clerk/clerk-sdk-go/v2/jwks"
	"github.com/clerk/clerk-sdk-go/v2/jwt"
	"github.com/clerk/clerk-sdk-go/v2/user"
)

// tokenLeeway forgives small clock differences between Clerk and this server.
const tokenLeeway = 5 * time.Second

// keyRefreshEvery limits how often Clerk's key set is fetched. A session cookie names its own key
// id, so without a limit every forged cookie with a new id would cost a call to Clerk.
const keyRefreshEvery = time.Minute

// keyFetchTimeout bounds a key set fetch, which runs apart from the request that triggered it.
const keyFetchTimeout = 5 * time.Second

// keySetGetter is the part of Clerk's JWKS client used here.
type keySetGetter interface {
	Get(ctx context.Context, params *jwks.GetParams) (*clerk.JSONWebKeySet, error)
}

// Clerk is the real Provider, backed by the Clerk API.
type Clerk struct {
	jwks      keySetGetter
	users     *user.Client
	publicURL string
	now       func() time.Time

	mu   sync.RWMutex
	keys map[string]*clerk.JSONWebKey

	// fetchMu allows one fetch at a time and guards fetchedAt. It is separate from mu so requests
	// with a known key don't wait for a fetch.
	fetchMu   sync.Mutex
	fetchedAt time.Time
}

// NewClerk returns a Provider. Session tokens must have been issued for publicURL.
func NewClerk(secretKey, publicURL string) *Clerk {
	cfg := &clerk.ClientConfig{}
	cfg.Key = clerk.String(secretKey)
	return &Clerk{
		jwks:      jwks.NewClient(cfg),
		users:     user.NewClient(cfg),
		publicURL: strings.TrimRight(publicURL, "/"),
		now:       time.Now,
		keys:      map[string]*clerk.JSONWebKey{},
	}
}

// VerifySession checks a session JWT against Clerk's signing keys.
func (c *Clerk) VerifySession(ctx context.Context, token string) (string, error) {
	unverified, err := jwt.Decode(ctx, &jwt.DecodeParams{Token: token})
	if err != nil {
		return "", fmt.Errorf("%w: decode: %v", ErrInvalidSession, err)
	}
	key, err := c.key(ctx, unverified.KeyID)
	if err != nil {
		return "", fmt.Errorf("get signing key: %w", err)
	}
	claims, err := jwt.Verify(ctx, &jwt.VerifyParams{
		Token:                  token,
		JWK:                    key,
		Leeway:                 tokenLeeway,
		AuthorizedPartyHandler: func(azp string) bool { return azp == "" || azp == c.publicURL },
	})
	if err != nil {
		return "", fmt.Errorf("%w: verify: %v", ErrInvalidSession, err)
	}
	return claims.Subject, nil
}

// key returns a signing key. An id it hasn't seen refreshes Clerk's whole key set, at most once per
// keyRefreshEvery; an id that is still unknown, or unknown inside that interval, is ErrInvalidSession.
func (c *Clerk) key(ctx context.Context, keyID string) (*clerk.JSONWebKey, error) {
	if key, ok := c.cachedKey(keyID); ok {
		return key, nil
	}

	c.fetchMu.Lock()
	defer c.fetchMu.Unlock()
	// The fetch this request waited for may have brought the key in.
	if key, ok := c.cachedKey(keyID); ok {
		return key, nil
	}
	now := c.now()
	if !c.fetchedAt.IsZero() && now.Sub(c.fetchedAt) < keyRefreshEvery {
		return nil, fmt.Errorf("%w: unknown signing key", ErrInvalidSession)
	}
	// Set before the call so a failing Clerk is asked once per interval too.
	c.fetchedAt = now

	// Detached from the request: a client that hangs up must not use up the interval's only fetch.
	fetchCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), keyFetchTimeout)
	defer cancel()
	set, err := c.jwks.Get(fetchCtx, &jwks.GetParams{})
	if err != nil {
		return nil, fmt.Errorf("fetch key set: %w", err)
	}
	keys := make(map[string]*clerk.JSONWebKey, len(set.Keys))
	for _, k := range set.Keys {
		if k != nil {
			keys[k.KeyID] = k
		}
	}
	c.mu.Lock()
	c.keys = keys
	c.mu.Unlock()

	key, ok := keys[keyID]
	if !ok {
		return nil, fmt.Errorf("%w: unknown signing key", ErrInvalidSession)
	}
	return key, nil
}

func (c *Clerk) cachedKey(keyID string) (*clerk.JSONWebKey, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	key, ok := c.keys[keyID]
	return key, ok
}

// UserMetadata reads the role and sponsor name from a Clerk user's public metadata.
func (c *Clerk) UserMetadata(ctx context.Context, clerkUserID string) (Metadata, error) {
	u, err := c.users.Get(ctx, clerkUserID)
	if err != nil {
		return Metadata{}, fmt.Errorf("get clerk user: %w", err)
	}
	var meta Metadata
	if len(u.PublicMetadata) > 0 {
		if err := json.Unmarshal(u.PublicMetadata, &meta); err != nil {
			return Metadata{}, fmt.Errorf("parse clerk metadata: %w", err)
		}
	}
	return meta, nil
}
