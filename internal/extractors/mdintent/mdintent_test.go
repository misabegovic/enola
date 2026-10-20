package mdintent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/enola-labs/enola/internal/facts"
	"github.com/enola-labs/enola/internal/intent"
	"github.com/enola-labs/enola/pkg/plugin"
)

const page = `---
title: Estate seams
kind: initiative
enola_intent:
  consumes:
    - {repo: mobile, target: backend, via: graphql}
  layers:
    - repo: backend
      order:
        - {name: handlers, paths: ["app/handlers/**"]}
        - {name: domain, paths: ["app/domain/**"]}
  claims:
    - {metric: fact-count, repo: backend, kind: route, name_prefix: "/api", value: 12}
---

# Estate seams

Prose body — never parsed, may even say enola_intent: without effect.
`

func TestMDIntent_PageCompilesToOwnedFacts(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "wiki", "enola", "prds")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "seams.md"), []byte(page), 0o644); err != nil {
		t.Fatal(err)
	}
	e := New()
	if ok, _ := e.Detect(dir); !ok {
		t.Fatal("a wiki tree carrying enola_intent must detect")
	}
	ff, err := e.Extract(context.Background(), dir, []string{"wiki/enola/prds/seams.md"})
	if err != nil {
		t.Fatal(err)
	}
	byKind := map[string]int{}
	for _, f := range ff {
		if f.Kind != facts.KindIntent {
			t.Fatalf("unexpected kind %s", f.Kind)
		}
		byKind[f.Props["intent_kind"].(string)]++
		if f.File != "wiki/enola/prds/seams.md" {
			t.Fatalf("provenance must be the page, got %s", f.File)
		}
	}
	if byKind["consumes"] != 1 || byKind["layer"] != 2 || byKind["claim"] != 1 {
		t.Fatalf("compiled %v, want 1 consumes + 2 layers + 1 claim", byKind)
	}
	for _, f := range ff {
		if f.Props["intent_kind"] == "consumes" && f.Props["intent_owner"] != "mobile" {
			t.Fatalf("owner must be the declared repo, got %v", f.Props["intent_owner"])
		}
	}
}

