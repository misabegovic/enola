package providers

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/enola-labs/enola/internal/facts"
)

func examplesDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("..", "..", "examples", "providers"))
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// isolate clears every variable the catalogue reads, so a test sees only
// what it sets.
func isolate(t *testing.T) {
	t.Helper()
	for _, name := range []string{"ENOLA_PROVIDERS", "ENOLA_PROVIDERS_DIR", "ENOLA_RUBY", "ENOLA_NODE", "ENOLA_ESLINT_RESULTS", "ENOLA_ESLINT_RUN"} {
		t.Setenv(name, "")
	}
	prevMode, prevShare := DefaultMode, ShareDir
	DefaultMode, ShareDir = "", ""
	t.Cleanup(func() { DefaultMode, ShareDir = prevMode, prevShare })
}

func TestResolveMode_FirstMatchWins(t *testing.T) {
	isolate(t)
	cases := []struct {
		name, key, env, stamped string
		listed                  bool
		want                    string
	}{
		{name: "nothing said", want: ModeDeclared},
		{name: "the build's default", stamped: ModeEvery, want: ModeEvery},
		{name: "the variable over the build", env: ModeNone, stamped: ModeEvery, want: ModeNone},
		{name: "a list over the variable", listed: true, env: ModeEvery, stamped: ModeEvery, want: ModeDeclared},
		{name: "the key over the list", key: ModeEvery, listed: true, env: ModeNone, want: ModeEvery},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("ENOLA_PROVIDERS", c.env)
			DefaultMode = c.stamped
			got, _, err := ResolveMode(c.key, c.listed)
			if err != nil || got != c.want {
				t.Fatalf("ResolveMode = %q, %v; want %q", got, err, c.want)
			}
		})
	}
	t.Setenv("ENOLA_PROVIDERS", "all")
	if _, _, err := ResolveMode("", false); err == nil || !strings.Contains(err.Error(), "ENOLA_PROVIDERS") {
		t.Fatalf("an unknown mode in the variable must be a named error, got %v", err)
	}
	if _, _, err := ResolveMode("some", false); err == nil || !strings.Contains(err.Error(), "providers_mode") {
		t.Fatalf("an unknown mode in the config must be a named error, got %v", err)
	}
}

func TestNewPlan_EveryIsTheCatalogueAndAListedEntryReplacesItsNamesake(t *testing.T) {
	isolate(t)
	t.Setenv("ENOLA_PROVIDERS_DIR", examplesDir(t))
	t.Setenv("ENOLA_RUBY", "/opt/ruby/bin/ruby")
	t.Setenv("ENOLA_NODE", "/opt/node/bin/node")
	listed := Provider{Name: "prism", Command: []string{"/usr/bin/ruby", "/elsewhere/prism.rb"}, ExpectedVersion: "0.1.0"}
	plan, err := NewPlan(Settings{Mode: ModeEvery, Declared: []Provider{listed, {Name: "custom", Command: []string{"/bin/custom"}}}})
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]Provider{}
	var names []string
	for _, p := range plan.Providers {
		byName[p.Name] = p
		names = append(names, p.Name)
	}
	sort.Strings(names)
	if strings.Join(names, " ") != "custom eslint prism rbs rubydex runtime zeitwerk" {
		t.Fatalf("plan names %v, want the six catalogue entries and the listed one", names)
	}
	if got := byName["prism"]; got.Command[1] != "/elsewhere/prism.rb" || got.ExpectedVersion != "0.1.0" {
		t.Fatalf("a listed prism must replace the catalogue's, got %+v", got)
	}
	rbs := byName["rbs"]
	if rbs.Command[0] != "/opt/ruby/bin/ruby" || rbs.Command[1] != filepath.Join(examplesDir(t), "ruby", "rbs", "enola_rbs_provider.rb") {
		t.Fatalf("rbs runs the named Ruby over the directory's script, got %v", rbs.Command)
	}
	if byName["eslint"].Command[0] != "/opt/node/bin/node" {
		t.Fatalf("eslint runs the named Node, got %v", byName["eslint"].Command)
	}
	if len(byName["rubydex"].Command) != 0 {
		t.Fatalf("rubydex is the built-in, got %+v", byName["rubydex"])
	}
	for _, name := range []string{"eslint", "rbs", "rubydex", "runtime", "zeitwerk"} {
		if byName[name].applies == nil && name != "rubydex" {
			t.Errorf("catalogue entry %s carries no applicability", name)
		}
	}

	declared, err := NewPlan(Settings{Mode: ModeDeclared, Declared: []Provider{listed}})
	if err != nil || len(declared.Providers) != 1 || declared.Providers[0].Name != "prism" {
		t.Fatalf("declared is the list exactly, got %+v, %v", declared.Providers, err)
	}
	none, err := NewPlan(Settings{Mode: ModeNone, Declared: []Provider{listed}})
	if err != nil || len(none.Providers) != 0 {
		t.Fatalf("none runs nothing, got %+v, %v", none.Providers, err)
	}
}

