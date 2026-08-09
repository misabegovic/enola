package rubyextractor

import (
	"path/filepath"
	"strings"

	"github.com/enola-labs/enola/internal/facts"
	sitter "github.com/tree-sitter/go-tree-sitter"
	ruby "github.com/tree-sitter/tree-sitter-ruby/bindings/go"
)

// parseRouteFileAST parses a Rails route file with tree-sitter and emits KindRoute
// facts. Block boundaries come from the grammar (do_block) rather than counting
// `do`/`end`, so nested namespaces/resources/scopes are tracked precisely.
func parseRouteFileAST(src []byte, relFile string) []facts.Fact {
	ff, _, _ := parseRouteFile(src, relFile, "", jsonapiRouteDasherized, nil, "")
	return ff
}

// parseRouteFile parses a Rails route file, seeding the scope stack with
// initialPrefix (the URL prefix a parent routes.rb delegated this file under via
// draw(:pkg)), and additionally returns the draw(:pkg) -> prefix map discovered in
// this file, so the caller can inline each delegated file under its real scope.
func parseRouteFile(src []byte, relFile, initialPrefix, jsonapiFormat string, resolver *jsonapiResolver, refusalCause string) ([]facts.Fact, map[string]string, map[string]int) {
	parser := sitter.NewParser()
	defer parser.Close()
	if err := parser.SetLanguage(sitter.NewLanguage(ruby.Language())); err != nil {
		return nil, nil, nil
	}
	tree := parser.Parse(src, nil)
	defer tree.Close()

	var stack []routeScope
	if initialPrefix != "" {
		stack = []routeScope{{pathPrefix: initialPrefix}}
	}
	rw := &routeWalker{src: src, relFile: relFile, dir: filepath.Dir(relFile),
		draws: map[string]string{}, concerns: map[string]*sitter.Node{},
		unhandled: map[string]int{}, jsonapiFormat: jsonapiFormat,
		jsonapi: resolver, refusalCause: refusalCause}
	rw.walk(tree.RootNode(), stack)
	return rw.out, rw.draws, rw.unhandled
}

type routeWalker struct {
	src     []byte
	relFile string
	dir     string
	out     []facts.Fact
	// draws maps each draw(:pkg) delegation found in this file to the URL prefix it
	// is scoped under, so the caller can parse config/routes/<pkg>.rb with it.
	draws map[string]string
	// concerns holds each `concern :name do ... end` body so a later
	// `concerns: :name` can replay it in the scope that includes it. Rails
	// requires the definition to precede its use, so a single forward pass is
	// enough and a missing name resolves to nothing rather than to a guess.
	concerns map[string]*sitter.Node
	// unhandled counts route-declaring macros this walker does not know, keyed by
	// macro name. `jsonapi_resources :companies` declares routes and produces
	// none here; counting it is what stops that absence reading as "no routes".
	unhandled map[string]int
	// jsonapiFormat is how this repository formats JSONAPI::Resources route
	// segments, which decides whether a jsonapi declaration can be expanded at
	// all — see jsonapiRouteFormat.
	jsonapiFormat string
	// refusalCause names why a jsonapi declaration could not be expanded, when
	// the reason is the repository's configuration rather than the macro itself.
	refusalCause string
	// jsonapi resolves a declaration to its resource class, that class's
	// relationships, and the controller each related route is served by.
	jsonapi *jsonapiResolver
}

// routeWrappers are macros that take a block and *contain* routes rather than
// declaring them. The walker descends into every one, so their contents are
// extracted and they are not misses. Listing them explicitly keeps the
// unhandled tally about macros whose routes are genuinely lost.
var routeWrappers = map[string]bool{
	"constraints": true, "authenticate": true, "authenticated": true,
	"unauthenticated": true, "defaults": true, "with_options": true,
	"direct": true, "resolve": true, "devise_for": true,
	"devise_scope": true, "as": true, "shallow": true, "expose": true,
	// Modifiers that take symbols but declare nothing. Doorkeeper's
	// skip_controllers *removes* routes; counting it as unread would inflate the
	// tally with a macro that resolving could not gain anything from.
	"skip_controllers": true, "skip_authorization": true,
}

