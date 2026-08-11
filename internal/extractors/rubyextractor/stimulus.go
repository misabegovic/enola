package rubyextractor

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/enola-labs/enola/internal/facts"
)

// Stimulus bindings live in markup, not code: `data-controller="dropdown"`
// binds an element to app/javascript/controllers/dropdown_controller.js by
// naming convention, and `data-action="click->dropdown#toggle"` invokes a
// method on it. Neither is a call site any language parser sees, so the
// controller and its handlers read as unreached from every view that uses
// them. This pass records the binding as a fact WITHOUT pretending to resolve
// it: each declared controller identifier in an .html.erb file becomes one
// dependency-style fact at resolution_level "markup-declared", linked to the
// controller file only when the conventional path actually exists — otherwise
// the fact is name-only, a declared binding the graph could not ground. Never
// a guessed edge: an identifier that is not a plain Stimulus token (an ERB
// interpolation, a helper call) is skipped, and a missing controller file
// yields no target.
const stimulusResolutionLevel = "markup-declared"

// stimulusControllersDir is the conventional controller root the identifier
// maps into. Only the default layout is honored; an app that relocates its
// controllers gets name-only facts, which is the honest miss.
const stimulusControllersDir = "app/javascript/controllers"

var (
	// stimulusAttrRe captures a data-controller or data-action attribute value,
	// double- or single-quoted. Bounded on purpose: values are matched inside
	// one attribute literal, never across tags.
	stimulusAttrRe = regexp.MustCompile(`data-(controller|action)\s*=\s*(?:"([^"]*)"|'([^']*)')`)
	// stimulusIdentifierRe admits exactly the tokens Stimulus generates
	// identifiers from: lowercase segments joined by single dashes or
	// underscores, with `--` separating namespace segments. Anything else —
	// interpolated Ruby, a stray helper — fails closed.
	stimulusIdentifierRe = regexp.MustCompile(`^[a-z0-9]+(?:[-_][a-z0-9]+)*(?:--[a-z0-9]+(?:[-_][a-z0-9]+)*)*$`)
)

// extractStimulusBindings scans one .html.erb file for Stimulus markup
// bindings and emits one fact per declared controller identifier, sorted by
// identifier so the file's facts are a function of what it declares, not of
// attribute order.
func extractStimulusBindings(repoPath, relFile string, src []byte) []facts.Fact {
	if !strings.HasSuffix(strings.ToLower(relFile), ".html.erb") {
		return nil
	}
	declaredBy := map[string]map[string]bool{}
	declare := func(identifier, attr string) {
		if !stimulusIdentifierRe.MatchString(identifier) {
			return
		}
		if declaredBy[identifier] == nil {
			declaredBy[identifier] = map[string]bool{}
		}
		declaredBy[identifier][attr] = true
	}
	for _, m := range stimulusAttrRe.FindAllStringSubmatch(string(src), -1) {
		attr, value := "data-"+m[1], m[2]+m[3]
		// A value carrying embedded Ruby renders to something this pass cannot
		// know; the whole attribute is skipped, not just the interpolated token,
		// because the static remainder may not survive the rendering either.
		if strings.Contains(value, "<%") {
			continue
		}
		for _, token := range strings.Fields(value) {
			switch attr {
			case "data-controller":
				declare(token, attr)
			case "data-action":
				declare(stimulusActionController(token), attr)
			}
		}
	}

	identifiers := make([]string, 0, len(declaredBy))
	for identifier := range declaredBy {
		identifiers = append(identifiers, identifier)
	}
	sort.Strings(identifiers)

	var out []facts.Fact
	for _, identifier := range identifiers {
		attrs := make([]string, 0, len(declaredBy[identifier]))
		for attr := range declaredBy[identifier] {
			attrs = append(attrs, attr)
		}
		sort.Strings(attrs)
		fact := facts.Fact{
			Kind: facts.KindDependency,
			Name: fmt.Sprintf("stimulus-binding: %s -> %s", relFile, identifier),
			File: relFile,
			Props: map[string]any{
				"language":         "ruby",
				"framework":        "stimulus",
				"binding":          strings.Join(attrs, " "),
				"resolution_level": stimulusResolutionLevel,
			},
		}
		if target := stimulusControllerFile(repoPath, identifier); target != "" {
			fact.Relations = []facts.Relation{{Kind: facts.RelDependsOn, Target: target}}
		}
		out = append(out, fact)
	}
	return out
}

// stimulusActionController extracts the controller identifier from one
// data-action descriptor: `click->dropdown#toggle` names dropdown, and the
// event prefix is optional (`dropdown#toggle` is legal Stimulus). The returned
// token still passes through the identifier gate, so a descriptor this cannot
// parse declares nothing.
func stimulusActionController(descriptor string) string {
	rest := descriptor
	if idx := strings.LastIndex(rest, "->"); idx >= 0 {
		rest = rest[idx+2:]
	}
	controller, _, found := strings.Cut(rest, "#")
	if !found {
		return ""
	}
	return controller
}

// stimulusControllerFile maps an identifier to the conventional controller
// path and returns it only when that file exists: `--` separates directory
// segments and dashes become underscores, so `users--date-picker` maps to
// app/javascript/controllers/users/date_picker_controller.(js|ts). Both
// extensions are probed in fixed order, so the linked target never depends on
// directory iteration.
func stimulusControllerFile(repoPath, identifier string) string {
	segments := strings.Split(identifier, "--")
	for i, segment := range segments {
		segments[i] = strings.ReplaceAll(segment, "-", "_")
	}
	base := filepath.Join(segments...) + "_controller"
	for _, ext := range []string{".js", ".ts"} {
		rel := filepath.ToSlash(filepath.Join(stimulusControllersDir, base+ext))
		if _, err := os.Stat(filepath.Join(repoPath, filepath.FromSlash(rel))); err == nil {
			return rel
		}
	}
	return ""
}