// A Ruby tree with no Gemfile: rubydex has no workspace to index and prism
// alone would bind a constant receiver's call to an instance method, so both
// read not applicable with the reason; runtime has no capture and eslint no
// results file. Nothing here needs an interpreter to find that out.
func TestEvery_ReadsNotApplicableWhereNothingIsThere(t *testing.T) {
	isolate(t)
	t.Setenv("ENOLA_PROVIDERS_DIR", examplesDir(t))
	t.Setenv("ENOLA_RUBY", "/nonexistent/ruby")
	t.Setenv("ENOLA_NODE", "/nonexistent/node")
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "a.rb"), []byte("class A; end\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	plan, err := NewPlan(Settings{Mode: ModeEvery})
	if err != nil {
		t.Fatal(err)
	}
	_, records := RunWith(context.Background(), Input{Providers: plan.Providers, RepoPath: repo, Files: []string{"a.rb"}})
	got := map[string]facts.ProviderRecord{}
	for _, r := range records {
		got[r.Name] = r
	}
	for name, reason := range map[string]string{"rubydex": "Gemfile", "prism": "Gemfile", "runtime": ".enola-runtime", "eslint": "results"} {
		if r := got[name]; !r.NotApplicable() || !strings.Contains(r.Reason, reason) {
			t.Errorf("%s = %+v, want not applicable naming %q", name, r, reason)
		}
	}
	for _, name := range []string{"rbs", "zeitwerk"} {
		if r := got[name]; r.NotApplicable() || !r.Skipped || !strings.Contains(r.Reason, "/nonexistent/ruby") {
			t.Errorf("%s = %+v, want a skip naming the interpreter that is not there", name, r)
		}
	}
}