// walk iterates the statements of a program / body_statement, dispatching each
// route-DSL call with the current scope stack.
func (rw *routeWalker) walk(node *sitter.Node, stack []routeScope) {
	if node == nil {
		return
	}
	for i := uint(0); i < node.ChildCount(); i++ {
		c := node.Child(i)
		if c.Kind() == "call" {
			rw.handleCall(c, stack)
		}
	}
}

// blockBody returns the body_statement of a call's do/brace block, or nil.
func blockBody(call *sitter.Node) *sitter.Node {
	block := call.ChildByFieldName("block")
	if block == nil {
		return nil
	}
	return block.ChildByFieldName("body")
}

func (rw *routeWalker) handleCall(call *sitter.Node, stack []routeScope) {
	method := rubyText(call.ChildByFieldName("method"), rw.src)
	args := call.ChildByFieldName("arguments")
	body := blockBody(call)
	prefix := buildPrefix(stack)

	switch method {
	case "get", "post", "put", "patch", "delete":
		// Accept both a string path ('cities_by_zip') and a bare symbol (:cities_by_zip);
		// the positional helper also avoids picking up a `to:` handler string.
		path := firstPositionalPath(args, rw.src)
		// `get :settings, path: "verify_new_email/:token"` renames the segment the
		// action is served at. Reading only the action name emits a path the app
		// does not serve AND misses the one it does — one declaration reported as
		// two defects, which is the shape this comparison keeps finding.
		if override, present := pairStringPresent(args, "path", rw.src); present {
			path = override
		}
		if path == "" {
			return
		}
		if !strings.HasPrefix(path, "/") {
			path = "/" + path
		}
		// A bare verb directly inside a plural `resources` block nests under the
		// parent member id (Rails serves `resources :steps do get :status end` at
		// /steps/:step_id/status), as does `on: :member`; only `on: :collection`
		// stays at the collection path. Explicit member/collection blocks push
		// their own scope, whose memberParam is empty, so nothing doubles.
		// Rails distinguishes three placements inside a `resources` block and
		// they do not agree on the parameter name. A bare verb nests under the
		// parent member id (`/steps/:step_id/status`), which buildPrefix has
		// already supplied; `on: :member` addresses the resource itself and
		// uses `:id` (`/steps/:id/audit`); `on: :collection` stays at the
		// collection path.
		switch pairSymbol(args, "on", rw.src) {
		case "collection":
			prefix = collectionPrefix(stack)
		case "member":
			prefix = collectionPrefix(stack) + memberSegment(stack)
		}
		props := map[string]any{
			"method":    strings.ToUpper(method),
			"framework": "rails",
			"language":  "ruby",
		}
		if handler := pairString(args, "to", rw.src); handler != "" {
			props["handler"] = qualifyHandler(handler, modulePath(stack))
		} else if controller := enclosingController(stack); controller != "" {
			// A bare verb inside a resource block is served by that resource's
			// controller, with the verb's own name as the action.
			action := strings.TrimPrefix(path, "/")
			if idx := strings.Index(action, "/"); idx >= 0 {
				action = action[:idx]
			}
			if act := pairString(args, "action", rw.src); act != "" {
				action = act
			}
			if action != "" && !strings.Contains(action, ":") {
				props["handler"] = qualifyHandler(controller, modulePath(stack)) + "#" + action
			}
		}
		// A Rails optional segment `foo(/:bar)` serves two paths; emit both.
		for _, p := range expandOptionalSegments(path) {
			rw.emit(prefix+p, line(call), props)
		}

	case "mount":
		// `mount Sidekiq::Web => "/sidekiq"` declares an endpoint: the app serves
		// that path and hands it to a Rack application. It was listed as a
		// container the walker descends into, which is what `namespace` is — but
		// mount takes no block of routes, so descending found nothing and the
		// path was never emitted. The runtime table records these with
		// endpoint_kind=rack, which is how the miss became visible.
		mounted := mountPath(args, rw.src)
		if mounted == "" {
			rw.unhandled[method]++
			return
		}
		if !strings.HasPrefix(mounted, "/") {
			mounted = "/" + mounted
		}
		rw.emit(prefix+mounted, line(call), map[string]any{
			"method":    "ANY",
			"framework": "rails",
			"language":  "ruby",
			"mounted":   true,
		})

	case "root":
		handler := pairString(args, "to", rw.src)
		if handler == "" {
			handler = firstStringArg(args, rw.src)
		}
		props := map[string]any{
			"method":    "GET",
			"framework": "rails",
			"language":  "ruby",
		}
		if handler != "" {
			props["handler"] = qualifyHandler(handler, modulePath(stack))
		}
		rw.emit(prefix+"/", line(call), props)

	case "resources", "resource", "jsonapi_resources", "jsonapi_resource":
		name := firstSymbolArg(args, rw.src)
		if name == "" {
			return
		}
		only, onlyGiven := pairSymbolsPresent(args, "only", rw.src)
		except, exceptGiven := pairSymbolsPresent(args, "except", rw.src)
		singular := method == "resource" || method == "jsonapi_resource"
		jsonapi := method == "jsonapi_resources" || method == "jsonapi_resource"
		if jsonapi && rw.jsonapiFormat == jsonapiRouteUnknown {
			// A repository-supplied route formatter decides the URL segment, and
			// reading Ruby to find out what it decides is guessing. Count the
			// declaration against the cause rather than the macro: "29 unread
			// jsonapi_resources" reads as an extractor limitation, and this is a
			// located line of configuration.
			cause := rw.refusalCause
			if cause == "" {
				cause = method
			}
			rw.unhandled[cause]++
			return
		}

		// A resource nested inside a *plural* `resources` block nests under the parent
		// member (`/widgets/:widget_id/...`); the parent supplies that param via the
		// enclosing scope's memberParam. parentScopePrefix is the parent resource's own
		// path segment, needed to compute the shallow path below.
		// buildPrefix has already materialized the enclosing resource's member
		// param, so the segment must not repeat it. parentNesting is what a
		// shallow member route strips back off.
		parentNesting := ""
		if len(stack) > 0 {
			if p := stack[len(stack)-1].memberParam; p != "" {
				parentNesting = stack[len(stack)-1].pathPrefix + "/:" + p
			}
		}
		// `path:` overrides the URL segment while the resource name still drives the
		// props and the nested member param (Rails derives `:name_id` from the name).
		segmentName := name
		if jsonapi {
			segmentName = jsonapiSegment(name, rw.jsonapiFormat)
		}
		if p := pairString(args, "path", rw.src); p != "" {
			segmentName = strings.Trim(p, "/")
		}
		segment := "/" + segmentName
		resourcePath := prefix + segment

		// Rails `shallow: true` serves a nested plural resource's MEMBER routes
		// (show/edit/update/destroy) at a shallow path — the parent resource segment
		// and its member param are dropped — while the collection routes
		// (index/create/new) stay nested. shallowBase is the prefix with the parent
		// resource segment stripped, plus this resource's own segment.
		// `shallow: true` is declared on the *parent* and applies to everything
		// nested inside it, so it has to be inherited down the stack rather than
		// read only off this call. Reading it locally means the common spelling —
		// `resources :posts, shallow: true do resources :comments end` — never
		// takes effect on the resource it was written for.
		shallow := !singular && parentNesting != "" &&
			(pairBool(args, "shallow", rw.src) || inheritedShallow(stack))
		shallowBase := strings.TrimSuffix(prefix, parentNesting) + "/" + segmentName

		filter := actionFilter{only: only, except: except, onlyGiven: onlyGiven, exceptGiven: exceptGiven}
		actions := restfulActions(filter)
		switch {
		case jsonapi && singular:
			actions = jsonapiRestfulActionsSingular(filter)
		case jsonapi:
			actions = jsonapiRestfulActions(filter)
		case singular:
			actions = restfulActionsSingular(filter)
		}
		var resourceClass *jsonapiResourceClass
		mod := modulePath(stack)
		if jsonapi && rw.jsonapi != nil {
			resourceClass = rw.jsonapi.resourceClass(mod, name)
		}
		if resourceClass != nil && resourceClass.immutable {
			actions = filterActions(actions, writeActions, false)
		}
		// `resources :sync, param: :key` renames the member segment: Rails serves
		// /sync/:key, and every member route under it uses that name. The action
		// tables spell the default, so the rename is applied to what they produce.
		memberName := "id"
		if p := pairSymbol(args, "param", rw.src); p != "" {
			memberName = p
		}
		// Every RESTful route names a controller, and it is derivable without a
		// `to:`: the resource's own name under its module path, unless the
		// declaration overrode it. 2,688 of the monolith's routes claimed no
		// handler at all, which is not the same as having none.
		// A singular resource is served by the PLURAL controller — `resource :eeo`
		// by eeos — and pluralizing is a rule this extractor has refused to grow
		// for good reason. Without an explicit controller: it claims nothing.
		controller := ""
		if override := pairString(args, "controller", rw.src); override != "" {
			controller = qualifyHandler(override, modulePath(stack))
		} else if !singular {
			controller = joinModule(modulePath(stack), name)
		}
		for _, a := range actions {
			routePath := resourcePath + strings.ReplaceAll(a.suffix, "/:id", "/:"+memberName)
			if shallow && strings.HasPrefix(a.suffix, "/:id") {
				routePath = shallowBase + strings.ReplaceAll(a.suffix, "/:id", "/:"+memberName)
			}
			resourceProps := map[string]any{
				"method":    a.method,
				"framework": "rails",
				"language":  "ruby",
				"resource":  name,
				"action":    a.name,
			}
			if controller != "" {
				resourceProps["handler"] = controller + "#" + a.name
			}
			rw.emit(routePath, line(call), resourceProps)
		}
		if resourceClass != nil {
			rw.emitJsonapiRelationships(call, resourcePath, mod, name, singular, resourceClass)
		}
		// A plural resource exposes a member id to its children; a singular one does not.
		childMember := ""
		if !singular {
			childMember = singularize(name) + "_id"
		}
		childScope := append(stack, routeScope{
			pathPrefix:         segment,
			memberParam:        childMember,
			ownParam:           memberName,
			singularOwner:      singular,
			resourceName:       controllerName(name, args, rw.src),
			explicitController: pairString(args, "controller", rw.src),
			shallow:            pairBool(args, "shallow", rw.src) || inheritedShallow(stack),
		})
		// `resources :folders, concerns: :archivable` replays the concern's routes
		// inside this resource, exactly as if they had been written in its block.
		// The common spelling carries no block at all, so this must sit outside
		// the body guard rather than inside it.
		for _, included := range symbolValues(findPairValue(args, "concerns", rw.src), rw.src) {
			if defined := rw.concerns[included]; defined != nil {
				rw.walk(defined, childScope)
			}
		}
		if body != nil {
			rw.walk(body, childScope)
		}

	case "match":
		// `match 'x', via: [:get, :post]` maps one path to several verbs; emit one
		// route per listed verb. Without a `via:` the verb set is ambiguous (older
		// Rails defaulted to all), so emit nothing rather than guess.
		path := firstPositionalPath(args, rw.src)
		if path == "" {
			return
		}
		if !strings.HasPrefix(path, "/") {
			path = "/" + path
		}
		handler := pairString(args, "to", rw.src)
		for _, v := range symbolValues(findPairValue(args, "via", rw.src), rw.src) {
			verb := strings.ToUpper(v)
			if verb == "" || verb == "ALL" {
				continue // via: :all matches every verb — not a concrete route
			}
			props := map[string]any{
				"method":    verb,
				"framework": "rails",
				"language":  "ruby",
			}
			if handler != "" {
				props["handler"] = handler
			}
			for _, p := range expandOptionalSegments(path) {
				rw.emit(prefix+p, line(call), props)
			}
		}

	case "namespace":
		name := firstSymbolArg(args, rw.src)
		if name == "" || body == nil {
			return
		}
		// `path:` overrides the URL segment (module stays the symbol name), e.g.
		// `namespace :admin, path: 'administration'`.
		pathSeg := "/" + name
		if p, present := pairStringPresent(args, "path", rw.src); present {
			if p != "" && !strings.HasPrefix(p, "/") {
				p = "/" + p
			}
			pathSeg = p
		}
		rw.walk(body, append(stack, routeScope{pathPrefix: pathSeg, module: name}))

	case "scope":
		// module: and path: are independent — a scope may set either or both. Read a
		// positional string path or a `path:` keyword for the URL prefix, and `module:`
		// for the controller namespace.
		ns := routeScope{}
		// Only a *positional* string is a path. `scope module: "internal"` names
		// a controller namespace and contributes no URL segment; reading its
		// value as a path prefixes every route inside with /internal.
		path, explicit := firstPositionalString(args, rw.src), false
		if path == "" {
			path, explicit = pairStringPresent(args, "path", rw.src)
		}
		if path == "" {
			// A bare positional symbol is a path prefix: `scope :users` == `scope
			// path: 'users'`. (module: is a keyword pair, so it is not read here.)
			path = firstSymbolArg(args, rw.src)
		}
		if path != "" || explicit {
			if path != "" && !strings.HasPrefix(path, "/") {
				path = "/" + path
			}
			ns.pathPrefix = path
		}
		// `scope module: "api"` is as common as the symbol form, and reading only
		// symbols loses the whole "api" segment of every controller under it.
		mod := pairSymbol(args, "module", rw.src)
		if mod == "" {
			mod = pairString(args, "module", rw.src)
		}
		if mod != "" {
			ns.module = mod
		}
		if body != nil {
			rw.walk(body, append(stack, ns))
		}

	case "concern":
		// `concern :name do ... end` defines routes to be replayed wherever the
		// concern is included; it declares nothing on its own.
		if name := firstSymbolArg(args, rw.src); name != "" && body != nil {
			rw.concerns[name] = body
		}

	case "concerns":
		// `concerns :a, :b` includes them at this point in the current scope.
		for _, name := range positionalSymbols(args, rw.src) {
			if included := rw.concerns[name]; included != nil {
				rw.walk(included, stack)
			}
		}

	case "member", "collection":
		memberPrefix := ""
		if method == "member" {
			memberPrefix = memberSegment(stack)
		}
		if body != nil {
			rw.walk(body, append(stack, routeScope{
				pathPrefix: memberPrefix, dropParentMember: true,
			}))
		}

	case "draw":
		// `draw do ... end` is the routes wrapper (Rails.application.routes.draw,
		// engine routers, etc.) — recurse into the block. `draw(:pkg)` with no
		// block is a packwerk delegation — emit a DRAW route.
		if body != nil {
			rw.walk(body, stack)
			return
		}
		if pkg := firstSymbolArg(args, rw.src); pkg != "" {
			// Record the delegation so the caller can parse config/routes/<pkg>.rb
			// seeded with this prefix, giving its routes their real /api/vN scope.
			if rw.draws != nil {
				rw.draws[pkg] = prefix
			}
			// A DRAW placeholder route is still emitted (it backs route helpers); the
			// linker treats method "DRAW" as inert, so it never matches or is flagged.
			rw.out = append(rw.out, facts.Fact{
				Kind: facts.KindRoute,
				Name: prefix + "/" + pkg,
				File: rw.relFile,
				Line: line(call),
				Props: map[string]any{
					"method":    "DRAW",
					"framework": "rails",
					"language":  "ruby",
					"delegate":  pkg,
				},
			})
		}

	default:
		// Unknown DSL call — descend into any block so nested routes are still
		// discovered. A macro naming a resource by symbol is a different case: it
		// declares routes this walker cannot produce, and going quiet about that
		// is what makes an absent API surface look like an empty one. Count it.
		if !routeWrappers[method] && firstSymbolArg(args, rw.src) != "" {
			rw.unhandled[method]++
		}
		if body != nil {
			rw.walk(body, stack)
		}
	}
}

