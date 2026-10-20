package mining

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/enola-labs/enola/internal/facts"
)

func partIntent(name, match string) facts.Fact {
	return facts.Fact{Kind: facts.KindIntent, Name: "component: " + name, File: "enola/constraints/parts.yaml",
		Props: map[string]any{"intent_kind": "component", "component": name, "match": match, "source": "enola/constraints/parts.yaml"}}
}

func partRuleIntent(id string, props map[string]any) facts.Fact {
	merged := map[string]any{"intent_kind": "rule", "rule": id, "because": "declared", "source": "enola/constraints/parts.yaml"}
	for k, v := range props {
		merged[k] = v
	}
	return facts.Fact{Kind: facts.KindIntent, Name: "rule: " + id, File: "enola/constraints/parts.yaml", Props: merged}
}

func fn(name, file string, calls ...string) facts.Fact {
	rels := make([]facts.Relation, 0, len(calls))
	for _, c := range calls {
		rels = append(rels, facts.Relation{Kind: facts.RelCalls, Target: c})
	}
	return facts.Fact{Kind: facts.KindSymbol, Name: name, File: file,
		Props: map[string]any{"symbol_kind": "function", "language": "go"}, Relations: rels}
}

// partsWorld is three parts: core calls only itself, twelve times; verbs
// calls core twelve times and itself; doors calls verbs. Nothing ever calls
// doors, and core never reaches verbs or doors.
func partsWorld() []facts.Fact {
	ff := []facts.Fact{
		partIntent("core", "core/**"),
		partIntent("verbs", "verbs/**"),
		partIntent("doors", "doors/**"),
	}
	for i := 0; i < 12; i++ {
		ff = append(ff, fn(fmt.Sprintf("core.c%02d", i), "core/c.go", fmt.Sprintf("core.c%02d", (i+1)%12)))
		ff = append(ff, fn(fmt.Sprintf("verbs.v%02d", i), "verbs/v.go", fmt.Sprintf("core.c%02d", i), fmt.Sprintf("verbs.v%02d", (i+1)%12)))
		ff = append(ff, fn(fmt.Sprintf("doors.d%02d", i), "doors/d.go", fmt.Sprintf("verbs.v%02d", i), "fmt.Println"))
	}
	return ff
}

func partCandidate(t *testing.T, r *Report, statementPart string) Candidate {
	t.Helper()
	for _, c := range r.Candidates {
		if strings.Contains(c.Statement, statementPart) {
			return c
		}
	}
	var lines []string
	for _, c := range r.Candidates {
		lines = append(lines, c.Statement)
	}
	t.Fatalf("no candidate containing %q; candidates:\n%s", statementPart, strings.Join(lines, "\n"))
	return Candidate{}
}

func hasStatement(cs []Candidate, part string) bool {
	for _, c := range cs {
		if strings.Contains(c.Statement, part) {
			return true
		}
	}
	return false
}

func TestMineParts_ProposesAZeroCrossingPairWithItsSupport(t *testing.T) {
	r := MineParts(storeOf(partsWorld()), DefaultConfig())
	if r.Grouping != GroupingComponent {
		t.Fatalf("grouping = %q", r.Grouping)
	}
	c := partCandidate(t, r, "core never calls verbs")
	if c.Family != FamilyForbidEdge || c.Numerator != 12 || c.Denominator != 12 || len(c.Exceptions) != 0 {
		t.Fatalf("candidate = %+v", c)
	}
	if c.Support == nil || c.Support.Part != "core" || c.Support.Outgoing != 12 || c.Support.Inside != 12 || c.Support.Crossing != 0 {
		t.Fatalf("support = %+v", c.Support)
	}
	if c.Rule.Forbid != "core" || c.Rule.To != "verbs" || c.Rule.Via != "calls" {
		t.Fatalf("rule = %+v", c.Rule)
	}
	if strings.Contains(c.YAML, "components:") {
		t.Errorf("a rule over declared components redeclares none:\n%s", c.YAML)
	}
	var text bytes.Buffer
	r.WriteText(&text, 0)
	if !strings.Contains(text.String(), "support: core makes 12 resolved calls (of 12 it makes, 100.0%): 12 inside core, 0 into other parts, 0 elsewhere") {
		t.Errorf("the support line is missing from the text:\n%s", text.String())
	}
}

