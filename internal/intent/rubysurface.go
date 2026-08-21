package intent

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"
	ruby "github.com/tree-sitter/tree-sitter-ruby/bindings/go"
)

// The Ruby declaration surface: the same laws, written the way the team that
// lives with them would say them.
//
// A declaration is read as data and never executed. The file is parsed with
// the Ruby grammar the extractors already carry, and only the constructs
// named here are understood; anything else is a problem citing its line, so a
// sentence that looks like a law but is not one can never pass silently.
//
// It compiles to exactly the declaration the YAML loader produces, so the
// evaluator, the provenance stamping, the lint surface and the pre-edit
// contract are the same for both spellings, and a repository can hold one of
// each while a team moves.
//
//	Enola.architecture "storefront" do
//	  rails
//	  part :maintenance, files: "app/tasks/**"
//
//	  law "background jobs never invoke controller code" do
//	    jobs.must_not_call controllers
//	    why "rendering from a job goes through ApplicationController.renderer"
//	  end
//	end

// railsParts is the vocabulary a `rails` line declares: the conventional parts
// of a Rails application, each selected by the directory Rails puts it in.
//
// They are path-selected rather than predicate-selected, and the validator is
// why. A component selected by a `where` predicate cannot sit on the far side
// of an edge rule: a predicate selects the facts carrying a property, and a
// class's calls ride its member facts, so an edge-walking rule resolves it
// against nothing. Declaring these by prop read better and would have made
// every edge law in the surface unwritable, which the first compile of the
// Rails example caught.
var railsParts = []struct{ part, files string }{
	{"models", "app/models/**"},
	{"controllers", "app/controllers/**"},
	{"jobs", "app/jobs/**"},
	{"mailers", "app/mailers/**"},
	{"policies", "app/policies/**"},
	{"serializers", "app/serializers/**"},
	{"components", "app/components/**"},
	{"channels", "app/channels/**"},
	{"helpers", "app/helpers/**"},
	{"concerns", "app/models/concerns/**"},
}

// edgeVerbs are the laws about edges: each names a rule form and the role its
// object fills.
var edgeVerbs = map[string]struct{ form, role string }{
	"must_not_call":      {"forbid", "to"},
	"must_not_reach":     {"forbid_reach", "to"},
	"may_only_call":      {"allow", "only"},
	"is_reached_only_by": {"protect", "owners"},
	"must_be_reached_by": {"require_edge", "to"},
	"stays_inside":       {"private", "except"},
	"must_follow":        {"protocol", "steps"},
}

// memberVerbs are the laws about a component's own members: no counterpart,
// each carrying its own argument shape.
var memberVerbs = map[string]string{
	"must_define":          "require_defines",
	"names_must_match":     "require_name",
	"names_must_not_match": "forbid_name",
	"must_be_empty":        "forbid_fact",
	"at_most":              "cap",
	"must_carry":           "require",
	"advises":              "guide",
}

// ParseRubySurface reads a Ruby declaration file into the same shape the YAML
// loader produces. Every problem it finds is returned, each citing its line,
// because a declaration file is read by a person and one error at a time is a
// poor way to fix five.
func ParseRubySurface(src []byte, path string) (ConstraintsFile, []string) {
	parser := sitter.NewParser()
	defer parser.Close()
	if err := parser.SetLanguage(sitter.NewLanguage(ruby.Language())); err != nil {
		return ConstraintsFile{}, []string{path + ": the Ruby grammar could not be loaded"}
	}
	tree := parser.Parse(src, nil)
	defer tree.Close()

	r := &surfaceReader{src: src, path: path, parts: map[string]bool{}}
	r.walkTop(tree.RootNode())
	sort.Strings(r.problems)
	return ConstraintsFile{Path: path, Components: r.components, Rules: r.rules}, r.problems
}

type surfaceReader struct {
	src        []byte
	path       string
	components []ConstraintComponent
	rules      []ConstraintRule
	parts      map[string]bool
	problems   []string
}

func (r *surfaceReader) fail(node *sitter.Node, format string, args ...any) {
	line := 0
	if node != nil {
		line = int(node.StartPosition().Row) + 1
	}
	r.problems = append(r.problems, fmt.Sprintf("%s:%d: %s", r.path, line, fmt.Sprintf(format, args...)))
}

func (r *surfaceReader) text(node *sitter.Node) string {
	if node == nil {
		return ""
	}
	return string(r.src[node.StartByte():node.EndByte()])
}

