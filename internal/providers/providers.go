// Package providers runs configured external fact providers at snapshot time —
// the seam through which a tool enola does not ship can contribute measured
// facts to the graph without becoming an extractor.
//
// A provider is an executable named in the engine config's `providers:` block,
// or, in the every providers mode, one the catalogue offers (catalogue.go): the
// built-in Rubydex provider and the example scripts in the providers directory,
// each where it applies. The seam runs it twice: once with --version to learn what build it is
// talking to, once with the repository path to collect facts, printed as JSONL
// on stdout in the store's own fact schema. Everything about the exchange is
// fail-closed: output is validated strictly (an unknown kind, an unknown
// relation, an unknown field, a missing resolution_level — any invalid line
// rejects the provider's WHOLE output, never just the line), and a provider
// fact may not share a name+kind identity with an extractor fact — colliding
// facts are skipped with a logged count, never overwritten, because the
// extractor's account of a fact is the one the rest of the graph was built
// against. A configured provider whose command is missing degrades to a named
// skip in the census rather than an error: the machine not having a tool
// installed must not fail the snapshot, it must be visible in the receipt.
//
// Determinism: providers run concurrently and are merged in name order, each
// provider's accepted facts are sorted before merging, and every accepted fact
// is stamped with the provider's name and reported version — so two runs over
// the same tree with the same providers produce byte-identical fact sets, and a
// fact can always answer who put it in the graph.
package providers

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/enola-labs/enola/internal/facts"
	"github.com/enola-labs/enola/internal/providerspec"
)

// Provider is one fact source as the seam runs it: the census name its facts
// are stamped with, the command (argv) the seam runs, and the version the
// operator expects the installed build to report. An entry with no command
// names a provider the binary carries itself (see builtin.go), run
// in-process under the same validation and census. A reported version that
// disagrees with the expected one is a skip, not a merge — facts from a build
// the config did not pin would drift the graph for reasons no code change made.
//
// The declaration a config writes, and its validation, live in
// internal/providerspec, which the config loads without reaching this package
// and the engines behind it; FromSpecs turns those into Providers. The exported
// fields below are that declaration's, and the unexported ones are what only
// the catalogue sets, so Provider is a struct of its own rather than an alias.
type Provider struct {
	Name            string   `yaml:"name"`
	Command         []string `yaml:"command"`
	ExpectedVersion string   `yaml:"expected_version"`
	// Files declares how the provider's output partitions. Empty means the
	// provider reads the whole tree on every run. FilesPerFile means every
	// fact it emits names the file it came from and nothing else influenced
	// it, so the seam may hand it only the files whose content has no cache
	// entry and reuse the rest; a provider that reads across files must not
	// declare it, because the cache would then serve facts computed against
	// a tree that has since changed.
	Files string `yaml:"files,omitempty"`
	// Extensions names the file suffixes a per-file provider reads, so the
	// seam keys and lists only those; required with FilesPerFile.
	Extensions []string `yaml:"extensions,omitempty"`

	// The fields below are set only on an entry the catalogue built, never
	// from a config: an explicit list means exactly what it says.
	//
	// env is the provider process's environment, nil for the seam's own.
	env []string
	// interpreter is the interpreter the catalogue chose for the script, named
	// in a skip when its version probe fails.
	interpreter string
	// applies decides whether anything the provider reads is in a repository,
	// before anything is run.
	applies func(repo string, files []string) (bool, string)
	// unavailable is why the entry cannot run on this machine (no directory,
	// no script, no interpreter), asked only where it applies.
	unavailable string
}

func FromSpecs(specs []providerspec.Spec) []Provider {
	if specs == nil {
		return nil
	}
	out := make([]Provider, len(specs))
	for i, s := range specs {
		out[i] = Provider{
			Name:            s.Name,
			Command:         s.Command,
			ExpectedVersion: s.ExpectedVersion,
			Files:           s.Files,
			Extensions:      s.Extensions,
		}
	}
	return out
}

// FilesPerFile is the one value Provider.Files accepts.
const FilesPerFile = providerspec.FilesPerFile

// FilesFlag is the argument a per-file provider receives after the repository
// path: the path of a file listing, one repo-relative path per line, the files
// it is to read in this run.
const FilesFlag = "--files"