// emit appends a route fact with a declares relation to the file's directory.
func (rw *routeWalker) emit(name string, lineNum int, props map[string]any) {
	rw.out = append(rw.out, facts.Fact{
		Kind:      facts.KindRoute,
		Name:      name,
		File:      rw.relFile,
		Line:      lineNum,
		Props:     props,
		Relations: []facts.Relation{{Kind: facts.RelDeclares, Target: rw.dir}},
	})
}

// --- keyword-argument helpers ---

// pairBool reports whether args contains a `key: true` pair (e.g. `shallow: true`).
func pairBool(args *sitter.Node, key string, src []byte) bool {
	v := findPairValue(args, key, src)
	return v != nil && rubyText(v, src) == "true"
}

// expandOptionalSegments expands a Rails optional route segment `foo(/:bar)` into
// the concrete paths it serves — one with the optional groups omitted and one with
// them included: `email_subscriptions(/:key)` → ["/email_subscriptions",
// "/email_subscriptions/:key"]. A path with no optional group is returned as-is.
// (Multiple optional groups collapse to the all-omitted and all-included variants,
// which covers the trailing-optional idiom without an exponential blow-up.)
func expandOptionalSegments(path string) []string {
	if !strings.Contains(path, "(") {
		return []string{path}
	}
	included := strings.NewReplacer("(", "", ")", "").Replace(path)
	var b strings.Builder
	depth := 0
	for _, r := range path {
		switch r {
		case '(':
			depth++
		case ')':
			if depth > 0 {
				depth--
			}
		default:
			if depth == 0 {
				b.WriteRune(r)
			}
		}
	}
	omitted := b.String()
	if omitted == included {
		return []string{included}
	}
	return []string{omitted, included}
}

