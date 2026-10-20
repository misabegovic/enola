package providers

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/enola-labs/enola/internal/facts"
)

// writeInapplicableProvider answers --version and then says, on the line the
// seam reads, that nothing it reads is in this repository; body is printed on
// stdout after it, so a test can make the provider contradict itself.
func writeInapplicableProvider(t *testing.T, reason, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "inapplicable-provider")
	script := "#!/bin/sh\n" +
		"for a in \"$@\"; do if [ \"$a\" = \"--version\" ]; then echo 0.3.0; exit 0; fi; done\n" +
		"echo '" + NotApplicablePrefix + reason + "' >&2\n" +
		"cat <<'EOF'\n" + body + "EOF\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRun_AProviderThatFindsNoInputIsNotApplicable(t *testing.T) {
	script := writeInapplicableProvider(t, "no results file", "")
	ff, records := Run(context.Background(), []Provider{{Name: "lint", Command: []string{script}}}, t.TempDir(), nil, nil)
	if len(ff) != 0 {
		t.Fatalf("a provider that does not apply contributes nothing, got %+v", ff)
	}
	r := records[0]
	if !r.NotApplicable() || !r.Skipped || r.Reason != "no results file" || r.Version != "0.3.0" || r.FactCount != 0 {
		t.Fatalf("record = %+v, want not applicable with the provider's own reason and its version", r)
	}
	if got := facts.RanProviders(records); len(got) != 0 {
		t.Fatalf("a provider that did not apply did not run, but RanProviders = %v", got)
	}
}

func TestRun_NotApplicableBesideFactsIsInvalidOutput(t *testing.T) {
	script := writeInapplicableProvider(t, "no results file", validCallFact+"\n")
	ff, records := Run(context.Background(), []Provider{{Name: "lint", Command: []string{script}}}, t.TempDir(), nil, nil)
	if len(ff) != 0 {
		t.Fatalf("a provider contradicting itself must contribute nothing, got %+v", ff)
	}
	r := records[0]
	if r.NotApplicable() || !r.Skipped || !strings.Contains(r.Reason, "does not apply") || !strings.Contains(r.Reason, "1 fact") {
		t.Fatalf("record = %+v, want a skip naming the contradiction", r)
	}
}

func TestRun_RubydexWithoutAGemfileIsNotApplicable(t *testing.T) {
	_, records := Run(context.Background(), []Provider{{Name: "rubydex"}}, t.TempDir(), nil, nil)
	r := records[0]
	if !r.NotApplicable() || !strings.Contains(r.Reason, "Gemfile") {
		t.Fatalf("record = %+v, want not applicable for want of a Gemfile, whether or not the library is installed", r)
	}
}

func TestSummary_NamesEveryProviderWithWhatHappenedToIt(t *testing.T) {
	no := false
	records := []facts.ProviderRecord{
		{Name: "eslint", Skipped: true, Applicable: &no, Reason: "no results file"},
		{Name: "prism", Version: "0.2.0", Skipped: true, Reason: "version mismatch: reported 0.2.0, expected 0.1.0"},
		{Name: "rubydex", Version: "0.4.0", FactCount: 14},
	}
	got := SummaryLines(records)
	want := []string{
		"  eslint   not applicable: no results file",
		"  prism    skipped: version mismatch: reported 0.2.0, expected 0.1.0",
		"  rubydex  ran 0.4.0, 14 facts",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("summary =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if lines := SummaryLines(nil); len(lines) != 0 {
		t.Fatalf("with no provider the summary is empty, got %q", lines)
	}
}