// Cache is what the engine lends the seam so provider facts can be reused
// between snapshots. Keys are the seam's; the engine scopes them to its cache
// version and build. Get hands a stored entry out once and carries it forward
// to the next run; Peek reads without carrying forward, for an entry the caller
// is about to replace; Put stores an entry for this run.
type Cache interface {
	Get(key string) ([]facts.Fact, bool)
	Peek(key string) ([]facts.Fact, bool)
	Put(key string, ff []facts.Fact)
}

// Input is everything one Run needs: the providers, the repository, the
// extractor's identity set and ignore globs, and, when the engine caches, the
// walked files with their content digests and the cache to reuse through.
type Input struct {
	Providers []Provider
	RepoPath  string
	Taken     func(kind, name string) bool
	Ignored   func(file string) bool
	Files     []string
	Hashes    map[string]string
	Cache     Cache

	rubydex *rubydexEngine
}

// engine is the run's one Rubydex index, made here for a caller that runs a
// built-in outside RunWith.
func (in Input) engine() *rubydexEngine {
	if in.rubydex == nil {
		return &rubydexEngine{}
	}
	return in.rubydex
}

// Reserved prop keys on provider facts. The provenance pair is stamped by the
// seam — a provider claiming them itself is invalid output — and the
// resolution level is the provider's own honesty declaration: how it resolved
// what it emitted, required on every fact so a consumer can weigh a
// constant-receiver call edge differently from a name-only one.
const (
	PropProvider          = "provider"
	PropProviderVersion   = "provider_version"
	PropResolutionLevel   = "resolution_level"
	PropObservedVia       = "observed_via"
	PropRuntimeObserved   = "runtime_observed"
	PropDeclaredIn        = "declared_in"
	PropTyped             = "typed"
	PropDeclaredSignature = "declared_signature"
)

const (
	LevelConstantReceiver = "constant-receiver"
	LevelLexicalSelf      = "lexical-self"
	LevelNameOnly         = "name-only"
	LevelLiteralDeclared  = "literal-declared"
	LevelMarkupDeclared   = "markup-declared"
	// LevelToolReported is a lint provider's honesty declaration: the fact is
	// what another tool reported, file and line and rule, and the provider
	// resolved nothing about it.
	LevelToolReported      = "tool-reported"
	LevelConventionDerived = "convention-derived"
	LevelRuntimeObserved   = "runtime-observed"
	LevelDeclared          = "declared"
	// LevelResolved states that the provider resolved the name through the
	// language's own lookup rules (nesting, inheritance, the locked gems) to
	// one declaration. It is neither a receiver typing nor a signature-file
	// claim nor a path convention, which is why it is its own word.
	LevelResolved = "resolved"
)

const CensusPrefix = "enola-provider-census: "

// NotApplicablePrefix starts the stderr line a provider writes when nothing it
// reads is in the repository, followed by what was missing. It then emits no
// facts and exits 0, and the seam records it as not applicable rather than as
// a run that found nothing: a lint reader with no results file and one whose
// results are clean are different answers.
const NotApplicablePrefix = "enola-provider-not-applicable: "

var allowedResolutionLevels = map[string]bool{
	LevelConstantReceiver:  true,
	LevelLexicalSelf:       true,
	LevelNameOnly:          true,
	LevelLiteralDeclared:   true,
	LevelMarkupDeclared:    true,
	LevelConventionDerived: true,
	LevelRuntimeObserved:   true,
	LevelDeclared:          true,
	LevelToolReported:      true,
	LevelResolved:          true,
}

// allowedFactKinds is the closed set a provider may emit: the measured kinds.
// The engine-owned kinds (intent, service, extraction) are deliberately
// absent — declarations and cross-repo synthesis have exactly one producer
// each, and a provider minting them would forge provenance.
var allowedFactKinds = map[string]bool{
	facts.KindModule:      true,
	facts.KindSymbol:      true,
	facts.KindRoute:       true,
	facts.KindStorage:     true,
	facts.KindDependency:  true,
	facts.KindAssociation: true,
	facts.KindTestRef:     true,
	facts.KindFileRef:     true,
	facts.KindLint:        true,
}

// allowedRelationKinds is the closed relation vocabulary, mirroring the
// constants in internal/facts — an edge kind nothing traverses is an edge
// nothing can act on, so it is rejected at the seam instead of aging silently.
var allowedRelationKinds = map[string]bool{
	facts.RelDeclares:     true,
	facts.RelImports:      true,
	facts.RelCalls:        true,
	facts.RelImplements:   true,
	facts.RelDependsOn:    true,
	facts.RelInstantiates: true,
	facts.RelInjects:      true,
	facts.RelHasMethod:    true,
	facts.RelHandledBy:    true,
}

