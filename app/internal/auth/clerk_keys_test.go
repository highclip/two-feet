package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/clerk/clerk-sdk-go/v2"
	"github.com/clerk/clerk-sdk-go/v2/jwks"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeKeySet stands in for Clerk's key set endpoint and counts how often it is asked.
type fakeKeySet struct {
	kids  atomic.Value // []string
	err   atomic.Value // error wrapper, see failWith
	calls atomic.Int32
}

type fetchFailure struct{ err error }

func newFakeKeySet(kids ...string) *fakeKeySet {
	f := &fakeKeySet{}
	f.kids.Store(kids)
	f.err.Store(fetchFailure{})
	return f
}

func (f *fakeKeySet) failWith(err error) { f.err.Store(fetchFailure{err}) }

func (f *fakeKeySet) Get(ctx context.Context, _ *jwks.GetParams) (*clerk.JSONWebKeySet, error) {
	f.calls.Add(1)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if failure := f.err.Load().(fetchFailure); failure.err != nil {
		return nil, failure.err
	}
	set := &clerk.JSONWebKeySet{}
	for _, kid := range f.kids.Load().([]string) {
		set.Keys = append(set.Keys, &clerk.JSONWebKey{KeyID: kid})
	}
	return set, nil
}

func newTestClerk(keys keySetGetter) (*Clerk, *time.Time) {
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	c := NewClerk("sk_test_unused", "http://localhost")
	c.jwks = keys
	c.now = func() time.Time { return now }
	return c, &now
}

// forgedToken is a well-formed JWT carrying a key id, signed by nobody.
func forgedToken(t *testing.T, kid string) string {
	t.Helper()
	enc := base64.RawURLEncoding
	header := enc.EncodeToString(fmt.Appendf(nil, `{"alg":"RS256","typ":"JWT","kid":%q}`, kid))
	payload := enc.EncodeToString([]byte(`{"sub":"user_forged"}`))
	return header + "." + payload + "." + enc.EncodeToString([]byte("not-a-signature"))
}

func randomKid(t *testing.T) string {
	t.Helper()
	b := make([]byte, 8)
	_, err := rand.Read(b)
	require.NoError(t, err)
	return "kid_" + hex.EncodeToString(b)
}

func TestManyRandomKidsCauseAtMostOneFetchPerInterval(t *testing.T) {
	keys := newFakeKeySet("real")
	c, now := newTestClerk(keys)

	for range 200 {
		_, err := c.VerifySession(context.Background(), forgedToken(t, randomKid(t)))
		require.ErrorIs(t, err, ErrInvalidSession)
	}
	assert.EqualValues(t, 1, keys.calls.Load(), "one fetch inside the interval")

	*now = now.Add(keyRefreshEvery)
	for range 200 {
		_, err := c.VerifySession(context.Background(), forgedToken(t, randomKid(t)))
		require.ErrorIs(t, err, ErrInvalidSession)
	}
	assert.EqualValues(t, 2, keys.calls.Load(), "one more fetch in the next interval")
}

func TestConcurrentRandomKidsCauseOneFetch(t *testing.T) {
	keys := newFakeKeySet("real")
	c, _ := newTestClerk(keys)

	var wg sync.WaitGroup
	for range 50 {
		token := forgedToken(t, randomKid(t))
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := c.VerifySession(context.Background(), token)
			assert.ErrorIs(t, err, ErrInvalidSession)
		}()
	}
	wg.Wait()
	assert.EqualValues(t, 1, keys.calls.Load())
}

func TestOneFetchCachesEveryKey(t *testing.T) {
	keys := newFakeKeySet("one", "two")
	c, _ := newTestClerk(keys)

	for _, kid := range []string{"one", "two", "one"} {
		key, err := c.key(context.Background(), kid)
		require.NoError(t, err)
		assert.Equal(t, kid, key.KeyID)
	}
	assert.EqualValues(t, 1, keys.calls.Load())
}

func TestARotatedKeyIsFoundAfterTheInterval(t *testing.T) {
	keys := newFakeKeySet("old")
	c, now := newTestClerk(keys)
	_, err := c.key(context.Background(), "old")
	require.NoError(t, err)

	keys.kids.Store([]string{"old", "new"})
	_, err = c.key(context.Background(), "new")
	require.ErrorIs(t, err, ErrInvalidSession, "inside the interval an unknown key is refused without a fetch")

	*now = now.Add(keyRefreshEvery)
	key, err := c.key(context.Background(), "new")
	require.NoError(t, err)
	assert.Equal(t, "new", key.KeyID)
	assert.EqualValues(t, 2, keys.calls.Load())
}

func TestRealRequestsArrivingTogetherAfterAStartAllGetTheKey(t *testing.T) {
	keys := newFakeKeySet("real")
	c, _ := newTestClerk(keys)

	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := c.key(context.Background(), "real")
			assert.NoError(t, err)
		}()
	}
	wg.Wait()
	assert.EqualValues(t, 1, keys.calls.Load())
}

func TestAFailedFetchStillCountsAgainstTheInterval(t *testing.T) {
	keys := newFakeKeySet("real")
	keys.failWith(errors.New("clerk is down"))
	c, now := newTestClerk(keys)

	_, err := c.key(context.Background(), "real")
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrInvalidSession, "an outage is logged, unlike a bad token")

	_, err = c.key(context.Background(), "real")
	require.ErrorIs(t, err, ErrInvalidSession)
	assert.EqualValues(t, 1, keys.calls.Load())

	keys.failWith(nil)
	*now = now.Add(keyRefreshEvery)
	_, err = c.key(context.Background(), "real")
	require.NoError(t, err)
}

func TestACancelledRequestCannotWasteTheFetch(t *testing.T) {
	keys := newFakeKeySet("real")
	c, _ := newTestClerk(keys)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	key, err := c.key(ctx, "real")
	require.NoError(t, err)
	assert.Equal(t, "real", key.KeyID)
}
