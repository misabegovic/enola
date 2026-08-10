package intent

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// PageIntentKey is the frontmatter key enola owns. It is deliberately
// namespaced — a wiki toolchain's own parsers, linters and renderers ignore
// it, and nothing else is ever read from a page: the body is prose, and
// prose is never parsed.
const PageIntentKey = "enola_intent"

// PageIntent is one page's enola_intent: block. Unlike a repo's own
// declaration file, a page declares intent ABOUT repos — each entry names its
// owner — because the page lives where the decision lives, not inside the
// repo it governs.
type PageIntent struct {
	Page       *PageDecl             `yaml:"page"`
	Consumes   []PageSeam            `yaml:"consumes"`
	Layers     []PageLayer           `yaml:"layers"`
	Claims     []Claim               `yaml:"claims"`
	Components []ConstraintComponent `yaml:"components"`
	Rules      []ConstraintRule      `yaml:"rules"`
}

// PageDecl declares the page itself as a knowledge node: what kind of
// knowledge it is, which repos it is about, and how it relates to other
// pages. With it, an entire wiki compiles into the graph — decisions,
// specs and references become facts with edges, not just the pages that
// happen to declare seams.
type PageDecl struct {
	Type      string         `yaml:"type"`
	Status    string         `yaml:"status"`
	Scope     []string       `yaml:"scope"`
	Affects   []string       `yaml:"affects"`
	Origin    []string       `yaml:"origin"`
	Relations []PageRelation `yaml:"relations"`
	Anchors   []PageAnchor   `yaml:"anchors"`
}

// PageAnchor pins a page to a code location: the repo and repo-relative path
// (a file, or a directory prefix) the page's knowledge is about. Where a
// relation joins page to page, an anchor joins page to the measured graph —
// the reverse query ("which decisions govern this file?") becomes a
// traversal, and an anchor whose path no measured fact touches is verdicted
// as dangling instead of aging silently as a stale citation.
type PageAnchor struct {
	Repo string `yaml:"repo"`
	Path string `yaml:"path"`
}

// AllowedOrigins is the closed channel vocabulary for a page's origin — WHERE
// the declared knowledge came from. Channels, not sources: an entry names the
// class of system the page's evidence was ingested from, and the wiki keeps
// the mapping from its own source layout to these names. A new ingest channel
// is a deliberate vocabulary addition here, never a stringly-typed leak.
var AllowedOrigins = map[string]bool{
	"slack": true, "langfuse": true, "notion": true, "github": true,
	"web": true, "repo": true, "other": true,
}

func allowedOrigins() string {
	return "github, langfuse, notion, other, repo, slack, web"
}

// PageRelation is a typed edge from this page to another page, named by its
// repo-relative path. The vocabulary is enola's, small and closed, so the
// dangling-relation verdict can trust every edge it walks.
type PageRelation struct {
	Rel string `yaml:"rel"`
	To  string `yaml:"to"`
}

// AllowedPageRels is the closed relation vocabulary for page declarations.
var AllowedPageRels = map[string]bool{
	"depends-on": true, "supersedes": true, "superseded-by": true,
	"part-of": true, "relates-to": true,
}

func allowedPageRels() string {
	return "depends-on, part-of, relates-to, supersedes, superseded-by"
}

// PageSeam is a declared seam with an explicit owner: `repo` intends to
// consume `target` via the named mechanism.
type PageSeam struct {
	Repo   string `yaml:"repo"`
	Target string `yaml:"target"`
	Via    string `yaml:"via"`
}

// PageLayer declares a repo's layer order from a page.
type PageLayer struct {
	Repo  string  `yaml:"repo"`
	Order []Layer `yaml:"order"`
}

// Claim is a measurable statement about the graph, verdicted every snapshot.
// Two metrics: fact-count (kind + filters + expected value) and seam
// (a measured cross-repo edge must exist).
type Claim struct {
	Metric     string `yaml:"metric"`
	Repo       string `yaml:"repo"`
	Kind       string `yaml:"kind"`
	FilePrefix string `yaml:"file_prefix"`
	NamePrefix string `yaml:"name_prefix"`
	Value      *int   `yaml:"value"`
	Consumer   string `yaml:"consumer"`
	Provider   string `yaml:"provider"`
	Via        string `yaml:"via"`
}

