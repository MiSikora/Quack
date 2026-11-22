package tui

import (
	"reflect"
	"testing"
)

func TestEveryWordOfTheTextMatchesInAnyOrder(t *testing.T) {
	items := []string{"goto.settings", "goto.sync_settings", "feature.toggle"}

	for _, text := range []string{"sync set", "set sync", "goto  sync"} {
		var got []string
		for _, match := range find(text, items) {
			got = append(got, match.Str)
		}
		if want := []string{"goto.sync_settings"}; !reflect.DeepEqual(got, want) {
			t.Errorf("%q matches %q, want %q", text, got, want)
		}
	}
}

func TestAWordThatMatchesNothingMatchesNothing(t *testing.T) {
	if got := find("sync zz", []string{"goto.sync_settings"}); len(got) != 0 {
		t.Errorf("matches %v, want none", got)
	}
}

func TestOnlySpacesMatchNothing(t *testing.T) {
	if got := find("  ", []string{"goto.sync_settings"}); len(got) != 0 {
		t.Errorf("matches %v, want none", got)
	}
}

func TestWordsHighlightWhatEachOfThemMatched(t *testing.T) {
	got := find("to go", []string{"goto"})

	if len(got) != 1 || !reflect.DeepEqual(got[0].MatchedIndexes, []int{2, 3, 0, 1}) {
		t.Errorf("matches %v, want the indexes of both words", got)
	}
}

func TestActionsMatchTheWordsOfTheText(t *testing.T) {
	h := start(t, newFakeApp())

	h.typing("del acc")

	h.waitFor("1/6", "▸ account.delete")
}

func TestSpacesAloneMatchEveryAction(t *testing.T) {
	h := start(t, newFakeApp())

	h.typing("  ")

	h.waitFor("6/6")
}