// pairString returns the string content of a `key: "value"` pair.
func pairString(args *sitter.Node, key string, src []byte) string {
	value, _ := pairStringPresent(args, key, src)
	return value
}

// pairStringPresent distinguishes `path: ""` from no `path:` at all. Rails
// treats the empty string as a real override — `namespace :app, path: ""`
// mounts at the root and contributes no segment — so a caller that cannot tell
// the two apart falls back to the namespace name and invents a segment.
func pairStringPresent(args *sitter.Node, key string, src []byte) (string, bool) {
	v := findPairValue(args, key, src)
	if v == nil {
		return "", false
	}
	return firstStringArg(v, src), true
}

// pairSymbol returns the symbol name of a `key: :value` pair.
func pairSymbol(args *sitter.Node, key string, src []byte) string {
	if v := findPairValue(args, key, src); v != nil && v.Kind() == "simple_symbol" {
		return strings.TrimPrefix(rubyText(v, src), ":")
	}
	return ""
}

// pairSymbolsPresent returns the symbol names of a `key: [:a, :b]` pair and
// whether the pair was written at all.
//
// The two are different questions and Rails uses the difference: `only: []`
// serves NO RESTful action, and thirteen declarations in the monolith's routes
// say exactly that to open a block of custom routes. Deciding by the size of
// what parsed — the natural way to write "was a filter given" — reads an empty
// declaration as an absent one and emits all eight actions.
func pairSymbolsPresent(args *sitter.Node, key string, src []byte) (map[string]bool, bool) {
	out := make(map[string]bool)
	v := findPairValue(args, key, src)
	if v == nil {
		return out, false
	}
	for _, name := range symbolValues(v, src) {
		out[name] = true
	}
	return out, true
}

