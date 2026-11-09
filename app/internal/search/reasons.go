package search

import (
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

const (
	maxReasons      = 3
	maxSharedTags   = 2
	phraseWindow    = 6
	minPhraseWords  = 2
	maxPhraseRunes  = 40
	sameStemRunes   = 4
	sharedTagPrefix = "both mention "
)

var (
	clauseBreak = regexp.MustCompile(`[.:;!?,]+\s+|[.:;!?,]+$`)
	wordPart    = regexp.MustCompile(`[-_.+]`)
)

// terms are the meaningful words of some text, in the spelling the tag vocabulary uses, so that
// "k8s" and "Kubernetes" count as one word.
func terms(token string) []string {
	var out []string
	for _, part := range wordPart.Split(strings.ToLower(token), -1) {
		if tag, ok := vocabulary[part]; ok {
			out = append(out, tag)
			continue
		}
		if utf8.RuneCountInString(part) >= minWordRunes && !stopwords[part] {
			out = append(out, part)
		}
	}
	return out
}

func questionTerms(text string) []string {
	var out []string
	for _, w := range word.FindAllString(text, -1) {
		out = append(out, terms(w)...)
	}
	return out
}

// sameWord treats "drops" and "dropping" as one word without needing a stemmer: they share a stem
// that is all but at most the last letter of the shorter word, so "config" and "conference" differ.
func sameWord(a, b string) bool {
	if a == b {
		return true
	}
	ra, rb := []rune(a), []rune(b)
	shared := 0
	for shared < min(len(ra), len(rb)) && ra[shared] == rb[shared] {
		shared++
	}
	return shared >= sameStemRunes && shared >= min(len(ra), len(rb))-1
}

func matchesQuestion(w string, question []string) bool {
	for _, t := range terms(w) {
		if slices.ContainsFunc(question, func(q string) bool { return sameWord(q, t) }) {
			return true
		}
	}
	return false
}

// clip cuts a phrase to fit a chip, at a word.
func clip(phrase string) string {
	if utf8.RuneCountInString(phrase) <= maxPhraseRunes {
		return phrase
	}
	cut := string([]rune(phrase)[:maxPhraseRunes])
	if i := strings.LastIndex(cut, " "); i > 0 {
		return cut[:i]
	}
	return cut
}

// bestRun finds the stretch of words, at most phraseWindow long, that starts and ends on a word
// from the question and holds the most of them. score is 0 when no word matches.
func bestRun(words []string, question []string) (start, end, score int) {
	for i := range words {
		if !matchesQuestion(words[i], question) {
			continue
		}
		hits := 0
		for j := i; j < len(words) && j < i+phraseWindow; j++ {
			if !matchesQuestion(words[j], question) {
				continue
			}
			hits++
			if hits > score {
				start, end, score = i, j, hits
			}
		}
	}
	return start, end, score
}

// bestPhrase is the run of words a text shares with the question, within one clause, and how many
// of its words came from the question. Elasticsearch 8.17 has no semantic highlighter, so this is
// done here. Text and question are only read, so the result is as plain as the input.
func bestPhrase(question, text string) (phrase string, score int) {
	wanted := questionTerms(question)
	for _, clause := range clauseBreak.Split(text, -1) {
		words := strings.Fields(clause)
		start, end, runScore := bestRun(words, wanted)
		if runScore > score {
			score = runScore
			if end-start+1 < minPhraseWords && end+1 < len(words) {
				end++
			}
			phrase = strings.Join(words[start:end+1], " ")
		}
	}
	return clip(phrase), score
}

// StrongestPhrase is the short piece of text that best explains a match, and empty when the text
// shares no words with the question.
func StrongestPhrase(question, text string) string {
	phrase, _ := bestPhrase(question, text)
	return phrase
}

// readable is how a tag or phrase compares with another, whatever joins its words.
func readable(s string) string { return strings.ToLower(strings.ReplaceAll(s, "-", " ")) }

// Reasons is the 1-3 chips a match shows: up to two tags it shares with the question, and the
// phrase. A longer phrase says a tag for it, and a tag says a phrase that is only that tag. A phrase
// resting on one shared word is shown only if nothing else explains the match. It is empty when the
// match shares no tag or word with the question, and the matcher drops such a hit: ELSER always
// returns something, and its scores can't tell a near miss from noise.
func Reasons(question string, questionTags, docTags []string, text string) []string {
	best, score := bestPhrase(question, text)
	phrase := readable(best)
	out := make([]string, 0, maxReasons)
	sayPhrase := phrase != ""
	for _, tag := range questionTags {
		if len(out) == maxSharedTags {
			break
		}
		if !slices.Contains(docTags, tag) {
			continue
		}
		if readable(tag) == phrase {
			sayPhrase = false
		} else if strings.Contains(phrase, readable(tag)) {
			continue
		}
		out = append(out, sharedTagPrefix+tag)
	}
	if sayPhrase && (score >= minPhraseWords || len(out) == 0) {
		out = append(out, best)
	}
	return out
}