// RunWith is Run with the engine's cache in hand. Without a cache every
// provider runs whole-tree exactly as Run does.
func RunWith(ctx context.Context, in Input) ([]facts.Fact, []facts.ProviderRecord) {
	taken, ignored := in.Taken, in.Ignored
	if in.rubydex == nil {
		in.rubydex = &rubydexEngine{}
	}
	sorted := append([]Provider(nil), in.Providers...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })

	// Each provider is an independent process, so they run concurrently; what
	// keeps the output deterministic is everything after: results are taken in
	// name order, each provider's facts are already sorted by runOne, and the
	// merge appends in that order. Wall-clock drops from the sum of the
	// providers to the slowest one, which on a monolith with prism beside a
	// lint reader is most of the difference.
	type outcome struct {
		accepted []facts.Fact
		record   facts.ProviderRecord
	}
	outcomes := make([]outcome, len(sorted))
	var wg sync.WaitGroup
	for i, p := range sorted {
		wg.Add(1)
		go func(i int, p Provider) {
			defer wg.Done()
			accepted, record := runOne(ctx, p, in)
			outcomes[i] = outcome{accepted: accepted, record: record}
		}(i, p)
	}
	wg.Wait()

	records := make([]facts.ProviderRecord, len(sorted))
	keptAll := make([][]facts.Fact, len(sorted))
	names := make([]string, len(sorted))
	for i, p := range sorted {
		accepted, record := outcomes[i].accepted, outcomes[i].record
		names[i] = p.Name
		if record.NotApplicable() {
			log.Printf("[providers] %s does not apply: %s", p.Name, record.Reason)
			records[i] = record
			continue
		}
		if record.Skipped {
			log.Printf("[providers] %s skipped: %s", p.Name, record.Reason)
			records[i] = record
			continue
		}
		kept := accepted[:0]
		collided, excluded := 0, 0
		for _, f := range accepted {
			if ignored != nil && f.File != "" && ignored(f.File) {
				excluded++
				continue
			}
			if taken != nil && taken(f.Kind, f.Name) {
				collided++
				continue
			}
			kept = append(kept, f)
		}
		if collided > 0 {
			log.Printf("[providers] %s: skipped %d fact(s) whose name+kind identity an extractor already owns", p.Name, collided)
		}
		if excluded > 0 {
			log.Printf("[providers] %s: dropped %d fact(s) about files this repository's ignore globs exclude", p.Name, excluded)
		}
		record.ExcludedByIgnore = excluded
		records[i] = record
		keptAll[i] = kept
	}
	// Two producers reading one call site agree or differ; the seam records
	// which, keeps an agreed relation once, and leaves every difference as
	// emitted. Runs after validation and ignore, before the merge, so the
	// counts describe what the graph holds.
	keptAll = pairAcrossProviders(names, keptAll, records)
	var merged []facts.Fact
	for i, p := range sorted {
		if records[i].Skipped {
			continue
		}
		records[i].FactCount = len(keptAll[i])
		merged = append(merged, keptAll[i]...)
		log.Printf("[providers] %s (%s): merged %d facts", p.Name, records[i].Version, len(keptAll[i]))
		if records[i].Agreed > 0 || records[i].Differing > 0 {
			log.Printf("[providers] %s: %d call relation(s) agreed with another provider, %d differed, %d read by it alone", p.Name, records[i].Agreed, records[i].Differing, records[i].OneSided)
		}
	}
	return merged, records
}

// runOne probes one provider's version, runs it, and strictly validates its
// output. Any failure is returned as a skipped census record.
func runOne(ctx context.Context, p Provider, in Input) ([]facts.Fact, facts.ProviderRecord) {
	repoPath := in.RepoPath
	record := facts.ProviderRecord{Name: p.Name}
	skip := func(format string, args ...any) ([]facts.Fact, facts.ProviderRecord) {
		record.Skipped = true
		record.Reason = fmt.Sprintf(format, args...)
		return nil, record
	}

	if p.applies != nil {
		if yes, missing := p.applies(repoPath, in.Files); !yes {
			return notApplicable(record, missing)
		}
	}
	if len(p.Command) == 0 {
		return runBuiltIn(ctx, p, in)
	}
	if p.unavailable != "" {
		return skip("%s", p.unavailable)
	}

	version, why := probe(ctx, p)
	if why != "" {
		return skip("%s", why)
	}
	record.Version = version
	if p.ExpectedVersion != "" && version != p.ExpectedVersion {
		return skip("version mismatch: reported %s, expected %s", version, p.ExpectedVersion)
	}

	if p.Files == FilesPerFile && in.Cache != nil {
		return runPerFile(ctx, p, in, version, record, skip)
	}

	accepted, census, missing, err := invoke(ctx, p, repoPath, "")
	if err != nil {
		return skip("%v", err)
	}
	if missing != "" {
		return notApplicable(record, missing)
	}
	record.Census = census
	stamp(accepted, p.Name, version)
	sortFacts(accepted)
	return accepted, record
}