func TestMineParts_DoesNotProposeWhatTheCodeDoes(t *testing.T) {
	r := MineParts(storeOf(partsWorld()), DefaultConfig())
	for _, c := range append(append([]Candidate{}, r.Candidates...), r.Unchecked...) {
		if c.Family == FamilyForbidEdge && ((c.Rule.Forbid == "verbs" && c.Rule.To == "core") || (c.Rule.Forbid == "doors" && c.Rule.To == "verbs")) {
			t.Errorf("proposed a forbid over a pair every edge crosses: %s", c.Statement)
		}
	}
	allow := partCandidate(t, r, "doors calls only doors and verbs")
	if allow.Family != FamilyAllowOnly || len(allow.Rule.Only) != 1 || allow.Rule.Only[0] != "verbs" {
		t.Fatalf("allow-only = %+v", allow)
	}
}

func TestMineParts_SuppressesAZeroWithoutSupport(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MinSupport = 13
	r := MineParts(storeOf(partsWorld()), cfg)
	if hasStatement(r.Candidates, "core never calls verbs") {
		t.Fatal("a zero over 12 outgoing calls was proposed under a support floor of 13")
	}
	for _, s := range r.Suppressed {
		if s.Family == FamilyForbidEdge && s.BelowSupportFloor == 0 {
			t.Fatalf("the suppressed zero was not counted: %+v", r.Suppressed)
		}
	}
}

func TestMineParts_AKindUnderTheResolutionFloorCannotBeChecked(t *testing.T) {
	ff := partsWorld()
	for i := 0; i < 400; i++ {
		ff = append(ff, fn(fmt.Sprintf("core.x%03d", i), "core/x.go", fmt.Sprintf("unresolved%03d", i)))
	}
	cfg := DefaultConfig()
	cfg.MinResolution = 0.2
	r := MineParts(storeOf(ff), cfg)
	if hasStatement(r.Candidates, "core never calls verbs") {
		t.Fatal("a candidate over a kind under the floor was proposed")
	}
	if !hasStatement(r.Unchecked, "core never calls verbs") {
		t.Fatalf("the candidate is not listed as one that cannot be checked: %+v", r.Unchecked)
	}
	var calls KindResolution
	for _, kr := range r.Resolution {
		if kr.Via == "calls" {
			calls = kr
		}
	}
	if !calls.BelowFloor || calls.Made <= calls.Resolved {
		t.Fatalf("calls resolution = %+v", calls)
	}
	var text bytes.Buffer
	r.WriteText(&text, 0)
	if !strings.Contains(text.String(), "Cannot be checked here") {
		t.Errorf("the text does not say what cannot be checked:\n%s", text.String())
	}
}

func TestMineParts_SkipsWhatADeclaredRuleStates(t *testing.T) {
	ff := append(partsWorld(),
		partRuleIntent("core-never-reaches-verbs", map[string]any{"forbid": "core", "to": "verbs", "via": "calls"}),
		partRuleIntent("doors-only-verbs", map[string]any{"allow": "doors", "only": "verbs", "via": "calls"}),
	)
	r := MineParts(storeOf(ff), DefaultConfig())
	if hasStatement(r.Candidates, "core never calls verbs") {
		t.Fatal("a declared forbid was proposed again")
	}
	if hasStatement(r.Candidates, "doors never calls core") || hasStatement(r.Candidates, "doors calls only") {
		t.Fatal("what a declared allow-only states was proposed again")
	}
	declared := map[string][]string{}
	for _, d := range r.Declared {
		declared[d.Statement[:strings.Index(d.Statement, ":")]] = d.DeclaredBy
	}
	if got := declared["core never calls verbs"]; len(got) != 1 || got[0] != "core-never-reaches-verbs" {
		t.Fatalf("declared = %v", declared)
	}
	if got := declared["doors never calls core"]; len(got) != 1 || got[0] != "doors-only-verbs" {
		t.Fatalf("declared = %v", declared)
	}
	if !hasStatement(r.Candidates, "core never calls doors") {
		t.Fatal("an undeclared zero was dropped with the declared ones")
	}
}

