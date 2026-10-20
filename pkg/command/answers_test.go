package command

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/enola-labs/enola/internal/diff"
	"github.com/enola-labs/enola/internal/facts"
	"github.com/enola-labs/enola/internal/intent"
)

// The answers a program reads from this binary are JSON documents with an
// identity on every finding, a state on every component and a verdict flag on
// every ledger row. These tests read them the way a caller does: from the
// built binary, over a scratch repository, with generic JSON.

var answersBinary struct {
	once sync.Once
	path string
	err  error
	out  []byte
}

func answersBin(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("builds the binary")
	}
	answersBinary.once.Do(func() {
		dir, err := os.MkdirTemp("", "answers-bin")
		if err != nil {
			answersBinary.err = err
			return
		}
		answersBinary.path = filepath.Join(dir, "enola")
		build := exec.Command("go", "build", "-o", answersBinary.path, "../../cmd/enola")
		answersBinary.out, answersBinary.err = build.CombinedOutput()
	})
	if answersBinary.err != nil {
		t.Fatalf("build: %v\n%s", answersBinary.err, answersBinary.out)
	}
	return answersBinary.path
}

const answersConstraints = `components:
  - name: api
    match: ["internal/api/**"]
  - name: store
    match: ["internal/store/**"]
  - name: ghost
    match: ["internal/ghost/**"]
  - name: remote
    match: ["src/**"]
    service: elsewhere
rules:
  - id: api-never-calls-store
    forbid: api
    to: store
    via: calls
    mode: strict
    said: "api never calls store, without exception."
    because: a scratch law.
  - id: ghost-never-calls-store
    forbid: ghost
    to: store
    via: calls
    mode: strict
    sayd: "a key nobody reads"
    because: a law over a part with no members.
  - id: remote-never-calls-store
    forbid: remote
    to: store
    via: calls
    because: a law over a part in a repository this snapshot does not hold.
`

// answersRepo writes a Go repository whose api calls store twice, under a law
// forbidding it, a part matching nothing, a part naming an absent service and
// one rule key nothing reads. It returns the repository path.
func answersRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	write := func(rel, content string) {
		path := filepath.Join(repo, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.com/r\n\ngo 1.22\n")
	write("internal/store/store.go", "package store\n\nfunc Put1() {}\n\nfunc Put2() {}\n")
	write("internal/api/api.go", "package api\n\nimport \"example.com/r/internal/store\"\n\nfunc Serve() { store.Put1(); store.Put2() }\n")
	write("enola/constraints/scratch.yaml", answersConstraints)
	return repo
}

type answersRun struct {
	t    *testing.T
	bin  string
	repo string
	home string
}

func newAnswersRun(t *testing.T) *answersRun {
	return &answersRun{t: t, bin: answersBin(t), repo: answersRepo(t), home: t.TempDir()}
}

func (a *answersRun) run(args ...string) (int, string, string) {
	a.t.Helper()
	cmd := exec.Command(a.bin, args...)
	cmd.Dir = a.repo
	cmd.Env = append(os.Environ(), "HOME="+a.home, "ENOLA_NO_UPDATE_CHECK=1")
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	code := 0
	if exit, ok := err.(*exec.ExitError); ok {
		code = exit.ExitCode()
	} else if err != nil {
		a.t.Fatalf("%v: %v", args, err)
	}
	return code, stdout.String(), stderr.String()
}

func (a *answersRun) mustGenerate() {
	a.t.Helper()
	if code, out, errOut := a.run("--generate", "."); code != 0 {
		a.t.Fatalf("generate exit %d\n%s\n%s", code, out, errOut)
	}
}

func decodeJSON(t *testing.T, label, doc string) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(doc), &out); err != nil {
		t.Fatalf("%s is not a JSON object: %v\n%s", label, err, doc)
	}
	return out
}