func (r *surfaceReader) walkTop(node *sitter.Node) {
	if node == nil {
		return
	}
	if node.Kind() == "call" && r.methodName(node) == "architecture" {
		if body := blockBody(node); body != nil {
			r.readDeclarations(body)
		}
		return
	}
	for i := uint(0); i < node.NamedChildCount(); i++ {
		r.walkTop(node.NamedChild(i))
	}
}

func (r *surfaceReader) readDeclarations(body *sitter.Node) {
	for i := uint(0); i < body.NamedChildCount(); i++ {
		r.readStatement(body.NamedChild(i))
	}
}

func (r *surfaceReader) readStatement(stmt *sitter.Node) {
	switch r.methodName(stmt) {
	case "rails":
		r.declareRailsParts()
	case "part":
		r.readPart(stmt)
	case "law":
		r.readLaw(stmt)
	default:
		r.fail(stmt, "%q is not a declaration; write part, rails or law", r.text(stmt))
	}
}

func (r *surfaceReader) declareRailsParts() {
	for _, p := range railsParts {
		if r.parts[p.part] {
			continue
		}
		r.parts[p.part] = true
		r.components = append(r.components, ConstraintComponent{
			Name:  componentToken(p.part),
			Match: []string{p.files},
		})
	}
}

func (r *surfaceReader) readPart(stmt *sitter.Node) {
	args := argumentNodes(stmt)
	if len(args) == 0 {
		r.fail(stmt, "part needs a name")
		return
	}
	name := r.symbolOrString(args[0])
	if name == "" {
		r.fail(stmt, "a part's name is a symbol or a string")
		return
	}
	component := ConstraintComponent{Name: componentToken(name)}
	for _, pair := range r.keywordPairs(args) {
		key := r.symbolOrString(pair.key)
		switch key {
		case "files":
			component.Match = append(component.Match, r.stringList(pair.value)...)
		case "kind":
			component.Kind = r.symbolOrString(pair.value)
		case "service":
			component.Service = r.symbolOrString(pair.value)
		case "named":
			component.NamePattern = r.symbolOrString(pair.value)
		case "where":
			component.Where = r.hash(pair.value)
		default:
			r.fail(pair.key, "a part takes files, kind, service, named or where, not %q", key)
		}
	}
	if len(component.Match) == 0 && component.Where == nil && component.NamePattern == "" {
		r.fail(stmt, "part %q selects nothing: give it files, where or named", name)
		return
	}
	r.parts[name] = true
	r.components = append(r.components, component)
}

func (r *surfaceReader) readLaw(stmt *sitter.Node) {
	args := argumentNodes(stmt)
	body := blockBody(stmt)
	sentence := ""
	if len(args) > 0 {
		sentence = r.symbolOrString(args[0])
	}
	if sentence == "" || body == nil {
		r.fail(stmt, "a law is a sentence and a block")
		return
	}
	rule := ConstraintRule{ID: slug(sentence), Because: sentence}
	stated := false
	for i := uint(0); i < body.NamedChildCount(); i++ {
		r.readLawLine(body.NamedChild(i), &rule, &stated)
	}
	if !stated {
		r.fail(stmt, "law %q says nothing: give it a sentence like jobs.must_not_call controllers", sentence)
		return
	}
	r.rules = append(r.rules, rule)
}

func (r *surfaceReader) readLawLine(line *sitter.Node, rule *ConstraintRule, stated *bool) {
	method := r.methodName(line)
	args := argumentNodes(line)
	switch method {
	case "why", "because":
		if text := r.symbolOrString(firstOrNil(args)); text != "" {
			rule.Because = text
			return
		}
		r.fail(line, "%s takes the reason as a string", method)
		return
	case "seen_in":
		if text := r.symbolOrString(firstOrNil(args)); text != "" {
			rule.Because = strings.TrimSpace(rule.Because) + " (" + text + ")"
			return
		}
		r.fail(line, "seen_in takes the measurement as a string")
		return
	case "mode":
		rule.Mode = r.symbolOrString(firstOrNil(args))
		return
	case "via":
		rule.Via = r.symbolOrString(firstOrNil(args))
		return
	case "exempt":
		r.readExemption(line, rule, args)
		return
	}
	if receiver := r.receiverName(line); receiver != "" {
		r.readSubjectLine(line, receiver, method, args, rule, stated)
		return
	}
	r.fail(line, "%q is not part of a law", r.text(line))
}