// symbolValues returns the symbol names of a value node, which may be a single
// `:sym`, an array `[:a, :b]`, or the `%i[a b]` literal — Ruby's three spellings
// of the same list. The grammar gives `%i[]` members as bare_symbol rather than
// simple_symbol, and reading only the latter silently turns `only: %i[index show]`
// into no filter at all, which serves six routes where two are served.
func symbolValues(v *sitter.Node, src []byte) []string {
	if v == nil {
		return nil
	}
	if v.Kind() == "simple_symbol" {
		return []string{strings.TrimPrefix(rubyText(v, src), ":")}
	}
	var out []string
	for i := uint(0); i < v.ChildCount(); i++ {
		switch c := v.Child(i); c.Kind() {
		case "simple_symbol":
			out = append(out, strings.TrimPrefix(rubyText(c, src), ":"))
		case "bare_symbol", "bare_string":
			// %i[a b] gives bare_symbol and %w[a b] gives bare_string — Ruby's
			// second and third spellings of the same list. Reading one and not the
			// other drops the filter entirely, which serves eight routes where two
			// are served.
			out = append(out, rubyText(c, src))
		}
	}
	return out
}

// findPairValue returns the value node of a `key: value` pair in an argument_list.
func findPairValue(args *sitter.Node, key string, src []byte) *sitter.Node {
	if args == nil {
		return nil
	}
	for i := uint(0); i < args.ChildCount(); i++ {
		c := args.Child(i)
		if c.Kind() != "pair" {
			continue
		}
		k := c.ChildByFieldName("key")
		if k != nil && strings.TrimSuffix(rubyText(k, src), ":") == key {
			return c.ChildByFieldName("value")
		}
	}
	return nil
}

