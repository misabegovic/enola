package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/enola-labs/enola/internal/providers/rubydex"
	"github.com/enola-labs/enola/internal/providerspec"
)

// ValidInterpreter reports whether an interpreters: key names one the
// catalogue runs scripts with.
func ValidInterpreter(kind string) bool { return providerspec.ValidInterpreter(kind) }

// Settings is what a config says about providers.
type Settings struct {
	Mode         string
	Listed       bool
	Declared     []Provider
	Interpreters map[string]string
}

// Plan is what a snapshot runs: the providers, and the mode that chose them.
type Plan struct {
	Mode       string
	ModeSource string
	Providers  []Provider
}

// NewPlan resolves the mode and, in every, the catalogue. A listed entry
// replaces the catalogue entry of its name, so a config can pin or relocate one
// provider and still take the rest.
func NewPlan(s Settings) (Plan, error) {
	mode, source, err := ResolveMode(s.Mode, s.Listed)
	if err != nil {
		return Plan{}, err
	}
	plan := Plan{Mode: mode, ModeSource: source}
	switch mode {
	case ModeNone:
		return plan, nil
	case ModeDeclared:
		plan.Providers = append([]Provider(nil), s.Declared...)
		return plan, nil
	}
	listed := map[string]bool{}
	for _, p := range s.Declared {
		listed[p.Name] = true
		plan.Providers = append(plan.Providers, p)
	}
	for _, p := range resolveCatalogue(s.Interpreters).providers {
		if !listed[p.Name] {
			plan.Providers = append(plan.Providers, p)
		}
	}
	sort.Slice(plan.Providers, func(i, j int) bool { return plan.Providers[i].Name < plan.Providers[j].Name })
	return plan, nil
}

// catalogueEntry is one provider the binary can offer without a config: a
// built-in, or a script found in the providers directory, with what makes it
// apply to a repository. The rubydex script is not one: beside the built-in
// it bound nothing the built-in did not, at two to three times the time.
type catalogueEntry struct {
	name        string
	script      string
	interpreter string
	files       string
	extensions  []string
	appliesTo   string
	applies     func(repo string, files []string) (bool, string)
}

var catalogue = []catalogueEntry{
	// prism is the built-in reading the Rubydex index under the Prism reference
	// script's names, with each receiver spelled as Ruby resolves it. The script
	// stays in the providers directory, run by a config that names it with a
	// command: alone it spells a constant receiver's call Const#m and binds a
	// call inside def self.x to the instance method.
	{name: "prism", appliesTo: "a repository with a Gemfile"},
	{name: "rubydex", appliesTo: "a repository with a Gemfile"},
	{name: "rbs", script: "ruby/rbs/enola_rbs_provider.rb", interpreter: "ruby",
		appliesTo: "Ruby, RBS or RBI files", applies: hasFileEnding(".rb", ".rbs", ".rbi")},
	{name: "zeitwerk", script: "ruby/zeitwerk/enola_zeitwerk_provider.rb", interpreter: "ruby",
		appliesTo: "Ruby files", applies: hasFileEnding(".rb")},
	{name: "runtime", script: "ruby/runtime/enola_runtime_provider.rb", interpreter: "ruby",
		appliesTo: "a .enola-runtime/*.json capture", applies: hasCapture},
	// Only where a results file exists: with none the script can only emit
	// nothing or run npx, which may download ESLint, and a generate installs
	// nothing.
	{name: "eslint", script: "js/eslint/enola_eslint_provider.mjs", interpreter: "node",
		appliesTo: "an ESLint results file (ENOLA_ESLINT_RESULTS, else tmp/eslint-results.json)", applies: hasESLintResults},
}

func hasFileEnding(endings ...string) func(string, []string) (bool, string) {
	return func(_ string, files []string) (bool, string) {
		for _, file := range files {
			for _, ending := range endings {
				if strings.HasSuffix(file, ending) {
					return true, ""
				}
			}
		}
		return false, "no " + strings.Join(endings, ", ") + " files"
	}
}

func hasCapture(repo string, _ []string) (bool, string) {
	entries, err := os.ReadDir(filepath.Join(repo, ".enola-runtime"))
	if err == nil {
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
				return true, ""
			}
		}
	}
	return false, "no .enola-runtime/*.json capture"
}

// hasESLintResults reads the results file the way the script does:
// ENOLA_ESLINT_RESULTS when it names a file, else tmp/eslint-results.json.
func hasESLintResults(repo string, _ []string) (bool, string) {
	if env := os.Getenv("ENOLA_ESLINT_RESULTS"); env != "" && isFile(env) {
		return true, ""
	}
	if isFile(filepath.Join(repo, "tmp", "eslint-results.json")) {
		return true, ""
	}
	return false, "no ESLint results file (ENOLA_ESLINT_RESULTS, or tmp/eslint-results.json)"
}

func isFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// interpreter is the program a script runs under, and whether a setting named
// it or PATH supplied it.
type interpreter struct {
	path    string
	named   bool
	source  string
	missing string
}

// resolveInterpreter reads ENOLA_RUBY or ENOLA_NODE, then the config's
// interpreters: block, then PATH.
func resolveInterpreter(kind string, configured map[string]string) interpreter {
	env := "ENOLA_" + strings.ToUpper(kind)
	if v := os.Getenv(env); v != "" {
		return interpreter{path: v, named: true, source: env}
	}
	if v := configured[kind]; v != "" {
		return interpreter{path: v, named: true, source: "interpreters." + kind}
	}
	if path, err := exec.LookPath(kind); err == nil {
		return interpreter{path: path, source: "PATH"}
	}
	return interpreter{missing: fmt.Sprintf("no %s interpreter: set %s, interpreters.%s in the config, or put %s on PATH", kind, env, kind, kind)}
}

