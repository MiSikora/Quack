package tui

import (
	"sort"
	"strings"

	"github.com/sahilm/fuzzy"
)

// Every word of the text must match, in any order, and the item scores the sum of the words' scores. Text without
// words matches nothing.
func find(text string, items []string) fuzzy.Matches {
	words := strings.Fields(text)
	if len(words) == 0 {
		return nil
	}
	if len(words) == 1 {
		return fuzzy.Find(words[0], items)
	}
	matches := map[int]fuzzy.Match{}
	for _, match := range fuzzy.Find(words[0], items) {
		matches[match.Index] = match
	}
	for _, word := range words[1:] {
		next := map[int]fuzzy.Match{}
		for _, match := range fuzzy.Find(word, items) {
			if previous, ok := matches[match.Index]; ok {
				match.Score += previous.Score
				match.MatchedIndexes = append(previous.MatchedIndexes, match.MatchedIndexes...)
				next[match.Index] = match
			}
		}
		matches = next
	}
	found := make(fuzzy.Matches, 0, len(matches))
	for _, match := range matches {
		found = append(found, match)
	}
	sort.Slice(found, func(i, j int) bool {
		if found[i].Score != found[j].Score {
			return found[i].Score > found[j].Score
		}
		return found[i].Index < found[j].Index
	})
	return found
}