// inheritedShallow reports whether any enclosing scope declared shallow
// nesting. Rails scopes it lexically, so the flag is a property of the
// surrounding block rather than of the call that happens to read it.
func inheritedShallow(stack []routeScope) bool {
	for _, scope := range stack {
		if scope.shallow {
			return true
		}
	}
	return false
}

// firstPositionalString returns the first *direct* string argument, ignoring
// keyword pairs entirely.
//
// firstStringArg recurses into the whole argument node, so for
// `scope module: "internal"` it returns "internal" — the value of a keyword
// that names a controller namespace, not a URL segment. Reading it as a path
// prefixes every route in the block with a segment Rails never serves.
func firstPositionalString(args *sitter.Node, src []byte) string {
	if args == nil {
		return ""
	}
	for i := uint(0); i < args.ChildCount(); i++ {
		child := args.Child(i)
		if child.Kind() != "string" {
			continue
		}
		for j := uint(0); j < child.ChildCount(); j++ {
			if child.Child(j).Kind() == "string_content" {
				return rubyText(child.Child(j), src)
			}
		}
	}
	return ""
}

// positionalSymbols returns the direct symbol arguments of a call, ignoring
// keyword pairs: the `:a, :b` of `concerns :a, :b`.
func positionalSymbols(args *sitter.Node, src []byte) []string {
	if args == nil {
		return nil
	}
	var out []string
	for i := uint(0); i < args.ChildCount(); i++ {
		child := args.Child(i)
		if child.Kind() == "simple_symbol" {
			out = append(out, strings.TrimPrefix(rubyText(child, src), ":"))
		}
	}
	return out
}

