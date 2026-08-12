package constraints

import (
	"context"
	"strings"
	"testing"

	"github.com/enola-labs/enola/internal/facts"
)

// The shape finding 0010 was written about: lib/express.js requires
// ./application, and the extractor measures that as an imports edge onto the
// path lib/application — which names no member fact, because the file itself is
// measured as lib/application.js and file_ref is not a member kind. The rule
// yielded nothing on a dependency any reader of the file can see, and read
// exactly like the reverse rule, which is silent because the dependency is
// genuinely absent.
func TestExplain_FileGranularImportTargetGroundsInItsComponent(t *testing.T) {
	store := facts.NewStore()
	store.Add(
		componentIntent("entry", "lib/express.js"),
		componentIntent("support", "lib/application.js"),
		ruleIntent("entry-avoids-support", "entry", "support", "imports", "the entry file must not reach the support files"),
		facts.Fact{Kind: facts.KindDependency, Name: "lib -> lib/application", File: "lib/express.js",
			Relations: []facts.Relation{{Kind: facts.RelImports, Target: "lib/application"}}},
		facts.Fact{Kind: facts.KindFileRef, Name: "lib/application.js", File: "lib/application.js"},
		facts.Fact{Kind: facts.KindFileRef, Name: "lib/express.js", File: "lib/express.js"},
	)
	insights, err := New().Explain(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	var verdict *facts.Insight
	for i := range insights {
		if strings.HasPrefix(insights[i].Title, "Constraint entry-avoids-support violated") {
			verdict = &insights[i]
		}
	}
	if verdict == nil {
		t.Fatalf("the import is measured and the target names a measured file, so the rule must verdict: %+v", insights)
	}
	if verdict.Confidence != 1.0 {
		t.Errorf("confidence = %v, want 1.0: the membership is the component's own match glob", verdict.Confidence)
	}
}

// The reverse direction stays silent for the reason it always did — there is no
// such import — so the fallback cannot be answering "yes" to everything.
func TestExplain_FileGranularFallbackDoesNotInventTheReverseEdge(t *testing.T) {
	store := facts.NewStore()
	store.Add(
		componentIntent("entry", "lib/express.js"),
		componentIntent("support", "lib/application.js"),
		ruleIntent("support-avoids-entry", "support", "entry", "imports", "the support files must not reach back"),
		facts.Fact{Kind: facts.KindDependency, Name: "lib -> lib/application", File: "lib/express.js",
			Relations: []facts.Relation{{Kind: facts.RelImports, Target: "lib/application"}}},
		facts.Fact{Kind: facts.KindFileRef, Name: "lib/application.js", File: "lib/application.js"},
		facts.Fact{Kind: facts.KindFileRef, Name: "lib/express.js", File: "lib/express.js"},
	)
	insights, err := New().Explain(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	for _, insight := range insights {
		if strings.Contains(insight.Title, "support-avoids-entry violated") {
			t.Fatalf("no such import is measured, yet the rule verdicted: %+v", insight)
		}
	}
}

// A target that names neither a fact nor a measured file — a package outside the
// tree — is skipped, and the skip is counted. Silence about it would read as
// compliance, which is the failure the advisory exists to prevent.
func TestExplain_UngroundableImportTargetsAreCounted(t *testing.T) {
	store := facts.NewStore()
	store.Add(
		componentIntent("entry", "lib/express.js"),
		componentIntent("support", "lib/application.js"),
		ruleIntent("entry-avoids-support", "entry", "support", "imports", "the entry file must not reach the support files"),
		facts.Fact{Kind: facts.KindDependency, Name: "lib -> serve-static", File: "lib/express.js",
			Relations: []facts.Relation{{Kind: facts.RelImports, Target: "serve-static"}}},
		facts.Fact{Kind: facts.KindFileRef, Name: "lib/application.js", File: "lib/application.js"},
		facts.Fact{Kind: facts.KindFileRef, Name: "lib/express.js", File: "lib/express.js"},
	)
	insights, err := New().Explain(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	var advisory *facts.Insight
	for i := range insights {
		if strings.Contains(insights[i].Title, "reached no verdict on 1 import target") {
			advisory = &insights[i]
		}
		if strings.Contains(insights[i].Title, "entry-avoids-support violated") {
			t.Fatalf("an unresolvable target must never be guessed into a breach: %+v", insights[i])
		}
	}
	if advisory == nil {
		t.Fatalf("the skipped target must be counted, not dropped: %+v", insights)
	}
	if !strings.Contains(advisory.Description, "serve-static") {
		t.Errorf("the advisory must name the sample it counted, got: %q", advisory.Description)
	}
	if advisory.Confidence >= 1.0 {
		t.Errorf("confidence = %v, want a skip advisory below the gate floor", advisory.Confidence)
	}
}

// Directory imports resolve to their index file, and the resolution is scoped to
// the importing repository: a file measured in one repo can never ground a
// target written in another.
func TestGrounding_ResolvesIndexAndStaysInsideTheRepo(t *testing.T) {
	store := facts.NewStore()
	store.Add(
		facts.Fact{Kind: facts.KindFileRef, Repo: "a", Name: "src/widgets/index.ts", File: "a/src/widgets/index.ts"},
		facts.Fact{Kind: facts.KindFileRef, Repo: "b", Name: "src/other.ts", File: "b/src/other.ts"},
	)
	g := newGrounding(store)
	if got, ok := g.resolve("src/widgets", "a"); !ok || got != "a/src/widgets/index.ts" {
		t.Errorf("resolve(src/widgets, a) = %q/%v, want the index file", got, ok)
	}
	if _, ok := g.resolve("src/other", "a"); ok {
		t.Error("a file measured in repo b must not ground a target written in repo a")
	}
	if _, ok := g.resolve("src/missing", "a"); ok {
		t.Error("a target naming no measured file must not resolve")
	}
}