// withoutGemPaths is the environment a named Ruby runs in: a version manager
// exports GEM_HOME and GEM_PATH for its own Ruby, and another Ruby following
// them loads gems built for that one, or fails to.
func withoutGemPaths(environ []string) []string {
	out := make([]string, 0, len(environ))
	for _, kv := range environ {
		if strings.HasPrefix(kv, "GEM_HOME=") || strings.HasPrefix(kv, "GEM_PATH=") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// readVersions reads the directory's packed versions. A directory with none
// is unpinned; one whose record does not parse pins nothing it can be sure
// of, so every script in it is refused rather than run unpinned.
func readVersions(dir string) (map[string]string, error) {
	data, err := os.ReadFile(filepath.Join(dir, VersionsFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var versions map[string]string
	if err := json.Unmarshal(data, &versions); err != nil {
		return nil, fmt.Errorf("%s does not parse: %v", filepath.Join(dir, VersionsFile), err)
	}
	return versions, nil
}

type resolved struct {
	dir, dirSource string
	interpreters   map[string]interpreter
	providers      []Provider
	entries        []catalogueEntry
}

func resolveCatalogue(configured map[string]string) resolved {
	r := resolved{interpreters: map[string]interpreter{}}
	r.dir, r.dirSource = Directory()
	var versions map[string]string
	var versionsErr error
	if r.dir != "" {
		versions, versionsErr = readVersions(r.dir)
	}
	for _, kind := range []string{"ruby", "node"} {
		r.interpreters[kind] = resolveInterpreter(kind, configured)
	}
	for _, c := range catalogue {
		r.entries = append(r.entries, c)
		p := Provider{Name: c.name}
		if c.script == "" {
			r.providers = append(r.providers, p)
			continue
		}
		in := r.interpreters[c.interpreter]
		script := filepath.Join(r.dir, filepath.FromSlash(c.script))
		program := in.path
		if program == "" {
			program = c.interpreter
		}
		p.Command = []string{program, script}
		p.Files, p.Extensions = c.files, c.extensions
		p.ExpectedVersion = versions[c.script]
		p.interpreter = in.path
		p.applies = c.applies
		if in.named && c.interpreter == "ruby" {
			p.env = withoutGemPaths(os.Environ())
		}
		switch {
		case r.dir == "":
			p.unavailable = "no providers directory: set ENOLA_PROVIDERS_DIR to one laid out as examples/providers"
		case !isFile(script):
			p.unavailable = fmt.Sprintf("no such script %s (the providers directory is from %s)", script, r.dirSource)
		case versionsErr != nil:
			p.unavailable = versionsErr.Error()
		case in.missing != "":
			p.unavailable = in.missing
		}
		r.providers = append(r.providers, p)
	}
	return r
}

// EntryStatus is one catalogue entry as `providers list` reports it.
type EntryStatus struct {
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	Script      string `json:"script,omitempty"`
	Interpreter string `json:"interpreter,omitempty"`
	// InterpreterFrom says which setting named the interpreter, or PATH.
	InterpreterFrom string `json:"interpreter_from,omitempty"`
	Version         string `json:"version,omitempty"`
	Expected        string `json:"expected_version,omitempty"`
	AppliesTo       string `json:"applies_to"`
	CanRun          bool   `json:"can_run"`
	Reason          string `json:"reason,omitempty"`
}

// Status is the catalogue on this machine: where its scripts come from, and
// whether each entry can run here and why not.
type Status struct {
	Directory       string        `json:"directory,omitempty"`
	DirectorySource string        `json:"directory_source,omitempty"`
	Entries         []EntryStatus `json:"providers"`
}

// CatalogueStatus asks each entry what a snapshot would: a built-in whether its
// library is installed, a script its version, under the interpreter a snapshot
// would use. Whether an entry applies is a question about a repository and is
// not asked here.
func CatalogueStatus(ctx context.Context, interpreters map[string]string) Status {
	r := resolveCatalogue(interpreters)
	status := Status{Directory: r.dir, DirectorySource: r.dirSource}
	for i, c := range r.entries {
		p := r.providers[i]
		entry := EntryStatus{Name: c.name, AppliesTo: c.appliesTo, Expected: p.ExpectedVersion}
		if c.script == "" {
			entry.Kind = "built-in"
			entry.Version = rubydex.Version
			if path, installed := rubydex.Installed(); installed {
				entry.CanRun = true
			} else {
				entry.Reason = fmt.Sprintf("the Rubydex library is not installed at %s; run `%s`", path, rubydex.FetchHint)
			}
			status.Entries = append(status.Entries, entry)
			continue
		}
		entry.Kind = "script"
		entry.Script = p.Command[1]
		entry.Interpreter = p.interpreter
		entry.InterpreterFrom = r.interpreters[c.interpreter].source
		if p.unavailable != "" {
			entry.Reason = p.unavailable
			status.Entries = append(status.Entries, entry)
			continue
		}
		version, why := probe(ctx, p)
		entry.Version = version
		switch {
		case why != "":
			entry.Reason = why
		case p.ExpectedVersion != "" && version != p.ExpectedVersion:
			entry.Reason = fmt.Sprintf("version mismatch: reported %s, expected %s", version, p.ExpectedVersion)
		default:
			entry.CanRun = true
		}
		status.Entries = append(status.Entries, entry)
	}
	return status
}
