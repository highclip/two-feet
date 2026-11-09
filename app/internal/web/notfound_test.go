package web_test

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCatchAll_NearMissesRedirectToTheEventHome(t *testing.T) {
	f := newFixture(t)
	for target, want := range map[string]string{
		"/e":                     "/e/cnh",
		"/e/":                    "/e/cnh",
		"/e/cnh/":                "/e/cnh",
		"/e/cnh/?spot=abc":       "/e/cnh?spot=abc", // a code's link still checks you in
		"/e/cnh/spaces":          "/e/cnh",
		"/e/cnh/spaces/":         "/e/cnh",
		"/e/cnh/posts":           "/e/cnh",
		"/e/cnh/checkin":         "/e/cnh",
		"/e/cnh/discoverable":    "/e/cnh",
		"/e/cnh/spaces?spot=abc": "/e/cnh",
	} {
		w := f.get(target)
		assert.Equal(t, http.StatusSeeOther, w.Code, target)
		assert.Equal(t, want, w.Header().Get("Location"), target)
	}
}

func TestCatchAll_AnythingElseIsAPageWithAWayHome(t *testing.T) {
	f := newFixture(t)
	for _, target := range []string{"/nope", "/e/cnh/nope", "/e/other/spaces", "/e/cnhx/"} {
		w := f.get(target)
		assert.Equal(t, http.StatusNotFound, w.Code, target)
		assert.Contains(t, w.Header().Get("Content-Type"), "text/html", target)
		assert.Contains(t, w.Body.String(), "That page isn&#39;t here", target)
		assert.Contains(t, w.Body.String(), `href="/e/cnh"`, target)
	}
}

func TestCatchAll_HtmxGetsAPlainStatus(t *testing.T) {
	f := newFixture(t)
	for _, target := range []string{"/nope", "/e/cnh/spaces"} {
		w := f.get(target, header("HX-Request", "true"))
		assert.Equal(t, http.StatusNotFound, w.Code, target)
		assert.Empty(t, w.Header().Get("Location"), target)
		assert.NotContains(t, w.Body.String(), "<html", target)
	}
}

func TestCatchAll_LeavesRealRoutesAndPostsAlone(t *testing.T) {
	f := newFixture(t)

	w := f.get("/healthz")
	assert.Equal(t, http.StatusOK, w.Code)

	w = f.get("/static/does-not-exist.js")
	assert.Equal(t, http.StatusNotFound, w.Code)
	assert.NotContains(t, w.Body.String(), "<html")

	assert.NotEqual(t, http.StatusSeeOther, f.post("/nope", url.Values{}).Code)
}
