package web_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/highclip/two-feet/app/internal/auth"
	"github.com/highclip/two-feet/app/internal/store"
	"github.com/highclip/two-feet/app/internal/web"
)

const (
	codesPath    = "/e/cnh/codes"
	signSentence = "Open space here. Scan to see what's on or start one."
)

func (f *fixture) allSpots(t *testing.T) []store.Spot {
	t.Helper()
	ctx := context.Background()
	e, err := f.store.EventBySlug(ctx, "cnh")
	require.NoError(t, err)
	spots, err := f.store.Spots(ctx, e.ID)
	require.NoError(t, err)
	require.NotEmpty(t, spots)
	return spots
}

// signs splits the codes page into one chunk of HTML per printed sign.
func signs(body string) []string {
	parts := strings.Split(body, "data-sign")
	return parts[1:]
}

func (f *fixture) organizerCodes(t *testing.T) string {
	t.Helper()
	f.meta["user_org"] = auth.Metadata{Role: store.RoleOrganizer}
	w := f.get(codesPath, as("user_org"))
	require.Equal(t, http.StatusOK, w.Code)
	return w.Body.String()
}

func TestS11_OrganizerOpensCodesEachSpotPrintsOnePageWithNameAndSentence(t *testing.T) {
	f := newFixture(t)
	spots := f.allSpots(t)

	body := f.organizerCodes(t)

	got := signs(body)
	require.Len(t, got, len(spots), "one sign per spot")
	for i, sp := range spots {
		text := visible(`<div ` + got[i])
		assert.Contains(t, text, sp.Name)
		assert.Contains(t, text, signSentence)
	}
}

func TestS11_PrintedSignQREncodesTheSpotURLWithNoSessionOrPersonalData(t *testing.T) {
	f := newFixture(t)
	f.meta["user_org"] = auth.Metadata{Role: store.RoleOrganizer}
	f.onboard(t, "user_org", "Olive Organizer")
	spots := f.allSpots(t)

	first := f.get(codesPath, as("user_org"))
	require.Equal(t, http.StatusOK, first.Code)
	got := signs(first.Body.String())
	require.Len(t, got, len(spots))

	for i, sp := range spots {
		want, _, err := web.EventQRPath("https://twofeet.test/e/cnh?spot=" + sp.QRSlug)
		require.NoError(t, err)
		assert.Contains(t, got[i], want, "the code is exactly the spot's URL, so nothing else can be in it: %s", sp.Name)
	}

	f.meta["user_other"] = auth.Metadata{Role: store.RoleOrganizer}
	f.onboard(t, "user_other", "Ori Other")
	second := f.get(codesPath, as("user_other"))
	require.Equal(t, http.StatusOK, second.Code)
	for i := range spots {
		assert.Equal(t, got[i], signs(second.Body.String())[i], "a sign doesn't depend on who printed it")
	}
}

func TestS11_NonOrganizerOpensCodesPageGets403(t *testing.T) {
	f := newFixture(t)
	f.meta["user_rep"] = auth.Metadata{Role: store.RoleSponsorRep, Sponsor: "Dash0"}
	f.onboard(t, "user_a", "Ana")
	f.onboard(t, "user_rep", "Rae")
	spots := f.allSpots(t)

	for name, user := range map[string]string{"attendee": "user_a", "sponsor rep": "user_rep", "no name yet": "user_new"} {
		t.Run(name, func(t *testing.T) {
			w := f.get(codesPath, as(user))

			assert.Equal(t, http.StatusForbidden, w.Code)
			for _, sp := range spots {
				assert.NotContains(t, w.Body.String(), sp.QRSlug)
			}
		})
	}
}

func TestS11_SignedOutVisitorIsSentToSignInAndSeesNoCodes(t *testing.T) {
	f := newFixture(t)
	spots := f.allSpots(t)

	w := f.get(codesPath)

	assert.Equal(t, http.StatusSeeOther, w.Code)
	assert.Equal(t, auth.SignInURL(codesPath), w.Header().Get("Location"))
	for _, sp := range spots {
		assert.NotContains(t, w.Body.String(), sp.QRSlug)
	}
}

func TestS11_CodesPageForUnknownEventIs404(t *testing.T) {
	f := newFixture(t)
	f.meta["user_org"] = auth.Metadata{Role: store.RoleOrganizer}

	assert.Equal(t, http.StatusNotFound, f.get("/e/nope/codes", as("user_org")).Code)
}

func TestS11_CodesPageIsNotStored(t *testing.T) {
	f := newFixture(t)
	f.meta["user_org"] = auth.Metadata{Role: store.RoleOrganizer}

	w := f.get(codesPath, as("user_org"))

	assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
}

// D11: the badge sticker carries the event's own QR, with no spot in it.
func TestS11_StickerSheetCarriesTheEventQR(t *testing.T) {
	f := newFixture(t)
	want, _, err := web.EventQRPath("https://twofeet.test/e/cnh")
	require.NoError(t, err)

	body := f.organizerCodes(t)

	assert.Equal(t, 12, strings.Count(body, "data-sticker"))
	assert.Equal(t, 12, strings.Count(body, want), "every sticker is the event's code")
}

func TestS11_VisitorSeesThePitchAndAWayToSignIn(t *testing.T) {
	f := newFixture(t)

	w := f.get("/e/cnh")

	require.Equal(t, http.StatusOK, w.Code)
	text := visible(w.Body.String())
	assert.Contains(t, text, "Open spaces used to happen once a day, on a wall of sticky notes.")
	assert.Contains(t, text, "Post a problem in one line")
	for _, kind := range []string{"Stuck on", "Curious", "Can help"} {
		assert.Contains(t, text, kind, "the page shows the three kinds of note")
	}
	assert.Contains(t, w.Body.String(), auth.SignInURL("/e/cnh"))
	assert.Contains(t, w.Body.String(), `href="/e/cnh/wall"`)
}

func TestS11_SignedInPersonStillGetsHomeNotLanding(t *testing.T) {
	f := newFixture(t)
	f.onboard(t, "user_a", "Ana")

	w := f.get("/e/cnh", as("user_a"))

	require.Equal(t, http.StatusOK, w.Code)
	assert.NotContains(t, visible(w.Body.String()), "Open spaces used to happen once a day")
}