func objects(v any) []map[string]any {
	list, _ := v.([]any)
	out := make([]map[string]any, 0, len(list))
	for _, item := range list {
		if m, ok := item.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// lint --json names every component with its state: the one matching nothing
// and the one naming a service this snapshot does not hold included, which is
// where a parser of the text lost two of four.
func TestLintJSONNamesEveryComponentWithItsState(t *testing.T) {
	a := newAnswersRun(t)
	a.mustGenerate()

	code, out, errOut := a.run("constraints", "lint", "--json", ".")
	if code != 0 {
		t.Fatalf("lint --json exit %d, want 0 (the declaration is valid)\n%s\n%s", code, out, errOut)
	}
	doc := decodeJSON(t, "lint --json", out)
	if doc["valid"] != true {
		t.Errorf("valid = %v, want true", doc["valid"])
	}
	want := map[string]struct {
		state   string
		members bool
		service string
	}{
		"api":    {"resolved", true, ""},
		"store":  {"resolved", true, ""},
		"ghost":  {"matches_nothing", false, ""},
		"remote": {"unasked", false, "elsewhere"},
	}
	got := map[string]map[string]any{}
	for _, c := range objects(doc["components"]) {
		name, _ := c["name"].(string)
		got[name] = c
	}
	if len(got) != len(want) {
		t.Errorf("components = %d, want %d: %v", len(got), len(want), got)
	}
	for name, w := range want {
		c, ok := got[name]
		if !ok {
			t.Errorf("component %s missing from lint --json", name)
			continue
		}
		if c["state"] != w.state {
			t.Errorf("%s state = %v, want %s", name, c["state"], w.state)
		}
		members, _ := c["members"].(float64)
		if (members > 0) != w.members {
			t.Errorf("%s members = %v, want members>0 = %v", name, c["members"], w.members)
		}
		if w.service != "" && c["service"] != w.service {
			t.Errorf("%s service = %v, want %s", name, c["service"], w.service)
		}
		if sel, _ := c["selector"].(string); sel == "" {
			t.Errorf("%s carries no selector", name)
		}
	}

	unknown := objects(doc["unknown_keys"])
	if len(unknown) != 1 {
		t.Fatalf("unknown_keys = %v, want exactly the one sayd key", doc["unknown_keys"])
	}
	k := unknown[0]
	if k["file"] != "enola/constraints/scratch.yaml" || k["rule"] != "ghost-never-calls-store" || k["key"] != "sayd" {
		t.Errorf("unknown key = %v, want file enola/constraints/scratch.yaml, rule ghost-never-calls-store, key sayd", k)
	}
	if line, _ := k["line"].(float64); int(line) != lineOf(t, answersConstraints, "    sayd:") {
		t.Errorf("unknown key line = %v, want %d", k["line"], lineOf(t, answersConstraints, "    sayd:"))
	}
}

// The text form keeps every line it printed and adds one warning naming the
// unknown key with its file and line; the exit stays 0, since a hand-written
// file with a key nothing reads is not invalid.
func TestLintTextWarnsOnAnUnknownKeyAndExitsZero(t *testing.T) {
	a := newAnswersRun(t)
	a.mustGenerate()
	code, out, _ := a.run("constraints", "lint", ".")
	if code != 0 {
		t.Fatalf("lint exit %d, want 0\n%s", code, out)
	}
	for _, want := range []string{
		"ghost                    0 member(s)  <- matches nothing; every rule naming it holds vacuously",
		"remote                   0 member(s)  <- names service elsewhere, absent from this snapshot; unasked, never failed",
		"every declaration is valid.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("lint text lost %q:\n%s", want, out)
		}
	}
	line := lineOf(t, answersConstraints, "    sayd:")
	wantWarning := "enola/constraints/scratch.yaml:" + itoa(line) + ": rule ghost-never-calls-store: key \"sayd\""
	if !strings.Contains(out, wantWarning) {
		t.Errorf("lint text does not warn %q:\n%s", wantWarning, out)
	}
	if strings.Contains(out, `key "said"`) {
		t.Errorf("said is a rule key and must not be warned about:\n%s", out)
	}
}

// Every ledger row says whether the rule was verdicted, and why not when it was
// not; a rule carrying said returns the sentence unchanged.
func TestLedgerRowsSayWhetherTheyWereVerdicted(t *testing.T) {
	a := newAnswersRun(t)
	a.mustGenerate()
	code, out, errOut := a.run("constraints", "ledger", "--json", "--breaches", ".")
	if code != 0 {
		t.Fatalf("ledger exit %d\n%s\n%s", code, out, errOut)
	}
	doc := decodeJSON(t, "ledger --json", out)
	rows := map[string]map[string]any{}
	for _, r := range objects(doc["rules"]) {
		id, _ := r["id"].(string)
		rows[id] = r
	}
	api := rows["api-never-calls-store"]
	if api == nil {
		t.Fatalf("no ledger row for api-never-calls-store: %s", out)
	}
	if api["said"] != "api never calls store, without exception." {
		t.Errorf("said on the ledger row = %v, want the sentence as written", api["said"])
	}
	if api["verdicted"] != true {
		t.Errorf("api-never-calls-store verdicted = %v, want true", api["verdicted"])
	}
	if _, has := api["unverdicted_reason"]; has {
		t.Errorf("a verdicted row carries unverdicted_reason: %v", api)
	}
	for id, reason := range map[string]string{
		"ghost-never-calls-store":  "matches_nothing",
		"remote-never-calls-store": "service_absent",
	} {
		row := rows[id]
		if row == nil {
			t.Errorf("no ledger row for %s", id)
			continue
		}
		if row["verdicted"] != false {
			t.Errorf("%s verdicted = %v, want false", id, row["verdicted"])
		}
		if row["unverdicted_reason"] != reason {
			t.Errorf("%s unverdicted_reason = %v, want %s", id, row["unverdicted_reason"], reason)
		}
		if detail, _ := row["unverdicted_detail"].(string); detail == "" {
			t.Errorf("%s names no detail for why it was not verdicted", id)
		}
	}
	if _, has := rows["ghost-never-calls-store"]["said"]; has {
		t.Error("a rule without said reads said on its ledger row")
	}
}

// Named the config of a cluster, the ledger reads the union every member
// holds and reports for the member --repo names. It used to look for the
// snapshot beside the config file, found none, and refused a cluster whose
// union held every rule, so a caller reading one member's law from the union
// fell back to the member's own older snapshot and missed rules compiled since.
func TestLedgerReadsTheUnionAConfigNames(t *testing.T) {
	a := newAnswersRun(t)
	other := writeGoRepo(t)
	cfg := filepath.Join(t.TempDir(), "mcp-arch.yaml")
	if err := os.WriteFile(cfg, []byte("repos:\n  - "+a.repo+"\n  - "+other+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, out, errOut := a.run("--generate", cfg); code != 0 {
		t.Fatalf("generate exit %d\n%s\n%s", code, out, errOut)
	}

	code, _, errOut := a.run("constraints", "ledger", "--json", cfg)
	if code != 2 || !strings.Contains(errOut, "Pass repo as one of them") || !strings.Contains(errOut, filepath.Base(a.repo)) {
		t.Errorf("a union with no member named: exit %d, want 2 and the members named\n%s", code, errOut)
	}

	code, out, errOut := a.run("constraints", "ledger", "--json", "--breaches", "--repo", filepath.Base(a.repo), cfg)
	if code != 0 {
		t.Fatalf("ledger --repo over the union exit %d\n%s\n%s", code, out, errOut)
	}
	doc := decodeJSON(t, "ledger --json over the union", out)
	var api map[string]any
	for _, r := range objects(doc["rules"]) {
		if r["id"] == "api-never-calls-store" {
			api = r
		}
	}
	if api == nil {
		t.Fatalf("the union's ledger names no api-never-calls-store: %s", out)
	}
	if api["verdicted"] != true || len(objects(api["breach_list"])) == 0 {
		t.Errorf("api-never-calls-store over the union: %v, want verdicted with its breaches listed", api)
	}

	code, _, errOut = a.run("constraints", "ledger", "--json", "--repo", "nowhere", cfg)
	if code != 2 || !strings.Contains(errOut, `repo "nowhere" is not a repository of this snapshot`) {
		t.Errorf("a member the union does not hold: exit %d\n%s", code, errOut)
	}
}

// Every finding in insights.json and in check --json carries the identity the
// diff pairs findings on, and it equals the ledger's breach identity and the
// SARIF enola/v1 fingerprint for the same finding.
func TestEveryFindingCarriesItsIdentity(t *testing.T) {
	a := newAnswersRun(t)
	a.mustGenerate()

	raw, err := os.ReadFile(filepath.Join(a.repo, ".enola", "insights.json"))
	if err != nil {
		t.Fatal(err)
	}
	var insights []map[string]any
	if err := json.Unmarshal(raw, &insights); err != nil {
		t.Fatal(err)
	}
	if len(insights) == 0 {
		t.Fatal("the scratch repository produced no finding")
	}
	for _, in := range insights {
		assertIdentity(t, "insights.json", in)
	}

	if code, out, errOut := a.run("baseline", "pin", "."); code != 0 {
		t.Fatalf("baseline pin exit %d\n%s\n%s", code, out, errOut)
	}
	extra := "package api\n\nimport \"example.com/r/internal/store\"\n\nfunc Again() { store.Put1() }\n"
	if err := os.WriteFile(filepath.Join(a.repo, "internal/api/again.go"), []byte(extra), 0o644); err != nil {
		t.Fatal(err)
	}

	_, out, errOut := a.run("check", "--json", ".")
	verdict := decodeJSON(t, "check --json", out)
	lists := findingLists(verdict)
	if len(lists) == 0 {
		t.Fatalf("check --json carried no finding list\n%s\n%s", out, errOut)
	}
	byTitle := map[string]string{}
	for label, list := range lists {
		for _, in := range list {
			id := assertIdentity(t, "check --json "+label, in)
			title, _ := in["title"].(string)
			byTitle[title] = id
		}
	}

	_, sarif, _ := a.run("check", "--format", "sarif", ".")
	var doc struct {
		Runs []struct {
			Results []struct {
				PartialFingerprints map[string]string `json:"partialFingerprints"`
			} `json:"results"`
		} `json:"runs"`
	}
	if err := json.Unmarshal([]byte(sarif), &doc); err != nil {
		t.Fatalf("sarif: %v\n%s", err, sarif)
	}
	fingerprints := map[string]bool{}
	for _, run := range doc.Runs {
		for _, r := range run.Results {
			fingerprints[r.PartialFingerprints["enola/v1"]] = true
		}
	}
	if len(fingerprints) == 0 {
		t.Fatalf("sarif carried no result\n%s", sarif)
	}
	matched := 0
	for _, id := range byTitle {
		if fingerprints[id] {
			matched++
		}
	}
	if matched == 0 {
		t.Errorf("no check --json identity equals a SARIF enola/v1 fingerprint: identities %v, fingerprints %v", byTitle, fingerprints)
	}

	_, ledgerOut, _ := a.run("constraints", "ledger", "--json", "--breaches", ".")
	ledger := decodeJSON(t, "ledger --json --breaches", ledgerOut)
	breaches := 0
	insightIDs := map[string]bool{}
	for _, in := range insights {
		id, _ := in["identity"].(string)
		insightIDs[id] = true
	}
	for _, r := range objects(ledger["rules"]) {
		for _, b := range objects(r["breach_list"]) {
			breaches++
			id, _ := b["identity"].(string)
			if !insightIDs[id] {
				t.Errorf("ledger breach identity %s is on no finding in insights.json", id)
			}
		}
	}
	if breaches == 0 {
		t.Fatalf("the ledger listed no breach: %s", ledgerOut)
	}
}

// findingLists gathers every array of findings in a check --json document, by
// its path, wherever it sits: a finding is an object carrying title,
// confidence and evidence.
func findingLists(doc map[string]any) map[string][]map[string]any {
	out := map[string][]map[string]any{}
	var walk func(path string, v any)
	walk = func(path string, v any) {
		switch x := v.(type) {
		case map[string]any:
			keys := make([]string, 0, len(x))
			for k := range x {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				walk(path+"."+k, x[k])
			}
		case []any:
			list := objects(x)
			if len(list) > 0 && isFinding(list[0]) {
				out[path] = list
				return
			}
			for i, item := range x {
				walk(path+"["+itoa(i)+"]", item)
			}
		}
	}
	walk("", doc)
	return out
}

func isFinding(m map[string]any) bool {
	_, title := m["title"]
	_, conf := m["confidence"]
	_, ev := m["evidence"]
	return title && conf && ev
}

func assertIdentity(t *testing.T, where string, in map[string]any) string {
	t.Helper()
	title, _ := in["title"].(string)
	source, _ := in["source"].(string)
	id, _ := in["identity"].(string)
	want := diff.FindingIdentity(facts.Insight{Title: title, Source: source})
	if id != want {
		t.Errorf("%s: finding %q identity = %q, want %q", where, title, id, want)
	}
	return id
}

func lineOf(t *testing.T, text, prefix string) int {
	t.Helper()
	for i, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, prefix) {
			return i + 1
		}
	}
	t.Fatalf("no line starts with %q", prefix)
	return 0
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

// contract --json prints the engine's closed sets from its own tables and a
// schema for every answer.
func TestContractPrintsTheEnginesOwnTables(t *testing.T) {
	a := newAnswersRun(t)
	code, out, errOut := a.run("contract", "--json")
	if code != 0 {
		t.Fatalf("contract --json exit %d\n%s\n%s", code, out, errOut)
	}
	doc := decodeJSON(t, "contract --json", out)
	if v, _ := doc["contract_version"].(float64); v < 1 {
		t.Errorf("contract_version = %v, want at least 1", doc["contract_version"])
	}
	vocab, _ := doc["vocabulary"].(map[string]any)
	var forms []string
	for _, f := range objects(vocab["rule_forms"]) {
		key, _ := f["key"].(string)
		forms = append(forms, key)
	}
	var want []string
	for _, f := range intent.RuleForms {
		want = append(want, f.Key)
	}
	if strings.Join(forms, ",") != strings.Join(want, ",") {
		t.Errorf("rule forms = %v, want ruleforms.go's table %v", forms, want)
	}
	var exts []string
	list, _ := vocab["page_extensions"].([]any)
	for _, e := range list {
		s, _ := e.(string)
		exts = append(exts, s)
	}
	if strings.Join(exts, ",") != strings.Join(intent.PageExtensionsRead(), ",") {
		t.Errorf("page extensions = %v, want the build's list %v", exts, intent.PageExtensionsRead())
	}
	schemas, _ := doc["schemas"].(map[string]any)
	for _, name := range []string{"facts_record", "receipt", "insights", "ledger", "check", "lint", "contract"} {
		if _, ok := schemas[name]; !ok {
			t.Errorf("contract carries no schema for %s", name)
		}
	}
}