// ConstraintComponent names a set of measured facts by where they live: any
// fact whose file falls under a match pattern (and, when narrowed, whose kind
// or exact name agrees) is a member. Components exist so rules can speak about
// the architecture in the page's own vocabulary — "the domain", "the adapters"
// — instead of repeating path lists per rule.
type ConstraintComponent struct {
	Name        string   `yaml:"name"`
	Match       []string `yaml:"match"`
	Kind        string   `yaml:"kind"`
	NamePattern string   `yaml:"name_pattern"`
}

// ConstraintRule forbids one component reaching another via a named edge kind.
// Unlike a claim (a measurement expected to hold), a rule carries enforcement
// semantics: a matching edge is a decided-rule breach at full confidence, and
// Because — mandatory — is the rationale the resulting finding surfaces, so a
// violation always says why the rule exists, not only that it was broken.
type ConstraintRule struct {
	ID      string `yaml:"id"`
	Forbid  string `yaml:"forbid"`
	To      string `yaml:"to"`
	Via     string `yaml:"via"`
	Because string `yaml:"because"`
}

// AllowedComponentKinds is the closed fact-kind vocabulary a component selector
// may narrow to — the measured kinds the constraints explainer resolves over.
var AllowedComponentKinds = map[string]bool{
	"module": true, "symbol": true, "route": true, "storage": true,
}

func allowedComponentKinds() string {
	return "module, route, storage, symbol"
}

// AllowedRuleVias is the closed edge vocabulary a rule may forbid — relation
// kinds the graph actually carries, so a rule can only forbid something the
// evaluator can see.
var AllowedRuleVias = map[string]bool{
	"depends_on": true, "imports": true, "calls": true,
}

func allowedRuleVias() string {
	return "calls, depends_on, imports"
}

// ParsePage reads a markdown page's frontmatter and returns its enola_intent
// block, or nil when the page carries none. Only the frontmatter between the
// leading --- fences is read; the body is never parsed. An invalid block is
// an error, never a silent skip.
func ParsePage(src []byte) (*PageIntent, error) {
	text := string(src)
	if !strings.HasPrefix(text, "---\n") {
		return nil, nil
	}
	end := strings.Index(text[4:], "\n---")
	if end < 0 {
		return nil, nil
	}
	front := text[4 : 4+end]
	if !strings.Contains(front, PageIntentKey+":") {
		return nil, nil
	}
	var doc struct {
		Intent *PageIntent `yaml:"enola_intent"`
	}
	if err := yaml.Unmarshal([]byte(front), &doc); err != nil {
		return nil, fmt.Errorf("parsing %s frontmatter: %w", PageIntentKey, err)
	}
	if doc.Intent == nil {
		return nil, nil
	}
	if err := doc.Intent.Validate(); err != nil {
		return nil, err
	}
	return doc.Intent, nil
}

