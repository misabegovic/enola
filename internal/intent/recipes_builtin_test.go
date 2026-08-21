package intent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A convention set ships with the binary, so a repository adopts it by
// instantiating it rather than by authoring it.
func TestBuiltinRecipes_ShipWithTheBinary(t *testing.T) {
	recipes, problems := BuiltinRecipes()
	if len(problems) != 0 {
		t.Fatalf("a shipped recipe that does not parse is a build defect: %v", problems)
	}
	byName := map[string]Recipe{}
	for _, rec := range recipes {
		byName[rec.Name] = rec
	}
	rails, ok := byName["rails-conventions"]
	if !ok {
		t.Fatalf("recipes = %v", byName)
	}
	if len(rails.Roles) == 0 || len(rails.Rules) == 0 {
		t.Fatalf("a shipped recipe declares roles and rules: %+v", rails)
	}
	if !strings.HasPrefix(rails.Path, BuiltinRecipeSource) {
		t.Fatalf("a shipped recipe cites where it came from, got %q", rails.Path)
	}
	for _, rule := range rails.Rules {
		if strings.TrimSpace(rule.Because) == "" {
			t.Fatalf("a shipped rule carries its reason like any other: %+v", rule)
		}
	}
	if problems, _ := RecipeProblems(recipes); len(problems) != 0 {
		t.Fatalf("shipped recipes must satisfy the same validator: %v", problems)
	}
}

// A repository that authors a recipe of the same name replaces the shipped
// one, and the replacement is reported rather than silent.
func TestMergeBuiltinRecipes_LocalReplacesShipped(t *testing.T) {
	local := []Recipe{{Name: "rails-conventions", Path: "enola/recipes/rails-conventions.yaml"}}
	merged, notes := MergeBuiltinRecipes(local)
	count := 0
	for _, rec := range merged {
		if rec.Name == "rails-conventions" {
			count++
			if rec.Path != "enola/recipes/rails-conventions.yaml" {
				t.Fatalf("the repository's own recipe must win: %+v", rec)
			}
		}
	}
	if count != 1 {
		t.Fatalf("one recipe of that name survives the merge, got %d", count)
	}
	joined := strings.Join(notes, " ")
	if !strings.Contains(joined, "replaces the recipe") {
		t.Fatalf("the override must be reported: %v", notes)
	}

	untouched, _ := MergeBuiltinRecipes(nil)
	if len(untouched) == 0 {
		t.Fatal("with nothing authored, the shipped set is what loads")
	}
}

// The shipped set is instantiable from a repository that authored no recipe of
// its own, which is the one line this exists for.
func TestBuiltinRecipes_InstantiableWithoutAuthoringOne(t *testing.T) {
	dir := t.TempDir()
	constraints := filepath.Join(dir, "enola", "constraints")
	if err := os.MkdirAll(constraints, 0o755); err != nil {
		t.Fatal(err)
	}
	declaration := `use_recipe:
  - recipe: rails-conventions
    as: app
    bind:
      controllers:
        match: ["app/controllers/**"]
      jobs:
        match: ["app/jobs/**"]
      models:
        match: ["app/models/**"]
      mailers:
        match: ["app/mailers/**"]
      policies:
        match: ["app/policies/**"]
      serializers:
        match: ["app/serializers/**"]
      view-components:
        match: ["app/components/**"]
`
	if err := os.WriteFile(filepath.Join(constraints, "architecture.yaml"), []byte(declaration), 0o644); err != nil {
		t.Fatal(err)
	}
	decl, err := LoadRepoFile(dir)
	if err != nil {
		t.Fatalf("a repository must be able to instantiate a shipped recipe: %v", err)
	}
	if decl == nil || len(decl.Rules) == 0 {
		t.Fatalf("the instantiation must expand into rules: %+v", decl)
	}
	for _, rule := range decl.Rules {
		if rule.Recipe != "rails-conventions" {
			t.Fatalf("every expanded rule names the recipe it came from: %+v", rule)
		}
	}
}

// The shipped set covers the arrangements a team is likely to have decided on,
// not only the framework it happens to use: every one of them declares roles a
// repository binds and rules that carry their reason, and every one satisfies
// the validator every other declaration is held to.
func TestBuiltinRecipes_CoverTheCommonArrangements(t *testing.T) {
	recipes, problems := BuiltinRecipes()
	if len(problems) != 0 {
		t.Fatalf("shipped recipes must parse: %v", problems)
	}
	byName := map[string]Recipe{}
	for _, rec := range recipes {
		byName[rec.Name] = rec
	}
	for _, name := range []string{"rails-conventions", "layered", "ports-and-adapters", "modular-monolith", "event-driven"} {
		rec, ok := byName[name]
		if !ok {
			t.Errorf("%s does not ship", name)
			continue
		}
		if len(rec.Roles) < 2 {
			t.Errorf("%s declares %d role(s); an arrangement is about how parts relate", name, len(rec.Roles))
		}
		if len(rec.Rules) < 3 {
			t.Errorf("%s declares %d rule(s); fewer says less than the name promises", name, len(rec.Rules))
		}
		roles := map[string]bool{}
		for _, role := range rec.Roles {
			roles[role.Name] = true
		}
		for _, rule := range rec.Rules {
			if strings.TrimSpace(rule.Because) == "" {
				t.Errorf("%s: rule %q carries no reason", name, rule.ID)
			}
			for _, named := range []string{rule.Forbid, rule.ForbidReach, rule.Allow, rule.Protect, rule.Private,
				rule.ForbidFact, rule.Cap, rule.Require, rule.RequireEdge, rule.RequireDefines, rule.RequireName, rule.To} {
				if named != "" && !roles[named] {
					t.Errorf("%s: rule %q names %q, which is not one of its roles", name, rule.ID, named)
				}
			}
			for _, group := range [][]string{rule.Only, rule.Owners, rule.Except, rule.Steps} {
				for _, named := range group {
					if !roles[named] {
						t.Errorf("%s: rule %q names %q, which is not one of its roles", name, rule.ID, named)
					}
				}
			}
		}
	}
	if problems, _ := RecipeProblems(recipes); len(problems) != 0 {
		t.Fatalf("shipped recipes must satisfy the validator: %v", problems)
	}
}
