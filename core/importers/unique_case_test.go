package importers

import (
	"testing"

	"github.com/pgodlews/verity-api/core/model"
)

func TestUniqueCaseNames(t *testing.T) {
	got := uniqueCaseNames([]model.TestCase{{Name: "op"}, {Name: "op"}, {Name: "op (2)"}, {Name: ""}})
	want := []string{"op", "op (2)", "op (2) (2)", "Unnamed"}
	for i, c := range got {
		if c.Name != want[i] {
			t.Fatalf("names = %v", got)
		}
	}
}