// probe asks the provider its version, the seam's test of whether it can run
// at all, and returns the version or why it could not be had.
func probe(ctx context.Context, p Provider) (version, why string) {
	cmd := exec.CommandContext(ctx, p.Command[0], append(append([]string(nil), p.Command[1:]...), "--version")...)
	cmd.Env = p.env
	out, err := cmd.Output()
	if err != nil {
		// Both spellings of a missing tool: exec.ErrNotFound for a bare name the
		// PATH lookup missed, fs.ErrNotExist for an absolute path that is not there.
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Sprintf("command not found: %s", p.Command[0])
		}
		if p.interpreter != "" {
			return "", fmt.Sprintf("the interpreter %s could not run the script: %s", p.interpreter, firstErrorLine(err))
		}
		return "", fmt.Sprintf("version probe failed: %v", err)
	}
	version = strings.TrimSpace(string(out))
	if version == "" {
		return "", "provider reported no version"
	}
	return version, ""
}

// runPerFile serves a per-file provider's facts from the cache for every file
// whose content digest has an entry and runs the provider over the rest,
// listed in an argument file. A fact the provider emits about a file it was
// not handed is dropped and counted: a provider that widened its own scope
// would otherwise put facts in the graph the cache could never invalidate.
func runPerFile(ctx context.Context, p Provider, in Input, version string, record facts.ProviderRecord, skip func(string, ...any) ([]facts.Fact, facts.ProviderRecord)) ([]facts.Fact, facts.ProviderRecord) {
	reuse := &facts.ProviderReuse{}
	record.Reuse = reuse
	var reused []facts.Fact
	var missing []string
	keys := map[string]string{}
	for _, file := range perFileCandidates(p, in.Files) {
		digest := in.Hashes[file]
		if digest == "" {
			missing = append(missing, file)
			continue
		}
		key := perFileKey(p.Name, version, digest)
		keys[file] = key
		if ff, ok := in.Cache.Get(key); ok {
			reused = append(reused, ff...)
			continue
		}
		missing = append(missing, file)
	}
	reuse.Reused = len(reused)
	reuse.FilesReused = len(keys) - len(missing)
	reuse.FilesComputed = len(missing)
	if len(missing) == 0 {
		sortFacts(reused)
		return reused, record
	}
	listing, err := writeFileListing(missing)
	if err != nil {
		return skip("could not write the file listing: %v", err)
	}
	defer func() { _ = os.Remove(listing) }()
	accepted, census, notFound, err := invoke(ctx, p, in.RepoPath, listing)
	if err != nil {
		return skip("%v", err)
	}
	if notFound != "" {
		if len(reused) > 0 {
			return skip("invalid output: the provider says it does not apply (%s), but %d of its facts about these files are cached", notFound, len(reused))
		}
		record.Reuse = nil
		return notApplicable(record, notFound)
	}
	record.Census = census
	stamp(accepted, p.Name, version)
	handed := make(map[string]bool, len(missing))
	for _, file := range missing {
		handed[file] = true
	}
	byFile := make(map[string][]facts.Fact, len(missing))
	computed := accepted[:0]
	for _, f := range accepted {
		if !handed[f.File] {
			reuse.OutsideScope++
			continue
		}
		byFile[f.File] = append(byFile[f.File], f)
		computed = append(computed, f)
	}
	if reuse.OutsideScope > 0 {
		log.Printf("[providers] %s: dropped %d fact(s) about files it was not handed", p.Name, reuse.OutsideScope)
	}
	for _, file := range missing {
		if key := keys[file]; key != "" {
			in.Cache.Put(key, byFile[file])
		}
	}
	reuse.Computed = len(computed)
	all := append(reused, computed...)
	sortFacts(all)
	return all, record
}

