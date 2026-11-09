package search_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/highclip/two-feet/app/internal/search"
)

func TestStrongestPhraseFindsTheWordsTheTextSharesWithTheQuestion(t *testing.T) {
	const question = "our OTel collector drops spans under load on k8s"
	cases := []struct {
		name, text, want string
	}{
		{"keeps the run of matching words", "Collector tuning: dropping spans under load. Bring your config", "dropping spans under load"},
		{"knows other spellings", "Rebuilding our OTel collector pipelines with tail sampling, happy to compare notes", "OTel collector"},
		{"reads through a hyphen and stays in its clause", "OpenTelemetry-native observability: traces, metrics and logs", "OpenTelemetry-native observability"},
		{"says nothing when no words are shared", "Durable workflows that survive failures and retries", ""},
		{"nothing to say about nothing", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, search.StrongestPhrase(question, tc.text))
		})
	}
}

func TestStrongestPhraseStaysShortEnoughForAChip(t *testing.T) {
	long := "supercalifragilisticexpialidocious antidisestablishmentarianism floccinaucinihilipilification words"

	got := search.StrongestPhrase("words", long+" more")

	assert.LessOrEqual(t, len([]rune(got)), 40)
	assert.NotEmpty(t, got)
}

func TestReasonsNeverRepeatATagThePhraseAlreadySays(t *testing.T) {
	got := search.Reasons("drops spans under load", []string{"spans", "load", "kubernetes"},
		[]string{"spans", "load", "kubernetes"}, "dropping spans under load")

	assert.Equal(t, []string{"both mention kubernetes", "dropping spans under load"}, got)
}

func TestReasonsAreOneToThree(t *testing.T) {
	tags := []string{"otel", "postgres", "helm", "docker", "go"}

	three := search.Reasons("kafka consumers rebalance on restart", tags, tags, "kafka consumers rebalance constantly")
	assert.Equal(t, []string{"both mention otel", "both mention postgres", "kafka consumers rebalance"}, three)
}

func TestReasonsDropAOneWordPhraseWhenATagExplainsTheMatch(t *testing.T) {
	got := search.Reasons("our collector on k8s", []string{"kubernetes"}, []string{"kubernetes"},
		"Helm upgrades on k8s keep timing out")

	assert.Equal(t, []string{"both mention kubernetes"}, got)
}

func TestReasonsAreEmptyWhenNothingLinesUp(t *testing.T) {
	assert.Empty(t, search.Reasons("our trace pipeline", nil, nil, "OpenTelemetry-native observability"))
	assert.Empty(t, search.Reasons("anything", nil, nil, ""))
}

func TestReasonsDoNotCountWordsThatOnlyStartAlike(t *testing.T) {
	assert.Empty(t, search.Reasons("config drift", nil, nil, "Conference swag and stickers"))
	assert.Equal(t, []string{"retries"}, search.Reasons("retry storms", nil, nil, "retries"))
}