// emitJsonapiRelationships emits the routes a resource class's relationships
// serve: four verbs each under /relationships/<name>, a fifth for to-many, and
// one related-resource GET whose controller is whoever serves the target.
//
// A relationship whose target does not resolve still gets its routes — the path
// is known either way — and loses only the handler prop. Saying "this endpoint
// exists and I do not know what serves it" is the whole discipline.
func (rw *routeWalker) emitJsonapiRelationships(call *sitter.Node, resourcePath, mod, declared string, singular bool, res *jsonapiResourceClass) {
	base := resourcePath
	if !singular {
		base += "/:" + singularize(declared) + "_id"
	}
	owner := rw.jsonapi.controllerFor(mod, declared)
	at := line(call)

	for _, rel := range res.relationships {
		segment := jsonapiSegment(rel.name, rw.jsonapiFormat)
		props := func(action, handler string) map[string]any {
			p := map[string]any{
				"method": "", "framework": "rails", "language": "ruby",
				"resource": declared, "relationship": rel.name, "action": action,
			}
			if handler != "" {
				p["handler"] = handler + "#" + action
			}
			return p
		}

		// `immutable` guards the write half of the relationship routes as well as
		// the RESTful ones — the gem wraps update, destroy and create in a single
		// `if res.mutable?`, so a read-only resource serves show_relationship alone.
		routes := jsonapiRelationshipRoutes
		if res.immutable {
			routes = filterActions(routes, map[string]bool{"show_relationship": true}, true)
		} else if rel.toMany {
			routes = append(append([]restAction{}, routes...), restAction{name: "create_relationship", method: "POST"})
		}
		for _, a := range routes {
			p := props(a.name, owner)
			p["method"] = a.method
			rw.emit(base+"/relationships/"+segment, at, p)
		}

		related := "get_related_resource"
		if rel.toMany {
			related = "get_related_resources"
		}
		p := props(related, rw.jsonapi.handlerFor(mod, rel, res, singularize(declared)))
		p["method"] = "GET"
		rw.emit(base+"/"+segment, at, p)
	}
}