// perFileCandidates is the set of walked files a per-file provider reads: those
// with one of its declared extensions, in walk order.
func perFileCandidates(p Provider, files []string) []string {
	var out []string
	for _, file := range files {
		for _, ext := range p.Extensions {
			if strings.HasSuffix(file, ext) {
				out = append(out, file)
				break
			}
		}
	}
	return out
}

func perFileKey(name, version, digest string) string {
	return "file\x00" + name + "\x00" + version + "\x00" + digest
}

func writeFileListing(files []string) (string, error) {
	f, err := os.CreateTemp("", "enola-provider-files-*.txt")
	if err != nil {
		return "", err
	}
	w := bufio.NewWriter(f)
	for _, file := range files {
		if _, err := w.WriteString(filepath.ToSlash(file) + "\n"); err != nil {
			_ = f.Close()
			return "", err
		}
	}
	if err := w.Flush(); err != nil {
		_ = f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	return f.Name(), nil
}

// invoke runs the provider over the repository, over the listed files only
// when listing names one, and returns its validated facts and census, or what
// was missing when the provider says nothing it reads is there.
func invoke(ctx context.Context, p Provider, repoPath, listing string) ([]facts.Fact, *facts.ProviderCensus, string, error) {
	args := append(append([]string(nil), p.Command[1:]...), repoPath)
	if listing != "" {
		args = append(args, FilesFlag, listing)
	}
	cmd := exec.CommandContext(ctx, p.Command[0], args...)
	cmd.Env = p.env
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, nil, "", fmt.Errorf("provider run failed: %v (%s)", err, strings.TrimSpace(stderr.String()))
	}
	accepted, err := parseFacts(out)
	if err != nil {
		return nil, nil, "", fmt.Errorf("invalid output: %v", err)
	}
	census, err := parseCensus(stderr.Bytes())
	if err != nil {
		return nil, nil, "", fmt.Errorf("invalid census: %v", err)
	}
	missing, err := parseNotApplicable(stderr.Bytes())
	if err != nil {
		return nil, nil, "", err
	}
	if missing != "" && len(accepted) > 0 {
		return nil, nil, "", fmt.Errorf("invalid output: the provider says it does not apply (%s) and emitted %d fact(s)", missing, len(accepted))
	}
	return accepted, census, missing, nil
}

// parseNotApplicable reads the provider's statement that nothing it reads is
// in the repository. Like the census, it is made once or not at all, and a
// statement with no reason is refused: "not applicable" with nothing named is
// as unreadable as a skip with no reason.
func parseNotApplicable(stderr []byte) (string, error) {
	missing := ""
	scanner := bufio.NewScanner(bytes.NewReader(stderr))
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, NotApplicablePrefix) {
			continue
		}
		if missing != "" {
			return "", fmt.Errorf("invalid output: more than one %q line", strings.TrimSpace(NotApplicablePrefix))
		}
		missing = strings.TrimSpace(strings.TrimPrefix(line, NotApplicablePrefix))
		if missing == "" {
			return "", fmt.Errorf("invalid output: %q names nothing that was missing", strings.TrimSpace(NotApplicablePrefix))
		}
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return missing, nil
}

// notApplicable records a provider that did not run because nothing it reads
// was there. It is Skipped too, so every reader asking what contributed keeps
// its answer; Applicable says why.
func notApplicable(record facts.ProviderRecord, missing string) ([]facts.Fact, facts.ProviderRecord) {
	no := false
	record.Skipped = true
	record.Applicable = &no
	record.Reason = missing
	return nil, record
}

// firstErrorLine is what a failed process said first on stderr, which for an
// interpreter that cannot load a script is the load error itself; the exit
// status alone says only that something failed.
func firstErrorLine(err error) string {
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		for _, line := range strings.Split(string(exit.Stderr), "\n") {
			if line = strings.TrimSpace(line); line != "" {
				return line
			}
		}
	}
	return err.Error()
}

func stamp(ff []facts.Fact, name, version string) {
	for i := range ff {
		ff[i].SetProp(PropProvider, name)
		ff[i].SetProp(PropProviderVersion, version)
	}
}

// sortFacts orders facts before merge so the graph is a function of what the
// provider emitted, never of the order it happened to emit it in.
func sortFacts(ff []facts.Fact) {
	sort.Slice(ff, func(i, j int) bool { return factOrder(ff[i]) < factOrder(ff[j]) })
}

