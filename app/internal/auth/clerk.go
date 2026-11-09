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

// Clerk is the real Provider, backed by the Clerk API.
type Clerk struct {
	jwks      *jwks.Client
	users     *user.Client
	publicURL string

	mu   sync.RWMutex
	keys map[string]*clerk.JSONWebKey
}

// NewClerk returns a Provider. Session tokens must have been issued for publicURL.
func NewClerk(secretKey, publicURL string) *Clerk {
	cfg := &clerk.ClientConfig{}
	cfg.Key = clerk.String(secretKey)
	return &Clerk{
		jwks:      jwks.NewClient(cfg),
		users:     user.NewClient(cfg),
		publicURL: strings.TrimRight(publicURL, "/"),
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

// key returns a signing key, fetching Clerk's key set only for a key id it hasn't seen.
func (c *Clerk) key(ctx context.Context, keyID string) (*clerk.JSONWebKey, error) {
	c.mu.RLock()
	key, ok := c.keys[keyID]
	c.mu.RUnlock()
	if ok {
		return key, nil
	}
	key, err := jwt.GetJSONWebKey(ctx, &jwt.GetJSONWebKeyParams{KeyID: keyID, JWKSClient: c.jwks})
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.keys[keyID] = key
	c.mu.Unlock()
	return key, nil
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
