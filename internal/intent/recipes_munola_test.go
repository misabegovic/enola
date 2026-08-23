package intent

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The catalogue this build carries beyond upstream's set parses, validates,
// and cites where it came from, like every other shipped recipe.
func TestMunolaRecipes_ShipWithThisBuild(t *testing.T) {
	recipes, problems := MunolaRecipes()
	if len(problems) != 0 {
		t.Fatalf("a carried recipe that does not parse is a build defect: %v", problems)
	}
	want := []string{"api-boundaries", "background-work", "data-ownership", "ember-conventions"}
	if len(recipes) != len(want) {
		t.Fatalf("carried recipes = %d, want %d", len(recipes), len(want))
	}
	for i, rec := range recipes {
		if rec.Name != want[i] {
			t.Fatalf("carried recipe %d = %q, want %q", i, rec.Name, want[i])
		}
		if !strings.HasPrefix(rec.Path, MunolaRecipeSource) {
			t.Fatalf("a carried recipe cites where it came from, got %q", rec.Path)
		}
		for _, rule := range rec.Rules {
			if strings.TrimSpace(rule.Because) == "" {
				t.Fatalf("a carried rule carries its reason: %+v", rule)
			}
		}
	}
	if problems, _ := RecipeProblems(recipes); len(problems) != 0 {
		t.Fatalf("carried recipes must satisfy the same validator: %v", problems)
	}
	all, problems := BuiltinRecipes()
	if len(problems) != 0 {
		t.Fatalf("upstream and carried recipes load together: %v", problems)
	}
	seen := map[string]int{}
	for _, rec := range all {
		seen[rec.Name]++
	}
	for _, name := range want {
		if seen[name] != 1 {
			t.Fatalf("%s appears %d times among the built-ins", name, seen[name])
		}
	}
}

// A release holds the carried copies identical to the enola-guides gem's
// files; RECIPES_SOURCE names a checkout of that gem, and the release workflow
// sets it to the tag the notes name.
func TestMunolaRecipes_MatchTheGuides(t *testing.T) {
	source := os.Getenv("RECIPES_SOURCE")
	if source == "" {
		t.Skip("RECIPES_SOURCE not set; the release workflow runs this against the enola-guides tag")
	}
	recipes, problems := MunolaRecipes()
	if len(problems) != 0 {
		t.Fatal(problems)
	}
	for _, rec := range recipes {
		name := filepath.Base(rec.Path)
		want, err := os.ReadFile(filepath.Join(source, "recipes", name))
		if err != nil {
			t.Fatalf("the guides checkout has no %s: %v", name, err)
		}
		got, err := EmbeddedMunolaRecipe(name)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("%s differs from the guides' copy; run scripts/munola-recipes-sync.sh %s", name, source)
		}
	}
}