func writeRejections(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "enola"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(RejectionsFileName)), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestMineParts_ARejectedCandidateIsNotProposedAgain(t *testing.T) {
	first := MineParts(storeOf(partsWorld()), DefaultConfig())
	target := partCandidate(t, first, "core never calls doors")
	dir := writeRejections(t, fmt.Sprintf(`rejections:
  - identity: %q
    owner: "a person"
    since: "2026-10-14"
    because: "core will own a door once the server moves"
  - identity: "forbid-edge||component|gone|away|calls"
    owner: "a person"
    since: "2026-10-01"
    because: "a part since removed"
`, target.Identity))
	rejections, err := LoadRejections(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	cfg.Rejections = rejections
	r := MineParts(storeOf(partsWorld()), cfg)
	if hasStatement(r.Candidates, "core never calls doors") {
		t.Fatal("a rejected candidate was proposed again")
	}
	if len(r.Rejected) != 1 || r.Rejected[0].Identity != target.Identity || r.Rejected[0].Rejection.Owner != "a person" {
		t.Fatalf("rejected = %+v", r.Rejected)
	}
	if len(r.StaleRejections) != 1 || r.StaleRejections[0].Identity != "forbid-edge||component|gone|away|calls" {
		t.Fatalf("stale = %+v", r.StaleRejections)
	}
	if len(r.Candidates) != len(first.Candidates)-1 {
		t.Fatalf("candidates = %d, want %d", len(r.Candidates), len(first.Candidates)-1)
	}
}

func TestMine_DirectoryModeHonoursRejectionsToo(t *testing.T) {
	first := Mine(storeOf(fullWorld()), DefaultConfig())
	if len(first.Candidates) == 0 {
		t.Fatal("no candidates in the full world")
	}
	target := first.Candidates[0]
	cfg := DefaultConfig()
	cfg.Rejections = []Rejection{{Identity: target.Identity, Owner: "a person", Since: "2026-10-14", Because: "no"}}
	r := Mine(storeOf(fullWorld()), cfg)
	for _, c := range r.Candidates {
		if c.Identity == target.Identity {
			t.Fatal("a rejected directory-mode candidate was proposed again")
		}
	}
	if len(r.Rejected) != 1 {
		t.Fatalf("rejected = %+v", r.Rejected)
	}
}

func TestLoadRejections_RefusesAFileItCannotReadWhole(t *testing.T) {
	for name, body := range map[string]string{
		"missing because":  "rejections:\n  - identity: a\n    owner: b\n    since: 2026-10-14\n",
		"missing owner":    "rejections:\n  - identity: a\n    since: 2026-10-14\n    because: c\n",
		"undated":          "rejections:\n  - identity: a\n    owner: b\n    since: last week\n    because: c\n",
		"duplicate":        "rejections:\n  - {identity: a, owner: b, since: 2026-10-14, because: c}\n  - {identity: a, owner: d, since: 2026-10-14, because: e}\n",
		"unknown field":    "rejections:\n  - {identity: a, owner: b, since: 2026-10-14, because: c, reason: d}\n",
		"not a rejections": "candidates: []\n",
	} {
		if _, err := LoadRejections(writeRejections(t, body), ""); err == nil {
			t.Errorf("%s: the file was read", name)
		}
	}
	if got, err := LoadRejections(t.TempDir(), ""); err != nil || got != nil {
		t.Fatalf("an absent file is no rejections: %v, %v", got, err)
	}
}

func TestMineParts_IdentityNamesTheGroupingAndSurvivesReordering(t *testing.T) {
	base := partsWorld()
	want := MineParts(storeOf(base), DefaultConfig())
	c := partCandidate(t, want, "core never calls verbs")
	if c.Identity != identityKey(FamilyForbidEdge, "", GroupingComponent, "core", "verbs", "calls") {
		t.Fatalf("identity = %q", c.Identity)
	}
	var wantJSONL bytes.Buffer
	if err := want.WriteJSONL(&wantJSONL); err != nil {
		t.Fatal(err)
	}
	for seed := int64(1); seed <= 3; seed++ {
		shuffled := append([]facts.Fact{}, base...)
		rand.New(rand.NewSource(seed)).Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
		var got bytes.Buffer
		if err := MineParts(storeOf(shuffled), DefaultConfig()).WriteJSONL(&got); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got.Bytes(), wantJSONL.Bytes()) {
			t.Fatalf("seed %d: the artifact moved with insertion order", seed)
		}
	}
}

