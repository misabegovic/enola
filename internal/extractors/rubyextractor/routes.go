package rubyextractor

import (
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/enola-labs/enola/internal/extractors/extcoverage"
	"github.com/enola-labs/enola/internal/facts"
)

// extractAllRoutes finds and parses all Rails route files in the repository.
//
// Rails commonly splits routes across config/routes/<pkg>.rb files pulled in by
// draw(:pkg) from config/routes.rb, often inside a scope('/api')/namespace(:vN)
// block. Parsed standalone, a delegated file loses that prefix, so its routes read
// as "/devices" instead of "/api/v2/devices" and no longer match a client call. To
// avoid that, the top-level config/routes.rb is parsed first to learn each
// delegation's prefix, then each delegated file is parsed seeded with it.
func extractAllRoutes(repoPath string, files []string) []facts.Fact {
	// Collect route files: config/routes.rb, config/routes/*.rb, packages/*/config/routes/*.rb
	var routeFiles []string
	for _, relFile := range files {
		if isRubyFile(relFile) && isRouteFile(relFile) {
			routeFiles = append(routeFiles, relFile)
		}
	}

	readSrc := func(relFile string) ([]byte, bool) {
		src, err := os.ReadFile(filepath.Join(repoPath, relFile))
		if err != nil {
			log.Printf("[ruby-extractor] error reading route file %s: %v", relFile, err)
			return nil, false
		}
		return src, true
	}

	mainFile := filepath.Join("config", "routes.rb")
	jsonapiFormat, refusalCause := jsonapiRouteFormat(repoPath, files)
	resolver := newJsonapiResolver(repoPath, files)
	// Hop 4 names a controller by the route name its resource was declared with,
	// so every declaration has to be known before any handler is emitted. That is
	// the only reason this walk is two passes.
	for _, relFile := range routeFiles {
		if src, ok := readSrc(relFile); ok {
			collectJsonapiDeclarations(src, resolver)
		}
	}

	// Pass 1: parse the top-level routes.rb, learning draw(:pkg) -> prefix. Each
	// draw(:pkg) loads config/routes/<pkg>.rb (Rails convention).
	var allFacts []facts.Fact
	drawPrefix := map[string]string{}
	unresolvedMacros := map[string]int{}
	for _, relFile := range routeFiles {
		if relFile != mainFile {
			continue
		}
		if src, ok := readSrc(relFile); ok {
			ff, draws, unhandled := parseRouteFile(src, relFile, "", jsonapiFormat, resolver, refusalCause)
			mergeCounts(unresolvedMacros, unhandled)
			allFacts = append(allFacts, ff...)
			for pkg, prefix := range draws {
				drawPrefix[filepath.Join("config", "routes", pkg+".rb")] = prefix
			}
		}
	}

	// Pass 2: parse the remaining route files, seeding any prefix learned in pass 1.
	for _, relFile := range routeFiles {
		if relFile == mainFile {
			continue
		}
		if src, ok := readSrc(relFile); ok {
			ff, _, unhandled := parseRouteFile(src, relFile, drawPrefix[relFile], jsonapiFormat, resolver, refusalCause)
			mergeCounts(unresolvedMacros, unhandled)
			allFacts = append(allFacts, ff...)
		}
	}

	if fact, ok := routeCoverageFact(repoPath, len(allFacts), unresolvedMacros); ok {
		allFacts = append(allFacts, fact)
	}
	return allFacts
}

func mergeCounts(into, from map[string]int) {
	for name, n := range from {
		into[name] += n
	}
}

// routeCoverageFact accounts for the route macros this extractor read and the
// ones it could not, in the `edge_coverage` shape the cross-repo layer already
// uses — because coverage in this codebase travels as facts.
//
// It emits nothing when there were no route files at all: an extractor that had
// nothing to look at must not report a confident zero, which is the exact
// failure this fact exists to prevent one level down.
func routeCoverageFact(repoPath string, resolved int, unresolved map[string]int) (facts.Fact, bool) {
	return extcoverage.Fact(repoPath, "ruby:routes", "rails_route_macro", resolved, unresolved)
}

// associationCoverageFact accounts for the associations this extractor read and
// the ones whose target it could not name.
func associationCoverageFact(repoPath string, resolved int, unresolved map[string]int) (facts.Fact, bool) {
	return extcoverage.Fact(repoPath, "ruby:associations", "rails_association", resolved, unresolved)
}