// A named interpreter that cannot load the script is a skip carrying its path
// and the first thing it said, not "exit status 1".
func TestEvery_AFailingInterpreterIsNamedWithItsFirstErrorLine(t *testing.T) {
	isolate(t)
	t.Setenv("ENOLA_PROVIDERS_DIR", examplesDir(t))
	ruby := filepath.Join(t.TempDir(), "ruby")
	if err := os.WriteFile(ruby, []byte("#!/bin/sh\necho 'cannot load such file -- prism (LoadError)' >&2\necho 'more' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ENOLA_RUBY", ruby)
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "a.rb"), []byte("class A; end\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	plan, err := NewPlan(Settings{Mode: ModeEvery})
	if err != nil {
		t.Fatal(err)
	}
	_, records := RunWith(context.Background(), Input{Providers: plan.Providers, RepoPath: repo, Files: []string{"a.rb"}})
	for _, r := range records {
		if r.Name != "zeitwerk" {
			continue
		}
		if !r.Skipped || !strings.Contains(r.Reason, ruby) || !strings.Contains(r.Reason, "cannot load such file -- prism (LoadError)") || strings.Contains(r.Reason, "more") {
			t.Fatalf("zeitwerk = %+v, want a skip naming %s and its first error line", r, ruby)
		}
		return
	}
	t.Fatal("zeitwerk has no record")
}

// A Ruby named by setting runs without the GEM_HOME and GEM_PATH a version
// manager exported for another Ruby, which would point it at gems built for
// that one; a Ruby found on PATH keeps the environment it came with.
func TestEvery_ANamedRubyRunsWithoutAnotherRubysGemPaths(t *testing.T) {
	isolate(t)
	dir := t.TempDir()
	script := filepath.Join(dir, "ruby", "zeitwerk", "enola_zeitwerk_provider.rb")
	if err := os.MkdirAll(filepath.Dir(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(script, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	ruby := filepath.Join(t.TempDir(), "ruby")
	body := "#!/bin/sh\n" +
		"for a in \"$@\"; do if [ \"$a\" = \"--version\" ]; then echo 0.1.0; exit 0; fi; done\n" +
		"printf '{\"kind\":\"symbol\",\"name\":\"gem-home: [%s] [%s]\",\"props\":{\"resolution_level\":\"name-only\"}}\\n' \"$GEM_HOME\" \"$GEM_PATH\"\n"
	if err := os.WriteFile(ruby, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ENOLA_PROVIDERS_DIR", dir)
	t.Setenv("ENOLA_RUBY", ruby)
	t.Setenv("GEM_HOME", "/usr/share/rvm/gems/ruby-3.4.0")
	t.Setenv("GEM_PATH", "/usr/share/rvm/gems/ruby-3.4.0")
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "a.rb"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	plan, err := NewPlan(Settings{Mode: ModeEvery})
	if err != nil {
		t.Fatal(err)
	}
	ff, _ := RunWith(context.Background(), Input{Providers: plan.Providers, RepoPath: repo, Files: []string{"a.rb"}})
	for _, f := range ff {
		if strings.HasPrefix(f.Name, "gem-home: ") {
			if f.Name != "gem-home: [] []" {
				t.Fatalf("the named Ruby saw %q", f.Name)
			}
			return
		}
	}
	t.Fatalf("zeitwerk emitted nothing: %+v", ff)
}

// The versions a packed directory records are the pins: a script beside the
// binary that reports another version is not the one it shipped with.
func TestEvery_ThePackedVersionsArePins(t *testing.T) {
	isolate(t)
	dir := t.TempDir()
	script := filepath.Join(dir, "ruby", "zeitwerk", "enola_zeitwerk_provider.rb")
	if err := os.MkdirAll(filepath.Dir(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(script, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, VersionsFile), []byte(`{"ruby/zeitwerk/enola_zeitwerk_provider.rb": "0.1.0"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	ruby := filepath.Join(t.TempDir(), "ruby")
	if err := os.WriteFile(ruby, []byte("#!/bin/sh\necho 0.2.0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ENOLA_PROVIDERS_DIR", dir)
	t.Setenv("ENOLA_RUBY", ruby)
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "a.rb"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	plan, err := NewPlan(Settings{Mode: ModeEvery})
	if err != nil {
		t.Fatal(err)
	}
	_, records := RunWith(context.Background(), Input{Providers: plan.Providers, RepoPath: repo, Files: []string{"a.rb"}})
	for _, r := range records {
		switch r.Name {
		case "zeitwerk":
			if !r.Skipped || r.Reason != "version mismatch: reported 0.2.0, expected 0.1.0" {
				t.Fatalf("zeitwerk = %+v, want the packed pin to refuse 0.2.0", r)
			}
		case "rbs":
			if !r.Skipped || !strings.Contains(r.Reason, "no such script") {
				t.Fatalf("rbs = %+v, want its missing script named", r)
			}
		}
	}
}

func TestEvery_NoDirectoryIsANamedSkipNotSilence(t *testing.T) {
	isolate(t)
	t.Setenv("ENOLA_RUBY", "/opt/ruby")
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "a.rb"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	plan, err := NewPlan(Settings{Mode: ModeEvery})
	if err != nil {
		t.Fatal(err)
	}
	_, records := RunWith(context.Background(), Input{Providers: plan.Providers, RepoPath: repo, Files: []string{"a.rb"}})
	for _, r := range records {
		if r.Name == "rbs" && (!r.Skipped || !strings.Contains(r.Reason, "ENOLA_PROVIDERS_DIR")) {
			t.Fatalf("rbs = %+v, want a skip saying where the scripts would be found", r)
		}
	}
}

func TestCatalogueStatus_NamesSixEntriesWithWhetherEachCanRun(t *testing.T) {
	isolate(t)
	t.Setenv("ENOLA_PROVIDERS_DIR", examplesDir(t))
	t.Setenv("ENOLA_RUBY", "/nonexistent/ruby")
	t.Setenv("ENOLA_NODE", "/nonexistent/node")
	status := CatalogueStatus(context.Background(), nil)
	if len(status.Entries) != 6 {
		t.Fatalf("the catalogue names %d entries, want 6: %+v", len(status.Entries), status.Entries)
	}
	for _, e := range status.Entries {
		if e.Kind == "built-in" {
			continue
		}
		if e.CanRun || !strings.Contains(e.Reason, "/nonexistent/") {
			t.Errorf("%s = %+v, want cannot run, naming the missing interpreter", e.Name, e)
		}
		if e.AppliesTo == "" {
			t.Errorf("%s says nothing about what it applies to", e.Name)
		}
	}
}