func TestMineParts_JSONLCarriesSupportAndTheSkippedSections(t *testing.T) {
	ff := append(partsWorld(), partRuleIntent("core-never-reaches-verbs", map[string]any{"forbid": "core", "to": "verbs", "via": "calls"}))
	var jsonl bytes.Buffer
	if err := MineParts(storeOf(ff), DefaultConfig()).WriteJSONL(&jsonl); err != nil {
		t.Fatal(err)
	}
	types := map[string]int{}
	for _, line := range strings.Split(strings.TrimSpace(jsonl.String()), "\n") {
		var parsed struct {
			Type     string          `json:"type"`
			Grouping string          `json:"grouping"`
			Support  json.RawMessage `json:"support"`
		}
		if err := json.Unmarshal([]byte(line), &parsed); err != nil {
			t.Fatal(err)
		}
		types[parsed.Type]++
		switch parsed.Type {
		case "mining-report":
			if parsed.Grouping != GroupingComponent {
				t.Errorf("header grouping = %q", parsed.Grouping)
			}
		case "candidate", "declared":
			if len(parsed.Support) == 0 {
				t.Errorf("%s line without support: %s", parsed.Type, line)
			}
		}
	}
	if types["candidate"] == 0 || types["declared"] != 1 {
		t.Fatalf("line types = %v", types)
	}
}

func TestMineParts_ReadsALayerOrderAsParts(t *testing.T) {
	layer := func(name string, order int, path string) facts.Fact {
		return facts.Fact{Kind: facts.KindIntent, Name: "layer " + name, File: "enola-intent.yaml",
			Props: map[string]any{"intent_kind": "layer", "layer_name": name, "order": order, "paths": []string{path}, "source": "enola-intent.yaml"}}
	}
	var ff []facts.Fact
	ff = append(ff, layer("surface", 0, "cmd/**"), layer("core", 1, "lib/**"))
	for i := 0; i < 12; i++ {
		ff = append(ff, facts.Fact{Kind: facts.KindDependency, Name: fmt.Sprintf("cmd%02d -> lib", i), File: fmt.Sprintf("cmd/m%02d.go", i),
			Relations: []facts.Relation{{Kind: facts.RelImports, Target: "lib/l.go"}}})
		ff = append(ff, facts.Fact{Kind: facts.KindDependency, Name: fmt.Sprintf("lib%02d -> lib", i), File: fmt.Sprintf("lib/l%02d.go", i),
			Relations: []facts.Relation{{Kind: facts.RelImports, Target: "lib/l.go"}}})
		ff = append(ff, fn(fmt.Sprintf("cmd.f%02d", i), fmt.Sprintf("cmd/m%02d.go", i)), fn(fmt.Sprintf("lib.g%02d", i), fmt.Sprintf("lib/l%02d.go", i)))
	}
	ff = append(ff, fn("lib.l", "lib/l.go"))
	r := MineParts(storeOf(ff), DefaultConfig())
	if len(r.Parts) != 2 || r.Parts[0].Name != "layer-core" || r.Parts[0].Layer != "core" {
		t.Fatalf("parts = %+v", r.Parts)
	}
	if hasStatement(r.Candidates, "layer-core never imports layer-surface") {
		t.Fatal("what the layer order states was proposed again")
	}
	found := false
	for _, d := range r.Declared {
		if strings.HasPrefix(d.Statement, "layer-core never imports layer-surface") && len(d.DeclaredBy) == 1 && strings.HasPrefix(d.DeclaredBy[0], "layer order:") {
			found = true
		}
	}
	if !found {
		t.Fatalf("the layer order is not named as what declares the pair: %+v", r.Declared)
	}
}

func TestMineParts_HoldsBackWhatTheCodeAlreadyCrossesUnlessAsked(t *testing.T) {
	ff := partsWorld()
	ff = append(ff, fn("core.leak", "core/leak.go", "verbs.v00"))
	r := MineParts(storeOf(ff), DefaultConfig())
	if hasStatement(r.Candidates, "core never calls verbs") || hasStatement(r.Candidates, "core rarely calls verbs") {
		t.Fatal("a pair the code crosses was proposed by default")
	}
	crossed := 0
	for _, s := range r.Suppressed {
		crossed += s.Crossed
	}
	if crossed == 0 {
		t.Fatalf("the held-back candidate was not counted: %+v", r.Suppressed)
	}
	cfg := DefaultConfig()
	cfg.WithCrossings = true
	c := partCandidate(t, MineParts(storeOf(ff), cfg), "core rarely calls verbs")
	if len(c.Exceptions) != 1 || c.Exceptions[0].Name != "core.leak -> verbs.v00" {
		t.Fatalf("exceptions = %+v", c.Exceptions)
	}
}