func digestOf(parts ...string) string {
	h := sha256.New()
	for _, part := range parts {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// parseFacts decodes a provider's JSONL strictly. The first invalid line
// rejects the whole output: a provider that emits one line the schema does not
// admit cannot be trusted about the lines it got right.
func parseFacts(out []byte) ([]facts.Fact, error) {
	var accepted []facts.Fact
	scanner := bufio.NewScanner(bytes.NewReader(out))
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		f, err := parseFactLine(line)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", lineNo, err)
		}
		accepted = append(accepted, f)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return accepted, nil
}

// parseFactLine validates one JSONL line against the fact schema: only known
// fields, a known kind, known relation kinds with targets, no engine-assigned
// or seam-stamped fields claimed by the provider, and a resolution_level on
// every fact.
func parseFactLine(line string) (facts.Fact, error) {
	var f facts.Fact
	dec := json.NewDecoder(strings.NewReader(line))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return facts.Fact{}, err
	}
	// Decode stops at the end of the first JSON value, so a line carrying two
	// objects used to keep the first and silently drop the rest — content lost
	// under a contract that promises strictness. Anything but EOF after the
	// fact object rejects the line.
	var trailing json.RawMessage
	if err := dec.Decode(&trailing); err != io.EOF {
		return facts.Fact{}, fmt.Errorf("trailing data after the fact object on the same line")
	}
	if err := validateFact(f); err != nil {
		return facts.Fact{}, err
	}
	return f, nil
}

// validateFact is the schema check every provider fact passes, external or
// built-in: a known kind, known relations with targets, a resolution level
// from the vocabulary, and nothing the engine or the seam assigns.
func validateFact(f facts.Fact) error {
	if !allowedFactKinds[f.Kind] {
		return fmt.Errorf("kind %q is not a provider-emittable fact kind (allowed: %s)", f.Kind, joinSorted(allowedFactKinds))
	}
	if f.Name == "" {
		return fmt.Errorf("fact has no name")
	}
	if f.Repo != "" {
		return fmt.Errorf("repo is engine-assigned; a provider must not set it")
	}
	for _, rel := range f.Relations {
		if !allowedRelationKinds[rel.Kind] {
			return fmt.Errorf("relation kind %q is not in the vocabulary (allowed: %s)", rel.Kind, joinSorted(allowedRelationKinds))
		}
		if rel.Target == "" {
			return fmt.Errorf("relation %q has no target", rel.Kind)
		}
	}
	level, _ := f.PropAny(PropResolutionLevel).(string)
	if level == "" {
		return fmt.Errorf("fact %q carries no %s prop — a provider must say how it resolved what it emitted", f.Name, PropResolutionLevel)
	}
	if !allowedResolutionLevels[level] {
		return fmt.Errorf("fact %q carries %s %q, which is not in the vocabulary (allowed: %s)", f.Name, PropResolutionLevel, level, joinSorted(allowedResolutionLevels))
	}
	if via, _ := f.PropAny(PropObservedVia).(string); level == LevelRuntimeObserved && via == "" {
		return fmt.Errorf("fact %q is %s but carries no %s prop — a runtime fact must name its observation channel", f.Name, LevelRuntimeObserved, PropObservedVia)
	}
	if in, _ := f.PropAny(PropDeclaredIn).(string); level == LevelDeclared && in == "" {
		return fmt.Errorf("fact %q is %s but carries no %s prop — a declared fact must name the signature file that claims it", f.Name, LevelDeclared, PropDeclaredIn)
	}
	for _, reserved := range []string{PropProvider, PropProviderVersion} {
		if _, claimed := f.Prop(reserved); claimed {
			return fmt.Errorf("prop %q is stamped by the seam; a provider must not set it", reserved)
		}
	}
	return nil
}

func parseCensus(stderr []byte) (*facts.ProviderCensus, error) {
	var census *facts.ProviderCensus
	scanner := bufio.NewScanner(bytes.NewReader(stderr))
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, CensusPrefix) {
			continue
		}
		if census != nil {
			return nil, fmt.Errorf("more than one %q line — a provider states its accounting exactly once", strings.TrimSpace(CensusPrefix))
		}
		var c facts.ProviderCensus
		dec := json.NewDecoder(strings.NewReader(strings.TrimPrefix(line, CensusPrefix)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&c); err != nil {
			return nil, err
		}
		census = &c
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return census, nil
}

// factOrder totally orders one provider's facts for the pre-merge sort.
func factOrder(f facts.Fact) string {
	return fmt.Sprintf("%s\x00%s\x00%s\x00%09d", f.Kind, f.Name, f.File, f.Line)
}

func joinSorted(set map[string]bool) string {
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, ", ")
}
