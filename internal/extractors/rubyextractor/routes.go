package rubyextractor

import (
	"log"
	"os"
	"path/filepath"
	"strings"

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

	// Pass 1: parse the top-level routes.rb, learning draw(:pkg) -> prefix. Each
	// draw(:pkg) loads config/routes/<pkg>.rb (Rails convention).
	var allFacts []facts.Fact
	drawPrefix := map[string]string{}
	for _, relFile := range routeFiles {
		if relFile != mainFile {
			continue
		}
		if src, ok := readSrc(relFile); ok {
			ff, draws := parseRouteFile(src, relFile, "")
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
			ff, _ := parseRouteFile(src, relFile, drawPrefix[relFile])
			allFacts = append(allFacts, ff...)
		}
	}

	return allFacts
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

// restAction describes a single RESTful action.
type restAction struct {
	name   string
	method string
	suffix string
}

// restfulActions returns the set of REST actions for a resources declaration,
// honoring only:/except: filters parsed from the declaration's arguments.
func restfulActions(only, except map[string]bool) []restAction {
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

	if len(only) > 0 {
		return filterActions(all, only, true)
	}
	if len(except) > 0 {
		return filterActions(all, except, false)
	}
	return all
}

// restfulActionsSingular returns the REST actions for a singular `resource`
// declaration. A singular resource has no index and no `:id` member segment — every
// action acts on the single resource at its base path.
func restfulActionsSingular(only, except map[string]bool) []restAction {
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

	if len(only) > 0 {
		return filterActions(all, only, true)
	}
	if len(except) > 0 {
		return filterActions(all, except, false)
	}
	return all
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
