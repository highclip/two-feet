package search_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/highclip/two-feet/app/internal/search"
)

func TestExtractTags(t *testing.T) {
	cases := []struct {
		name, text string
		want       []string
	}{
		{"known phrases come first and are normalised",
			"our OTel collector drops spans under load on k8s",
			[]string{"otel-collector", "kubernetes", "drops", "spans", "load"}},
		{"a shared phrase across two posts",
			"Rebuilding our OTel collector pipelines with tail sampling, happy to compare notes",
			[]string{"otel-collector", "tail-sampling", "rebuilding", "pipelines"}},
		{"spellings of the same thing collapse",
			"OpenTelemetry on Kubernetes vs otel on K8S",
			[]string{"otel", "kubernetes"}},
		{"hashtags are the tags an author adds",
			"Anyone running this on a #Raspberry-Pi cluster?",
			[]string{"raspberry-pi", "running", "cluster"}},
		{"html doesn't become a tag",
			"<script>alert(1)</script> breaks our helm chart",
			[]string{"helm", "alert", "breaks", "chart"}},
		{"only stopwords gives no tags", "I am so very stuck on this", []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, search.ExtractTags(tc.text))
		})
	}
}

func TestExtractTagsKeepsAtMostEightShortTags(t *testing.T) {
	tags := search.ExtractTags("alpha bravo charlie delta foxtrot hotel india juliet kilo lima mike " +
		"#" + strings.Repeat("x", 60))
	assert.LessOrEqual(t, len(tags), 8)
	for _, tag := range tags {
		assert.LessOrEqual(t, len(tag), 32)
	}
}