// Validate checks vocabulary and shape with the same rules a declaration
// file gets: named errors, allowed sets spelled out.
func (p *PageIntent) Validate() error {
	var problems []string
	if pg := p.Page; pg != nil {
		if !validToken(pg.Type) {
			problems = append(problems, fmt.Sprintf("page: type %q must be a lowercase token", pg.Type))
		}
		if pg.Status != "" && !validToken(pg.Status) {
			problems = append(problems, fmt.Sprintf("page: status %q must be a lowercase token", pg.Status))
		}
		for i, s := range append(append([]string{}, pg.Scope...), pg.Affects...) {
			if s == "" {
				problems = append(problems, fmt.Sprintf("page: scope/affects entry %d is empty", i))
			}
		}
		for i, o := range pg.Origin {
			if !AllowedOrigins[o] {
				problems = append(problems, fmt.Sprintf("page.origin[%d]: %q is not a channel (allowed: %s)", i, o, allowedOrigins()))
			}
		}
		for i, r := range pg.Relations {
			if !AllowedPageRels[r.Rel] {
				problems = append(problems, fmt.Sprintf("page.relations[%d]: rel %q is not in the vocabulary (allowed: %s)", i, r.Rel, allowedPageRels()))
			}
			if !strings.HasSuffix(r.To, ".md") {
				problems = append(problems, fmt.Sprintf("page.relations[%d]: to %q must be a repo-relative markdown path", i, r.To))
			}
		}
		for i, a := range pg.Anchors {
			if a.Repo == "" || a.Path == "" {
				problems = append(problems, fmt.Sprintf("page.anchors[%d]: needs repo and path", i))
				continue
			}
			if strings.HasPrefix(a.Path, "/") || strings.HasPrefix(a.Path, "..") || strings.Contains(a.Path, "/../") {
				problems = append(problems, fmt.Sprintf("page.anchors[%d]: path %q must be repo-relative", i, a.Path))
			}
		}
	}
	for i, c := range p.Consumes {
		if c.Repo == "" || c.Target == "" {
			problems = append(problems, fmt.Sprintf("consumes[%d]: needs repo and target", i))
		}
		if !AllowedVia(c.Via) {
			problems = append(problems, fmt.Sprintf("consumes[%d]: via %q is not a linker mechanism (allowed: %s)", i, c.Via, allowedViaKinds()))
		}
	}
	for i, l := range p.Layers {
		if l.Repo == "" || len(l.Order) == 0 {
			problems = append(problems, fmt.Sprintf("layers[%d]: needs repo and order", i))
		}
		for j, en := range l.Order {
			if en.Name == "" || len(en.Paths) == 0 {
				problems = append(problems, fmt.Sprintf("layers[%d].order[%d]: needs name and paths", i, j))
			}
		}
	}
	for i, c := range p.Claims {
		switch c.Metric {
		case "fact-count":
			if c.Repo == "" || c.Kind == "" || c.Value == nil {
				problems = append(problems, fmt.Sprintf("claims[%d]: fact-count needs repo, kind, value", i))
			}
		case "seam":
			if c.Consumer == "" || c.Provider == "" || !AllowedVia(c.Via) {
				problems = append(problems, fmt.Sprintf("claims[%d]: seam needs consumer, provider and a linker via", i))
			}
		default:
			problems = append(problems, fmt.Sprintf("claims[%d]: unknown metric %q (allowed: fact-count, seam)", i, c.Metric))
		}
	}
	componentNames := map[string]bool{}
	for i, c := range p.Components {
		if !validToken(c.Name) {
			problems = append(problems, fmt.Sprintf("components[%d]: name %q must be a lowercase token", i, c.Name))
		}
		if len(c.Match) == 0 {
			problems = append(problems, fmt.Sprintf("components[%d] (%s): needs at least one match pattern", i, c.Name))
		}
		for j, m := range c.Match {
			if !validConstraintMatch(m) {
				problems = append(problems, fmt.Sprintf("components[%d].match[%d]: %q must be an exact path or a prefix/** subtree (no other glob forms)", i, j, m))
			}
		}
		if c.Kind != "" && !AllowedComponentKinds[c.Kind] {
			problems = append(problems, fmt.Sprintf("components[%d]: kind %q is not a measured fact kind (allowed: %s)", i, c.Kind, allowedComponentKinds()))
		}
		componentNames[c.Name] = true
	}
	ruleIDs := map[string]bool{}
	for i, r := range p.Rules {
		if !validToken(r.ID) {
			problems = append(problems, fmt.Sprintf("rules[%d]: id %q must be a lowercase token", i, r.ID))
		} else if ruleIDs[r.ID] {
			problems = append(problems, fmt.Sprintf("rules[%d]: id %q is declared twice on this page", i, r.ID))
		}
		ruleIDs[r.ID] = true
		if !componentNames[r.Forbid] {
			problems = append(problems, fmt.Sprintf("rules[%d] (%s): forbid %q names no declared component", i, r.ID, r.Forbid))
		}
		if !componentNames[r.To] {
			problems = append(problems, fmt.Sprintf("rules[%d] (%s): to %q names no declared component", i, r.ID, r.To))
		}
		if !AllowedRuleVias[r.Via] {
			problems = append(problems, fmt.Sprintf("rules[%d] (%s): via %q is not a rule edge kind (allowed: %s)", i, r.ID, r.Via, allowedRuleVias()))
		}
		if r.Because == "" {
			problems = append(problems, fmt.Sprintf("rules[%d] (%s): needs a because — a rule with no stated rationale cannot surface one in its findings", i, r.ID))
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("invalid %s block: %s", PageIntentKey, strings.Join(problems, "; "))
	}
	return nil
}

// validConstraintMatch enforces the same bounded glob dialect declared layers
// match with (see layers' matchDeclaredLayerPath): an exact repo-relative path,
// or a `prefix/**` subtree — nothing more. Any other glob metacharacter is
// rejected at parse time, so a selector the evaluator would silently fail to
// match is an error the page author sees instead.
func validConstraintMatch(pattern string) bool {
	prefix, _ := strings.CutSuffix(pattern, "/**")
	if prefix == "" {
		return false
	}
	return !strings.ContainsAny(prefix, "*?[]{}")
}

func validToken(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
			return false
		}
	}
	return true
}