// callCoverageFact accounts for call edges whose target names a symbol this
// extractor emitted, against those that name something it never did.
//
// 71% of the monolith's 218,263 call edges point at a name that is not a known
// symbol — 37,913 distinct names, led by `params`, `include` and `company`.
// Those are not all misses: many are Rails DSL or local variables that were
// never calls. But until now they vanished, and a vanished edge is
// indistinguishable from one that was never there, which is the shape this
// estate has spent a day removing everywhere else.
//
// The unresolved set is counted, not materialised. Turning 37,913 names into
// nodes would put `params` in the graph as a symbol, and a node nobody can
// follow is the same defect as an edge nobody can follow — the association ADR
// settled that once and this does not reopen it.
func callCoverageFact(repoPath string, resolved int, unresolved map[string]int) (facts.Fact, bool) {
	return extcoverage.Fact(repoPath, "ruby:calls", "ruby_call", resolved, unresolved)
}

// isRouteFile returns true if the file path looks like a Rails route file.
func isRouteFile(relFile string) bool {
	// config/routes.rb
	if relFile == filepath.Join("config", "routes.rb") {
		return true
	}
	// config/routes/*.rb
	if strings.HasPrefix(relFile, filepath.Join("config", "routes")+string(filepath.Separator)) {
		return true
	}
	// packages/*/config/routes/*.rb (packwerk pattern)
	parts := strings.Split(filepath.ToSlash(relFile), "/")
	for i := 0; i+3 < len(parts); i++ {
		if parts[i] == "packages" && parts[i+2] == "config" && parts[i+3] == "routes" {
			return true
		}
	}
	return false
}

// routeScope tracks the current route scope prefix.
type routeScope struct {
	pathPrefix string
	module     string
	// memberParam is the parent member path parameter (`:<singular>_id`) that nested
	// resources declared inside a *plural* `resources` block must nest under; empty for
	// namespace/scope/singular-resource scopes, which add no member id to their children.
	memberParam string
	// shallow records that an enclosing `resources` declared shallow: true.
	// Rails scopes the flag lexically to everything nested inside, so it has to
	// travel down the stack rather than be read off each call.
	shallow bool
	// resourceName is the controller this resource's routes are served by, so a
	// bare verb declared inside its block can name one too.
	resourceName string
	// explicitController records a `controller:` override, which answers the
	// pluralization question a singular resource otherwise cannot.
	explicitController string
	// singularOwner marks a scope pushed by `resource` rather than `resources`.
	// A singular resource has no member id, so `on: :member` inside it addresses
	// the bare path — Rails serves /profile/settings, not /profile/:id/settings.
	singularOwner bool
	// ownParam is what this resource calls its own member segment — `:id` unless
	// the declaration renamed it with `param:`. It is what `on: :member` and a
	// `member` block address, which is a different question from memberParam:
	// that one is what CHILDREN nest under.
	ownParam string
	// dropParentMember marks a scope that suppresses the enclosing resource's
	// member param: `member`/`collection` blocks address the resource itself
	// rather than something nested under it.
	dropParentMember bool
}

// buildPrefix constructs the current URL prefix from the scope stack,
// materializing each resource's member param as it goes.
//
// The member param has to live here rather than be read off the innermost
// scope by each caller, because anything may sit between a parent resource and
// what it nests: `resources :companies do namespace :api do resources :sections
// end end` is served at /companies/:company_id/api/sections. Reading the param
// from the top of the stack loses it the moment a namespace or scope
// intervenes — which is the shape a real monolith is full of.
func buildPrefix(stack []routeScope) string {
	var b strings.Builder
	for i, s := range stack {
		b.WriteString(s.pathPrefix)
		if s.memberParam == "" {
			continue
		}
		if i+1 < len(stack) && stack[i+1].dropParentMember {
			continue
		}
		b.WriteString("/:" + s.memberParam)
	}
	return b.String()
}

// modulePath is the controller namespace the scope stack has accumulated —
// `scope module: "api"` inside `namespace :v1` is "api/v1". It is the directory
// a resource class lives in, and the prefix of every controller serving it.
func modulePath(stack []routeScope) string {
	var parts []string
	for _, s := range stack {
		if s.module != "" {
			parts = append(parts, s.module)
		}
	}
	return strings.Join(parts, "/")
}