// enclosingController is the resource name of the innermost resource scope, or
// empty when the verb is not inside one.
func enclosingController(stack []routeScope) string {
	for i := len(stack) - 1; i >= 0; i-- {
		if stack[i].resourceName == "" {
			continue
		}
		// A singular resource is served by the PLURAL controller — Rails serves
		// `resource :session` from sessions — and pluralizing is a rule this
		// extractor refuses to grow. The RESTful actions already decline to claim
		// one; a bare verb inside the same block must decline for the same reason,
		// and the domain explainer caught this the first time it ran.
		if stack[i].singularOwner && stack[i].explicitController == "" {
			return ""
		}
		return stack[i].resourceName
	}
	return ""
}

// qualifyHandler composes a handler with the controller namespace its scope
// declares. `to: "foo#show"` inside `scope module: "connect"` is served by
// connect/foo#show, and emitting the bare name points at a controller that does
// not exist.
//
// The composition happens even when the handler already names a namespace:
// `controller: "candidate/job_offers"` inside a `jobsite` module scope is served
// by jobsite/candidate/job_offers, which the booted route table says plainly.
// An earlier version of this function skipped namespaced handlers on the
// reasoning that Rails would not compose twice — reasoning, not measurement,
// and wrong for 399 handlers.
func qualifyHandler(handler, module string) string {
	if module == "" || strings.HasPrefix(handler, module+"/") {
		return handler
	}
	return module + "/" + handler
}

// controllerName is what a resource declaration's routes are served by: its own
// name, unless `controller:` named another.
func controllerName(name string, args *sitter.Node, src []byte) string {
	if override := pairString(args, "controller", src); override != "" {
		return override
	}
	return name
}

// mountPath reads the path a `mount` declaration serves. Rails accepts two
// spellings — `mount App => "/path"` and `mount App, at: "/path"` — and a
// declaration whose path is computed is counted rather than guessed at.
func mountPath(args *sitter.Node, src []byte) string {
	if at, present := pairStringPresent(args, "at", src); present {
		return at
	}
	if args == nil {
		return ""
	}
	// The hash-rocket form parses as a pair whose key is the mounted class.
	for i := uint(0); i < args.ChildCount(); i++ {
		child := args.Child(i)
		if child.Kind() != "pair" {
			continue
		}
		value := child.ChildByFieldName("value")
		if value != nil && value.Kind() == "string" {
			return strings.Trim(rubyText(value, src), `"'`)
		}
	}
	return ""
}
