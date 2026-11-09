package search

import (
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

const (
	maxTags      = 8
	maxTagLength = 32
	minWordRunes = 4
)

var (
	htmlTag = regexp.MustCompile(`<[^>]*>`)
	hashtag = regexp.MustCompile(`#([\p{L}\p{N}][\p{L}\p{N}_-]*)`)
	word    = regexp.MustCompile(`[\p{L}\p{N}]+(?:[-_.+][\p{L}\p{N}]+)*`)
)

// vocabulary maps the ways people write a technology to one tag, so "k8s" and "Kubernetes" overlap.
// Two-word phrases are tried before single words.
var vocabulary = map[string]string{
	"otel collector":          "otel-collector",
	"opentelemetry collector": "otel-collector",
	"tail sampling":           "tail-sampling",
	"service mesh":            "service-mesh",
	"github actions":          "github-actions",
	"vector search":           "vector-search",
	"otel":                    "otel",
	"opentelemetry":           "otel",
	"k8s":                     "kubernetes",
	"kubernetes":              "kubernetes",
	"golang":                  "go",
	"postgres":                "postgres",
	"postgresql":              "postgres",
	"elasticsearch":           "elasticsearch",
	"elser":                   "elasticsearch",
	"helm":                    "helm",
	"terraform":               "terraform",
	"docker":                  "docker",
	"prometheus":              "prometheus",
	"grafana":                 "grafana",
	"kafka":                   "kafka",
	"temporal":                "temporal",
	"tailscale":               "tailscale",
	"gitops":                  "gitops",
	"argocd":                  "argocd",
	"ebpf":                    "ebpf",
}

// stopwords are common words that say nothing about the topic of a post.
var stopwords = func() map[string]bool {
	words := strings.Fields(`
		about after again also anyone anything because been before being better between both
		cannot could does doing done down each even every from getting going good have having
		help here into just keep know like looking make many maybe more most much need needs
		only other over please really right same should show since some someone something still
		stuck such sure talk than that their them then there these they thing things think this
		those through under until very want wants well were what when where which while will
		with without work working would your yours happy compare notes curious share love today
		tomorrow`)
	set := make(map[string]bool, len(words))
	for _, w := range words {
		set[w] = true
	}
	return set
}()

// ExtractTags pulls up to eight tags out of a post: first the author's own #hashtags, then known
// technologies in a normal spelling, then the other meaningful words in the order they appear.
func ExtractTags(text string) []string {
	text = strings.ToLower(htmlTag.ReplaceAllString(text, " "))
	tags := []string{}
	add := func(tag string) {
		if tag != "" && utf8.RuneCountInString(tag) <= maxTagLength && !slices.Contains(tags, tag) && len(tags) < maxTags {
			tags = append(tags, tag)
		}
	}

	for _, m := range hashtag.FindAllStringSubmatch(text, -1) {
		add(m[1])
	}
	words := word.FindAllString(hashtag.ReplaceAllString(text, " "), -1)

	var others []string
	for i := 0; i < len(words); i++ {
		if i+1 < len(words) {
			if tag, ok := vocabulary[words[i]+" "+words[i+1]]; ok {
				add(tag)
				i++
				continue
			}
		}
		if tag, ok := vocabulary[words[i]]; ok {
			add(tag)
			continue
		}
		if utf8.RuneCountInString(words[i]) >= minWordRunes && !stopwords[words[i]] {
			others = append(others, words[i])
		}
	}
	for _, w := range others {
		add(w)
	}
	return tags
}