// collectionPrefix is the path of the innermost resource itself rather than of
// something nested under it: what `on: :collection` and a `collection` block
// address.
func collectionPrefix(stack []routeScope) string {
	if len(stack) == 0 {
		return ""
	}
	trimmed := append([]routeScope{}, stack...)
	trimmed[len(trimmed)-1].memberParam = ""
	return buildPrefix(trimmed)
}

// actionFilter is an only:/except: declaration, carrying whether each was
// WRITTEN as well as what it named. An empty declaration is not an absent one.
type actionFilter struct {
	only        map[string]bool
	except      map[string]bool
	onlyGiven   bool
	exceptGiven bool
}

// apply narrows a resource's action set. only: wins over except: when both are
// written, matching Rails.
func (f actionFilter) apply(all []restAction) []restAction {
	if f.onlyGiven {
		return filterActions(all, f.only, true)
	}
	if f.exceptGiven {
		return filterActions(all, f.except, false)
	}
	return all
}

// memberSegment is what `on: :member` and a `member` block add to the innermost
// resource's path: its member param, or nothing at all when that resource is
// singular.
func memberSegment(stack []routeScope) string {
	for i := len(stack) - 1; i >= 0; i-- {
		if stack[i].ownParam == "" && !stack[i].singularOwner {
			continue
		}
		if stack[i].singularOwner {
			return ""
		}
		return "/:" + stack[i].ownParam
	}
	return "/:id"
}

// restAction describes a single RESTful action.
type restAction struct {
	name   string
	method string
	suffix string
}

// restfulActions returns the set of REST actions for a resources declaration,
// honoring only:/except: filters parsed from the declaration's arguments.
func restfulActions(filter actionFilter) []restAction {
	all := []restAction{
		{name: "index", method: "GET", suffix: ""},
		{name: "create", method: "POST", suffix: ""},
		{name: "new", method: "GET", suffix: "/new"},
		{name: "show", method: "GET", suffix: "/:id"},
		// Rails routes BOTH PATCH and PUT to the update action, so emit both — a
		// client calling either verb must resolve to the same served endpoint.
		{name: "update", method: "PATCH", suffix: "/:id"},
		{name: "update", method: "PUT", suffix: "/:id"},
		{name: "edit", method: "GET", suffix: "/:id/edit"},
		{name: "destroy", method: "DELETE", suffix: "/:id"},
	}

	return filter.apply(all)
}

// restfulActionsSingular returns the REST actions for a singular `resource`
// declaration. A singular resource has no index and no `:id` member segment — every
// action acts on the single resource at its base path.
func restfulActionsSingular(filter actionFilter) []restAction {
	all := []restAction{
		{name: "create", method: "POST", suffix: ""},
		{name: "new", method: "GET", suffix: "/new"},
		{name: "show", method: "GET", suffix: ""},
		// Rails routes BOTH PATCH and PUT to update (see restfulActions).
		{name: "update", method: "PATCH", suffix: ""},
		{name: "update", method: "PUT", suffix: ""},
		{name: "edit", method: "GET", suffix: "/edit"},
		{name: "destroy", method: "DELETE", suffix: ""},
	}

	return filter.apply(all)
}

// JSONAPI::Resources rewrites each resource's URL segment through a route
// formatter, so `jsonapi_resources :career_sites` serves /career-sites rather
// than /career_sites. The formatter is repo configuration, and a repo that
// installs its own is running Ruby this extractor cannot read.
const (
	jsonapiRouteDasherized  = "dasherized"
	jsonapiRouteUnderscored = "underscored"
	jsonapiRouteUnknown     = "unknown"
)

var (
	jsonapiRouteFormatPattern = regexp.MustCompile(`route_format\s*=\s*(\S+)`)
	formatterDeclaration      = regexp.MustCompile(`class\s+(\w*)RouteFormatter\s*<\s*([\w:]+)`)
	formatOverride            = regexp.MustCompile(`(?m)^\s*def\s+(self\.)?format\b`)
)

// Refusal causes. A count with no cause reads as "this extractor cannot expand
// jsonapi macros", which was true until today and is now false everywhere
// except the repositories that configure their own formatter. Naming what was
// found turns a suspected extractor limitation into a located line of
// configuration.
const (
	refusalFormatterOverrides = "route_formatter_overrides_format"
	refusalFormatterUnknown   = "route_formatter_unknown"
)