func TestMDIntent_InvalidBlockFailsExtraction(t *testing.T) {
	dir := t.TempDir()
	bad := "---\nenola_intent:\n  consumes:\n    - {repo: a, target: b, via: rest}\n---\nbody\n"
	if err := os.WriteFile(filepath.Join(dir, "page.md"), []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := New().Extract(context.Background(), dir, []string{"page.md"})
	if err == nil || !strings.Contains(err.Error(), "graphql") {
		t.Fatalf("an invalid via must fail with the allowed set named, got %v", err)
	}
	// Fatal, so the engine fails the snapshot instead of recording a parse error
	// and publishing a run that quietly lost every verdict the block carried.
	var fatal *plugin.FatalError
	if !errors.As(err, &fatal) {
		t.Fatalf("an invalid declaration must be fatal, got %T: %v", err, err)
	}
}

// A page without the key is a document: a symbol named by its path, a symbol
// per heading named the way a fragment names it, and a names relation per
// link that resolves on disk from the section that carries it. Fenced code
// is not prose, a URL is external, a fragment is nothing, and a link to a
// file that is not there is counted rather than followed.
func TestMDIntent_PlainPagesAreDocuments(t *testing.T) {
	dir := t.TempDir()
	for _, p := range []string{"app/models/order.rb", "docs/guide.md", "lib/util.rb"} {
		if err := os.MkdirAll(filepath.Join(dir, filepath.Dir(p)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, p), []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	notes := "---\ntitle: x\n---\n# Orders\n\nSee [the model](../app/models/order.rb) and `lib/util.rb`.\n\n## How it works\n\nRead [the guide](guide.md#intro), [upstream](https://example.com/x.md), [top](#orders) and [gone](missing.md).\n\n```ruby\n# not a heading\n[link](../app/models/order.rb)\n```\n\n## How it works\n\nA path that escapes the tree: [out](../../etc/passwd).\n"
	if err := os.WriteFile(filepath.Join(dir, "docs", "notes.md"), []byte(notes), 0o644); err != nil {
		t.Fatal(err)
	}
	if ok, _ := New().Detect(dir); !ok {
		t.Fatal("a tree with markdown must detect")
	}
	// The engine hands every extractor the WHOLE walked file list, and a link now
	// resolves against that rather than against the filesystem — so the fixture
	// passes what the engine would. Resolving on disk made a snapshot depend on
	// whether a previous snapshot existed; see inScope.
	ff, err := New().Extract(context.Background(), dir,
		[]string{"app/models/order.rb", "docs/guide.md", "docs/notes.md", "lib/util.rb"})
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]facts.Fact{}
	for _, f := range ff {
		byName[f.Name] = f
	}
	doc := byName["docs/notes.md"]
	if doc.Kind != facts.KindSymbol || doc.Props["symbol_kind"] != SymbolDocument || doc.Props["title"] != "Orders" {
		t.Fatalf("document fact: %+v", doc)
	}
	names := func(f facts.Fact, kind string) []string {
		var out []string
		for _, r := range f.Relations {
			if r.Kind == kind {
				out = append(out, r.Target)
			}
		}
		return out
	}
	if got := names(doc, facts.RelDeclares); strings.Join(got, ",") != "docs,docs/notes.md#orders,docs/notes.md#how-it-works,docs/notes.md#how-it-works-1" {
		t.Fatalf("document declares %v", got)
	}
	if got := names(byName["docs/notes.md#orders"], facts.RelNames); strings.Join(got, ",") != "app/models/order.rb,lib/util.rb" {
		t.Fatalf("section links %v", got)
	}
	if got := names(byName["docs/notes.md#how-it-works"], facts.RelNames); strings.Join(got, ",") != "docs/guide.md" {
		t.Fatalf("second section links %v (fenced, external, fragment and missing must not resolve)", got)
	}
	if sec := byName["docs/notes.md#how-it-works-1"]; sec.Props["level"] != 2 || sec.Line != 17 {
		t.Fatalf("section fact: %+v", sec)
	}
	coverage := byName["mdintent:links"]
	if coverage.Kind != facts.KindExtraction || coverage.Props["unresolved_macros"] != "external=1,missing=1,outside-repo=1" {
		t.Fatalf("link coverage: %+v", coverage)
	}
	if entries := coverage.Props["edge_coverage"].([]map[string]any); entries[0]["resolved"] != 3 {
		t.Fatalf("resolved links: %v", entries)
	}
}

const knowledgePage = `---
title: Choose the queue
kind: decision
enola_intent:
  page:
    type: decision
    status: living
    scope: [backend]
    affects: [mobile]
    relations:
      - {rel: part-of, to: wiki/backend/epics/messaging.md}
      - {rel: supersedes, to: wiki/backend/adrs/old-queue.md}
---

Prose body.
`

func TestMDIntent_PageDeclCompilesKnowledgeNode(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "wiki", "backend", "adrs")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "queue.md"), []byte(knowledgePage), 0o644); err != nil {
		t.Fatal(err)
	}
	ff, err := New().Extract(context.Background(), dir, []string{"wiki/backend/adrs/queue.md"})
	if err != nil {
		t.Fatal(err)
	}
	byKind := map[string]int{}
	for _, f := range ff {
		byKind[f.Props["intent_kind"].(string)]++
		if f.File != "wiki/backend/adrs/queue.md" {
			t.Fatalf("provenance must be the page, got %s", f.File)
		}
	}
	if byKind["page"] != 1 || byKind["relation"] != 2 {
		t.Fatalf("compiled %v, want 1 page node + 2 relation edges", byKind)
	}
	for _, f := range ff {
		if f.Props["intent_kind"] == "page" {
			if f.Props["page_type"] != "decision" || f.Props["status"] != "living" {
				t.Fatalf("page node must carry type and status, got %v", f.Props)
			}
		}
	}
}