func (r *surfaceReader) readSubjectLine(line *sitter.Node, subject, verb string, args []*sitter.Node, rule *ConstraintRule, stated *bool) {
	if *stated {
		r.fail(line, "a law states one thing; write a second law for the rest")
		return
	}
	if !r.parts[subject] {
		r.fail(line, "%q is not a part; declare it with part or rails first", subject)
		return
	}
	if edge, ok := edgeVerbs[verb]; ok {
		targets := r.partList(args)
		if len(targets) == 0 && edge.form != "private" {
			r.fail(line, "%s names the part on the other side", verb)
			return
		}
		setForm(rule, edge.form, componentToken(subject))
		for i, target := range targets {
			targets[i] = componentToken(target)
		}
		switch edge.role {
		case "to":
			rule.To = firstString(targets)
		case "only":
			rule.Only = targets
		case "owners":
			rule.Owners = targets
		case "except":
			rule.Except = targets
		case "steps":
			rule.Steps = targets
		}
		if rule.Via == "" && formNeedsVia(edge.form) {
			rule.Via = "calls"
		}
		*stated = true
		return
	}
	form, ok := memberVerbs[verb]
	if !ok {
		r.fail(line, "%q is not something a part can be told to do", verb)
		return
	}
	setForm(rule, form, componentToken(subject))
	r.readMemberArguments(line, form, args, rule)
	*stated = true
}

func (r *surfaceReader) readMemberArguments(line *sitter.Node, form string, args []*sitter.Node, rule *ConstraintRule) {
	first := r.symbolOrString(firstOrNil(args))
	switch form {
	case "require_defines":
		rule.Method = first
	case "require_name", "forbid_name":
		rule.Pattern = first
		for _, pair := range r.keywordPairs(args) {
			if r.symbolOrString(pair.key) == "surface" {
				rule.Surface = r.symbolOrString(pair.value)
			}
		}
	case "cap":
		if n, err := strconv.Atoi(strings.TrimSpace(first)); err == nil {
			rule.MaxMembers = n
		} else {
			r.fail(line, "at_most takes a number")
		}
	case "guide":
		rule.Message = first
	case "require":
		match := &PropMatch{}
		for _, pair := range r.keywordPairs(args) {
			switch r.symbolOrString(pair.key) {
			case "prop":
				match.Prop = r.symbolOrString(pair.value)
			case "value":
				match.Value = r.symbolOrString(pair.value)
			}
		}
		if match.Prop == "" || match.Value == "" {
			r.fail(line, "must_carry names the prop and the value it must contain")
			return
		}
		rule.MustPropContain = match
	}
}

func (r *surfaceReader) readExemption(line *sitter.Node, rule *ConstraintRule, args []*sitter.Node) {
	exemption := ConstraintExemption{Witness: r.symbolOrString(firstOrNil(args))}
	for _, pair := range r.keywordPairs(args) {
		switch r.symbolOrString(pair.key) {
		case "because":
			exemption.Because = r.symbolOrString(pair.value)
		case "owner":
			exemption.Owner = r.symbolOrString(pair.value)
		case "since":
			exemption.Since = r.symbolOrString(pair.value)
		}
	}
	if exemption.Witness == "" || exemption.Because == "" {
		r.fail(line, "an exemption names what it carves out and why")
		return
	}
	rule.Exempt = append(rule.Exempt, exemption)
}

func setForm(rule *ConstraintRule, form, subject string) {
	switch form {
	case "forbid":
		rule.Forbid = subject
	case "forbid_reach":
		rule.ForbidReach = subject
	case "allow":
		rule.Allow = subject
	case "protect":
		rule.Protect = subject
	case "private":
		rule.Private = subject
	case "forbid_fact":
		rule.ForbidFact = subject
	case "cap":
		rule.Cap = subject
	case "require":
		rule.Require = subject
	case "require_edge":
		rule.RequireEdge = subject
	case "require_defines":
		rule.RequireDefines = subject
	case "require_name":
		rule.RequireName = subject
	case "forbid_name":
		rule.ForbidName = subject
	case "protocol":
		rule.Protocol = subject
	case "guide":
		rule.Guide = subject
	}
}

func formNeedsVia(form string) bool {
	switch form {
	case "forbid", "forbid_reach", "allow", "protect", "require_edge", "protocol":
		return true
	}
	return false
}

// componentToken is a part's name as the declaration vocabulary spells it.
// Ruby names a part in snake_case because that is what a Ruby file reads
// like; a component name is a lowercase token, so the underscore becomes a
// dash on the way through and the sentence and the compiled law stay the
// same thing said twice.
func componentToken(part string) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(part)), "_", "-")
}

// slug turns a law's sentence into the lowercase-token id every finding
// carries, so the id is derived from the sentence rather than repeated beside
// it.
func slug(sentence string) string {
	var b strings.Builder
	lastDash := true
	for _, c := range strings.ToLower(sentence) {
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
			b.WriteRune(c)
			lastDash = false
		default:
			if !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}
