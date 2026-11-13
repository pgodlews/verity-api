package secrets

import (
	"reflect"
	"testing"
)

func TestInsertOrderedMatchesFullSort(t *testing.T) {
	lits := map[string]struct{}{}
	var ordered []string
	for _, s := range []string{"bbbb", "a-long-secret", "aaaa", "zz-literal", "cccccc", "abcd", "x-even-longer-secret"} {
		lits[s] = struct{}{}
		ordered = insertOrdered(ordered, s)
		if want := orderedLiterals(lits); !reflect.DeepEqual(ordered, want) {
			t.Fatalf("after %q: %v, want %v", s, ordered, want)
		}
	}
}