func TestMDIntent_InvalidRelFailsWithVocabulary(t *testing.T) {
	dir := t.TempDir()
	bad := "---\nenola_intent:\n  page:\n    type: decision\n    relations:\n      - {rel: replaces, to: wiki/a.md}\n---\nbody\n"
	if err := os.WriteFile(filepath.Join(dir, "page.md"), []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := New().Extract(context.Background(), dir, []string{"page.md"})
	if err == nil || !strings.Contains(err.Error(), "supersedes") {
		t.Fatalf("an unknown rel must fail with the vocabulary named, got %v", err)
	}
}

// TestMDIntent_OriginChannelsCompileAndValidate pins the provenance channel:
// a page declaring origin compiles it onto the page node's props, and a name
// outside the closed vocabulary fails with the allowed set named.
func TestMDIntent_OriginChannelsCompileAndValidate(t *testing.T) {
	dir := t.TempDir()
	good := "---\nenola_intent:\n  page:\n    type: decision\n    origin: [slack, langfuse]\n---\nbody\n"
	if err := os.WriteFile(filepath.Join(dir, "d.md"), []byte(good), 0o644); err != nil {
		t.Fatal(err)
	}
	ff, err := New().Extract(context.Background(), dir, []string{"d.md"})
	if err != nil || len(ff) != 1 {
		t.Fatalf("extract = %v, %v", ff, err)
	}
	origin, _ := ff[0].Props["origin"].([]string)
	if len(origin) != 2 || origin[0] != "slack" || origin[1] != "langfuse" {
		t.Fatalf("origin = %v, want [slack langfuse]", ff[0].Props["origin"])
	}
	bad := "---\nenola_intent:\n  page:\n    type: decision\n    origin: [jira]\n---\nbody\n"
	if err := os.WriteFile(filepath.Join(dir, "b.md"), []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = New().Extract(context.Background(), dir, []string{"b.md"})
	if err == nil || !strings.Contains(err.Error(), "langfuse") {
		t.Fatalf("an unknown channel must fail with the vocabulary named, got %v", err)
	}
}

// TestMDIntent_AnchorsCompileToOwnedFacts pins the page-to-code join: a page
// declaring anchors compiles one anchor fact per entry, owned by the anchored
// repo with the page as provenance, and an invalid anchor fails with the
// shape named.
func TestMDIntent_AnchorsCompileToOwnedFacts(t *testing.T) {
	dir := t.TempDir()
	good := "---\nenola_intent:\n  page:\n    type: decision\n    anchors:\n      - {repo: backend, path: app/services/formatter.rb}\n      - {repo: backend, path: app/jobs}\n---\nbody\n"
	if err := os.WriteFile(filepath.Join(dir, "d.md"), []byte(good), 0o644); err != nil {
		t.Fatal(err)
	}
	ff, err := New().Extract(context.Background(), dir, []string{"d.md"})
	if err != nil {
		t.Fatal(err)
	}
	var anchors []facts.Fact
	for _, f := range ff {
		if f.Props["intent_kind"] == "anchor" {
			anchors = append(anchors, f)
		}
	}
	if len(anchors) != 2 {
		t.Fatalf("compiled %d anchor facts, want 2", len(anchors))
	}
	for _, f := range anchors {
		if f.Props["intent_owner"] != "backend" || f.File != "d.md" {
			t.Fatalf("anchor must be owned by the anchored repo with the page as provenance, got %v @ %s", f.Props, f.File)
		}
	}
	bad := "---\nenola_intent:\n  page:\n    type: decision\n    anchors:\n      - {repo: backend, path: /etc/passwd}\n---\nbody\n"
	if err := os.WriteFile(filepath.Join(dir, "b.md"), []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = New().Extract(context.Background(), dir, []string{"b.md"})
	if err == nil || !strings.Contains(err.Error(), "repo-relative") {
		t.Fatalf("an absolute anchor path must fail as not repo-relative, got %v", err)
	}
}

// A symbol anchor compiles to the same file-level relation a file anchor does,
// so every file-level reader keeps its meaning, and carries the symbol beside it
// for the readers that narrow. A symbol cannot sit on a directory: a directory
// declares no fact to name.
func TestMDIntent_SymbolAnchorCarriesItsSymbol(t *testing.T) {
	dir := t.TempDir()
	good := "---\nenola_intent:\n  page:\n    type: decision\n    anchors:\n      - {repo: backend, path: app/models/profile.rb, symbol: \"Profile#record_ledger\"}\n      - {repo: backend, path: app/jobs}\n---\nbody\n"
	if err := os.WriteFile(filepath.Join(dir, "d.md"), []byte(good), 0o644); err != nil {
		t.Fatal(err)
	}
	ff, err := New().Extract(context.Background(), dir, []string{"d.md"})
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]facts.Fact{}
	for _, f := range ff {
		if f.Props["intent_kind"] == "anchor" {
			byName[f.Name] = f
		}
	}
	sym, ok := byName["anchor: backend app/models/profile.rb Profile#record_ledger"]
	if !ok {
		t.Fatalf("the symbol anchor is named by its repo, file and symbol, got %v", byName)
	}
	if sym.Props["symbol"] != "Profile#record_ledger" || sym.Props["path"] != "app/models/profile.rb" {
		t.Fatalf("the symbol anchor carries its symbol and its file, got %v", sym.Props)
	}
	if len(sym.Relations) != 1 || sym.Relations[0].Target != "backend/app/models/profile.rb" {
		t.Fatalf("the symbol anchor joins its file like a file anchor, got %v", sym.Relations)
	}
	plain, ok := byName["anchor: backend app/jobs"]
	if !ok || plain.Props["symbol"] != nil {
		t.Fatalf("an anchor without a symbol is named and carried as before, got %v", byName)
	}
	for _, path := range []string{"app/models", "app/models/"} {
		bad := "---\nenola_intent:\n  page:\n    type: decision\n    anchors:\n      - {repo: backend, path: " + path + ", symbol: Profile}\n---\nbody\n"
		if err := os.WriteFile(filepath.Join(dir, "b.md"), []byte(bad), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err = New().Extract(context.Background(), dir, []string{"b.md"})
		if err == nil || !strings.Contains(err.Error(), "symbol needs a file") {
			t.Fatalf("a symbol on the directory form %q must fail the block, got %v", path, err)
		}
	}
}

// A snapshot must not depend on whether a previous snapshot exists. This
// repository's own documentation names paths under its output directory, and
// resolving links on disk made those links miss on a cold run and hit on the next
// — three runs, three different fact streams, on the one repository in the corpus
// whose docs describe enola.
func TestMDIntent_LinksResolveAgainstTheWalkedFilesNotTheDisk(t *testing.T) {
	dir := t.TempDir()
	for _, p := range []string{"internal/thing/thing.go", ".enola/extractor_cache.json", "docs/page.md"} {
		if err := os.MkdirAll(filepath.Join(dir, filepath.Dir(p)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, p), []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	page := "# Cache\n\nThe cache lives in [.enola/extractor_cache.json](.enola/extractor_cache.json), " +
		"written by [internal/thing](internal/thing).\n"
	if err := os.WriteFile(filepath.Join(dir, "docs", "page.md"), []byte(page), 0o644); err != nil {
		t.Fatal(err)
	}

	// What the walker keeps: the output directory is ignored, so it is absent even
	// though it is right there on disk.
	ff, err := New().Extract(context.Background(), dir,
		[]string{"docs/page.md", "internal/thing/thing.go"})
	if err != nil {
		t.Fatal(err)
	}

	var targets []string
	for _, f := range ff {
		if f.Name != "docs/page.md#cache" {
			continue
		}
		for _, r := range f.Relations {
			if r.Kind == facts.RelNames {
				targets = append(targets, r.Target)
			}
		}
	}
	for _, got := range targets {
		if strings.HasPrefix(got, ".enola/") {
			t.Errorf("resolved a link into the output directory: %q", got)
		}
	}
	// The directory of a walked file is in scope, so a link naming one still lands.
	if strings.Join(targets, ",") != "internal/thing" {
		t.Errorf("section links = %v, want only internal/thing", targets)
	}
}

// TestSymbolSpanMarkdown: a document spans its text, frontmatter included, and a
// section runs from its heading to the line before the next heading of its level
// or higher, less trailing blank lines; a lower heading stays inside it, and a
// heading inside a fence is not one.
func TestSymbolSpanMarkdown(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	page := "---\ntitle: x\n---\n# Orders\n\nIntro.\n\n## Placing\n\nSteps.\n\n### Paying\n\n```\n# not a heading\n```\n\n## Cancelling\n\nLast words.\n\n\n"
	if err := os.WriteFile(filepath.Join(dir, "docs", "orders.md"), []byte(page), 0o644); err != nil {
		t.Fatal(err)
	}
	ff, err := New().Extract(context.Background(), dir, []string{"docs/orders.md"})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string][4]int{}
	for _, f := range ff {
		got[f.Name] = [4]int{f.Line, f.EndLine, f.Column, f.EndColumn}
	}
	want := map[string][4]int{
		"docs/orders.md":            {1, 20, 1, 12},
		"docs/orders.md#orders":     {4, 20, 1, 12},
		"docs/orders.md#placing":    {8, 16, 1, 4},
		"docs/orders.md#paying":     {12, 16, 1, 4},
		"docs/orders.md#cancelling": {18, 20, 1, 12},
	}
	for name, span := range want {
		if got[name] != span {
			t.Errorf("%s span = %v, want %v", name, got[name], span)
		}
	}
}

// A build may read pages of more extensions than .md. The tests below list an
// invented one through the intent package's seam, since a test cannot pass the
// linker flag a build sets it with.
const listedExtension = ".leaf"

func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for p, body := range files {
		if err := os.MkdirAll(filepath.Join(dir, filepath.Dir(p)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, p), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func extractJSON(t *testing.T, dir string, files ...string) string {
	t.Helper()
	ff, err := New().Extract(context.Background(), dir, files)
	if err != nil {
		t.Fatal(err)
	}
	out, err := json.Marshal(ff)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

const listedPage = `---
title: Choose the queue
kind: decision
enola_intent:
  page:
    type: decision
    status: living
    scope: [backend]
    relations:
      - {rel: part-of, to: wiki/backend/epics/messaging.md}
      - {rel: depends-on, to: wiki/backend/specs/delivery.leaf}
    anchors:
      - {repo: backend, path: app/jobs/deliver.rb}
  claims:
    - {metric: fact-count, repo: backend, kind: route, name_prefix: "/api", value: 3}
---

# Choose the queue

Prose body.
`

// A listed page carrying a block compiles to exactly the facts the same bytes
// compile to under .md, apart from the path they cite, and a relation from it
// to a listed page binds. The .md side is read with the list empty too: what a
// markdown page compiles to must not depend on the list.
func TestMDIntent_AListedPageCompilesAsMarkdownDoes(t *testing.T) {
	md, leaf := t.TempDir(), t.TempDir()
	writeFiles(t, md, map[string]string{"wiki/backend/adrs/queue.md": listedPage})
	writeFiles(t, leaf, map[string]string{"wiki/backend/adrs/queue" + listedExtension: listedPage})

	_, err := New().Extract(context.Background(), md, []string{"wiki/backend/adrs/queue.md"})
	if err == nil || !strings.Contains(err.Error(), "wiki/backend/specs/delivery.leaf") {
		t.Fatalf("with the list empty a relation to %s must fail, got %v", listedExtension, err)
	}

	restore := intent.UsePageExtensions(listedExtension)
	defer restore()
	asMarkdown := extractJSON(t, md, "wiki/backend/adrs/queue.md")
	asListed := extractJSON(t, leaf, "wiki/backend/adrs/queue"+listedExtension)
	if asMarkdown == "[]" || asMarkdown == "null" {
		t.Fatal("the markdown page compiled to nothing")
	}
	if got := strings.ReplaceAll(asListed, "queue"+listedExtension, "queue.md"); got != asMarkdown {
		t.Fatalf("a listed page compiled differently from the same bytes under .md:\nlisted   %s\nmarkdown %s", got, asMarkdown)
	}
	if !strings.Contains(asListed, `"to":"wiki/backend/specs/delivery.leaf"`) {
		t.Fatalf("the relation to a listed page did not compile: %s", asListed)
	}
}

func TestMDIntent_AMalformedBlockInAListedPageIsFatal(t *testing.T) {
	defer intent.UsePageExtensions(listedExtension)()
	dir := t.TempDir()
	bad := "---\nenola_intent:\n  consumes:\n    - {repo: a, target: b, via: rest}\n---\nbody\n"
	writeFiles(t, dir, map[string]string{"page" + listedExtension: bad})
	_, err := New().Extract(context.Background(), dir, []string{"page" + listedExtension})
	var fatal *plugin.FatalError
	if err == nil || !errors.As(err, &fatal) || !strings.Contains(err.Error(), "page"+listedExtension) {
		t.Fatalf("an invalid block in a listed page must fail the snapshot naming the page, got %T: %v", err, err)
	}
}

// A listed file without the key is a document exactly as a markdown file is:
// named by its path, titled by its first top-level heading or else by its file
// name less its own extension, a section per heading, a names relation per
// link that resolves, and nothing else of its body. Its language is the syntax
// it is written in.
func TestMDIntent_AListedFileWithoutTheKeyIsADocument(t *testing.T) {
	defer intent.UsePageExtensions(listedExtension)()
	dir := t.TempDir()
	spec := "---\ntitle: delivery\nkind: reference\n---\n\n## Objective\n\nDeliver once. See [the job](../../app/jobs/deliver.rb).\n\n" +
		"spec: every message is delivered once\n- given a message, when it is sent twice, then one delivery\n\n```\n# not a heading\n```\n"
	writeFiles(t, dir, map[string]string{
		"app/jobs/deliver.rb":                     "x\n",
		"wiki/specs/delivery" + listedExtension:   spec,
		"wiki/specs/headed" + listedExtension:     "# b\n\nNothing else.\n",
		"wiki/specs/notes.md":                     "# notes\n\n[the spec](delivery" + listedExtension + ")\n",
		"wiki/_archive/old" + listedExtension:     "# old\n",
		"wiki/_views/generated" + listedExtension: "# generated\n",
	})
	ff, err := New().Extract(context.Background(), dir, []string{
		"app/jobs/deliver.rb",
		"wiki/_archive/old" + listedExtension,
		"wiki/_views/generated" + listedExtension,
		"wiki/specs/delivery" + listedExtension,
		"wiki/specs/headed" + listedExtension,
		"wiki/specs/notes.md",
	})
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]facts.Fact{}
	for _, f := range ff {
		byName[f.Name] = f
		if strings.Contains(f.File, "_archive/") || strings.Contains(f.File, "_views/") {
			t.Errorf("a listed file under a skipped directory was read: %s", f.Name)
		}
	}
	doc, ok := byName["wiki/specs/delivery"+listedExtension]
	if !ok || doc.Props["symbol_kind"] != SymbolDocument || doc.Props["title"] != "delivery" || doc.Props["language"] != "markdown" {
		t.Fatalf("keyless listed file: %+v", doc)
	}
	if doc.Props["sections"] != 1 {
		t.Fatalf("sections = %v, want the one heading and nothing from the spec lines or the fence", doc.Props["sections"])
	}
	objective := byName["wiki/specs/delivery"+listedExtension+"#objective"]
	if len(objective.Relations) != 1 || objective.Relations[0].Kind != facts.RelNames || objective.Relations[0].Target != "app/jobs/deliver.rb" {
		t.Fatalf("section links = %+v, want the one link that resolves", objective.Relations)
	}
	if headed := byName["wiki/specs/headed"+listedExtension]; headed.Props["title"] != "b" {
		t.Fatalf("a listed document is titled by its first top-level heading, got %+v", headed.Props)
	}
	var fromNotes []string
	for _, r := range byName["wiki/specs/notes.md#notes"].Relations {
		fromNotes = append(fromNotes, r.Target)
	}
	if strings.Join(fromNotes, ",") != "wiki/specs/delivery"+listedExtension {
		t.Fatalf("a markdown link to a listed file = %v, want it to land on the document", fromNotes)
	}
	symbols := 0
	for _, f := range ff {
		if f.Kind == facts.KindSymbol {
			symbols++
		}
	}
	// delivery (document, objective), headed (document, b), notes (document, notes).
	if symbols != 6 {
		t.Fatalf("symbols = %d, want 6", symbols)
	}
}

func TestMDIntent_DetectFiresOnATreeOfListedFilesOnly(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{"wiki/specs/delivery" + listedExtension: "# delivery\n"})
	if ok, _ := New().Detect(dir); ok {
		t.Fatal("with the list empty a tree of listed files must not detect")
	}
	defer intent.UsePageExtensions(listedExtension)()
	if ok, _ := New().Detect(dir); !ok {
		t.Fatal("a tree holding only listed files must detect")
	}
}

func TestMDIntent_OwnsFactFileAnswersForListedFiles(t *testing.T) {
	e := New()
	if e.OwnsFactFile("wiki/a" + listedExtension) {
		t.Fatal("with the list empty a listed file is not owned")
	}
	defer intent.UsePageExtensions(listedExtension)()
	for file, want := range map[string]bool{
		"wiki/a.md":                      true,
		"wiki/a" + listedExtension:       true,
		"wiki/a" + listedExtension + "x": false,
		"wiki/a.rb":                      false,
	} {
		if got := e.OwnsFactFile(file); got != want {
			t.Errorf("OwnsFactFile(%q) = %v, want %v", file, got, want)
		}
	}
}

// With the list empty a file of another extension is not a page at all: it
// yields no fact and no count, as it did before the list existed.
func TestMDIntent_WithTheListEmptyAListedFileYieldsNothing(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"wiki/a" + listedExtension: listedPage,
		"wiki/b" + listedExtension: "# b\n\n[x](a" + listedExtension + ")\n",
	})
	ff, err := New().Extract(context.Background(), dir, []string{"wiki/a" + listedExtension, "wiki/b" + listedExtension})
	if err != nil || len(ff) != 0 {
		t.Fatalf("extract = %v, %v, want nothing", ff, err)
	}
}

// A stamp the intent package refuses fails the snapshot by name rather than
// reading as a build that lists nothing.
func TestMDIntent_ARefusedListFailsTheSnapshot(t *testing.T) {
	defer intent.UsePageExtensions("leaf")()
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{"README.md": "# readme\n"})
	_, err := New().Extract(context.Background(), dir, []string{"README.md"})
	var fatal *plugin.FatalError
	if err == nil || !errors.As(err, &fatal) || !strings.Contains(err.Error(), `"leaf"`) {
		t.Fatalf("a refused list must fail the snapshot naming the entry, got %T: %v", err, err)
	}
}