// jsonapiRouteFormat reports how a repository formats JSONAPI::Resources route
// segments. An unconfigured repository gets the gem's own default, which is
// dasherized; a repository naming a formatter this extractor does not recognise
// gets jsonapiRouteUnknown, on which the declarations are counted rather than
// expanded — the segment is unknowable without running the formatter, and a
// plausible-looking wrong path is worse than a counted miss.
func jsonapiRouteFormat(repoPath string, files []string) (string, string) {
	for _, relFile := range files {
		if filepath.Dir(relFile) != filepath.Join("config", "initializers") || !isRubyFile(relFile) {
			continue
		}
		src, err := os.ReadFile(filepath.Join(repoPath, relFile))
		if err != nil {
			continue
		}
		m := jsonapiRouteFormatPattern.FindSubmatch(src)
		if m == nil {
			continue
		}
		switch strings.Trim(string(m[1]), `":`) {
		case "dasherized_route", "Dasherized":
			return jsonapiRouteDasherized, ""
		case "underscored_route", "Underscored":
			return jsonapiRouteUnderscored, ""
		}
		return classifyFormatter(src)
	}
	return jsonapiRouteDasherized, ""
}

// classifyFormatter reads what the repository's own formatter class says about
// itself. A subclass that never overrides the formatting method formats exactly
// as its parent does — that is what inheritance means, and reading the class
// body is reading the source rather than reasoning about it. A subclass that
// does override is running Ruby this extractor cannot, and no amount of reading
// the override changes that: `aboard`'s is provably harmless for resource
// segments and there is nothing in that repository able to check the result.
func classifyFormatter(src []byte) (string, string) {
	declaration := formatterDeclaration.FindSubmatch(src)
	if declaration == nil {
		return jsonapiRouteUnknown, refusalFormatterUnknown
	}
	parent := parentFormatterFormat(string(declaration[2]))
	if parent == "" {
		return jsonapiRouteUnknown, refusalFormatterUnknown
	}
	if formatOverride.Match(src) {
		return jsonapiRouteUnknown, refusalFormatterOverrides
	}
	return parent, ""
}

func parentFormatterFormat(superclass string) string {
	switch strings.TrimPrefix(superclass, "::") {
	case "DasherizedRouteFormatter", "JSONAPI::DasherizedRouteFormatter":
		return jsonapiRouteDasherized
	case "UnderscoredRouteFormatter", "JSONAPI::UnderscoredRouteFormatter":
		return jsonapiRouteUnderscored
	}
	return ""
}

// jsonapiSegment formats a resource name as the URL segment the gem serves it
// under.
func jsonapiSegment(name, format string) string {
	if format == jsonapiRouteUnderscored {
		return name
	}
	return strings.ReplaceAll(name, "_", "-")
}

// jsonapiRestfulActions returns the REST actions a `jsonapi_resources`
// declaration serves. JSONAPI::Resources is an API-only gem: it serves no `new`
// and no `edit`, since both exist in Rails only to render HTML forms. Reusing
// restfulActions here would fabricate two routes per declaration.
func jsonapiRestfulActions(filter actionFilter) []restAction {
	all := []restAction{
		{name: "index", method: "GET", suffix: ""},
		{name: "create", method: "POST", suffix: ""},
		{name: "show", method: "GET", suffix: "/:id"},
		// PATCH and PUT both reach update, as in restfulActions.
		{name: "update", method: "PATCH", suffix: "/:id"},
		{name: "update", method: "PUT", suffix: "/:id"},
		{name: "destroy", method: "DELETE", suffix: "/:id"},
	}

	return filter.apply(all)
}

// jsonapiRestfulActionsSingular returns the REST actions a singular
// `jsonapi_resource` declaration serves: no index, no `:id` member segment, and
// — as with the plural form — no new and no edit.
func jsonapiRestfulActionsSingular(filter actionFilter) []restAction {
	all := []restAction{
		{name: "create", method: "POST", suffix: ""},
		{name: "show", method: "GET", suffix: ""},
		{name: "update", method: "PATCH", suffix: ""},
		{name: "update", method: "PUT", suffix: ""},
		{name: "destroy", method: "DELETE", suffix: ""},
	}

	return filter.apply(all)
}

// filterActions returns actions filtered by an allow or deny list.
func filterActions(all []restAction, names map[string]bool, isAllow bool) []restAction {
	var result []restAction
	for _, a := range all {
		if isAllow {
			if names[a.name] {
				result = append(result, a)
			}
		} else {
			if !names[a.name] {
				result = append(result, a)
			}
		}
	}
	return result
}
