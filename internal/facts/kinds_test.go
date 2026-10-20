package facts

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// documentedKinds reads the first column of the table under heading in
// docs/schema/facts.md.
func documentedKinds(t *testing.T, heading string) []string {
	t.Helper()
	data, err := os.ReadFile("../../docs/schema/facts.md")
	if err != nil {
		t.Fatal(err)
	}
	_, section, found := strings.Cut(string(data), "\n"+heading+"\n")
	if !found {
		t.Fatalf("facts.md has no %q section", heading)
	}
	row := regexp.MustCompile("^\\| `([a-z_]+)` \\|")
	var kinds []string
	started := false
	for _, line := range strings.Split(section, "\n") {
		if m := row.FindStringSubmatch(line); m != nil {
			kinds = append(kinds, m[1])
			started = true
			continue
		}
		if started && !strings.HasPrefix(line, "|") {
			break
		}
	}
	return kinds
}

// The kind lists are the documented tables, entry for entry.
func TestKindListsAreTheDocumentedTables(t *testing.T) {
	for _, c := range []struct {
		heading string
		list    []string
	}{
		{"## Fact kinds", FactKinds},
		{"## Relation kinds", RelationKinds},
	} {
		documented := documentedKinds(t, c.heading)
		if strings.Join(documented, ",") != strings.Join(c.list, ",") {
			t.Errorf("%s: documented %v, listed %v", c.heading, documented, c.list)
		}
	}
}
