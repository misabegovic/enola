// Package frameworkroots marks where a framework enters application code, and
// what that entry reaches.
//
// The graph records who calls whom inside the application and nothing about
// the calls a framework makes into it: a router dispatching to an action, a
// queue draining a job, a mailer delivering, a migration running, a class
// body registering one of its own methods as a callback. Without those
// roots every one of them looks unreachable, which is why a dead-code
// verdict was declined twice on the same measurement.
//
// The first pass sets RootProp on the facts that already exist, with the
// mechanism as the value: a route and the action it resolves to, and the
// methods a framework invokes on a class the store can tie to that
// framework (its component, its superclass, or a mixin it declares). The
// table is data and every row names the class-level evidence it requires;
// a method name alone roots nothing, so a service object's call is not a
// root and a job's perform is.
//
// A class a library loads by its base class roots what the library calls on
// it: a maintenance task's collection and process, an admin resource's fields,
// an MCP tool's singleton call. A module's callbacks root on the classes a
// framework drives that include it.
//
// The second pass walks calls, handled_by, depends_on and instantiates edges
// from every root and sets ReachedFromProp on each symbol reached, naming the
// mechanisms that reach it. A bare call name resolves the way Ruby looks up a
// method on an implicit receiver: the caller's own class, then each
// superclass and the modules each includes. Building an instance reaches its
// initialize, and building a view component reaches the lifecycle its render
// runs. Anything else the walk cannot follow stays unreached and is counted,
// so a reader can tell a dead symbol from one behind an untyped receiver or a
// class named only as a value.
//
// Where the library that loads a root fixes the process that runs it, the
// root carries RootRuntimeProp and what it reaches carries ReachedRuntimeProp:
// a request serves routes, admin resources and MCP tools, and a queue worker
// runs jobs and the maintenance tasks the library's own job drives. A root
// whose library runs it wherever its caller runs (a mailer, a migration, a
// callback, a Pundit hook) carries no runtime and lends none to what it
// reaches, so the absence of a runtime is not a claim that nothing runs it.
//
// Both passes live in one binder because the second depends on the first,
// and binders may not depend on the order they were registered in.
package frameworkroots

import (
	"context"
	"log"
	"sort"
	"strings"

	"github.com/enola-labs/enola/internal/facts"
	"github.com/enola-labs/enola/pkg/plugin"
)

// RootProp names the mechanism through which a framework invokes a symbol:
// route, job, mailer, hook, task or callback. A route fact carries it too.
const RootProp = facts.PropFrameworkRoot

// ReachedFromProp lists, comma-separated and sorted, the root mechanisms
// whose walk reaches a symbol that is not itself a root.
const ReachedFromProp = "reached_from"

// RootRuntimeProp names the runtime a root runs in, set only where the library
// that invokes it fixes the process: web or job.
const RootRuntimeProp = "root_runtime"

// ReachedRuntimeProp lists, comma-separated and sorted, the runtimes of the
// roots whose walk reaches a symbol. A root of no known runtime adds none, so
// the list names where a symbol is known to run, not everywhere it may.
const ReachedRuntimeProp = "reached_runtime"

const (
	RuntimeWeb = "web"
	RuntimeJob = "job"
)

// libraryRuntimes names, per library base class or mixin in the hook and task
// tables, the runtime that library invokes its hooks in. A row absent here
// runs wherever its includer runs, which the library does not fix.
var libraryRuntimes = map[string]string{
	"Sidekiq::Job::Iterable":                          RuntimeJob,
	"Scimitar::ResourcesController":                   RuntimeWeb,
	"Scimitar::ActiveRecordBackedResourcesController": RuntimeWeb,
	"Avo::BaseResource":                               RuntimeWeb,
	"Avo::BaseAction":                                 RuntimeWeb,
	"Avo::Filters::SelectFilter":                      RuntimeWeb,
	"Avo::Filters::MultipleSelectFilter":              RuntimeWeb,
	"Avo::Filters::BooleanFilter":                     RuntimeWeb,
	"Avo::Filters::TextFilter":                        RuntimeWeb,
	"Avo::Filters::DateTimeFilter":                    RuntimeWeb,
	"MaintenanceTasks::Task":                          RuntimeJob,
	"MCP::Tool":                                       RuntimeWeb,
}

const (
	MechanismRoute    = "route"
	MechanismJob      = "job"
	MechanismMailer   = "mailer"
	MechanismHook     = "hook"
	MechanismTask     = "task"
	MechanismCallback = "callback"
)

var mechanisms = []string{MechanismRoute, MechanismJob, MechanismMailer, MechanismHook, MechanismTask, MechanismCallback}

var jobMixins = map[string]bool{"Sidekiq::Worker": true, "Sidekiq::Job": true}

var jobSuperclasses = map[string]bool{"ApplicationJob": true, "ActiveJob::Base": true}

// mixinHooks names, per mixin, the methods the mixed-in library calls on the
// class that included it. A row applies only to a class whose mixin fact names
// the module, so the evidence is the include itself.
var mixinHooks = map[string]map[string]bool{
	"Sidekiq::Job::Iterable": {"build_enumerator": true, "each_iteration": true, "on_start": true, "on_resume": true, "on_stop": true, "on_complete": true, "on_cancel": true, "around_iteration": true},
	"Pundit::Authorization":  {"pundit_user": true, "pundit_params_for": true, "policy_scope": true, "authorize": true},
	"Pundit":                 {"pundit_user": true, "pundit_params_for": true, "policy_scope": true, "authorize": true},
}

var walkedRelations = map[string]bool{facts.RelCalls: true, facts.RelHandledBy: true, facts.RelDependsOn: true, facts.RelInstantiates: true}

// superclassHooks names, per library base class, the methods the library
// calls on a subclass. A row applies through the superclass chain.
var superclassHooks = map[string]map[string]bool{
	"Scimitar::ResourcesController":                   {"storage_class": true, "storage_scope": true},
	"Scimitar::ActiveRecordBackedResourcesController": {"storage_class": true, "storage_scope": true},
	"Avo::BaseResource":                               {"fields": true, "actions": true, "filters": true, "scopes": true, "cards": true},
	"Avo::BaseAction":                                 {"handle": true, "fields": true},
	"Avo::Filters::SelectFilter":                      {"apply": true, "options": true, "default": true},
	"Avo::Filters::MultipleSelectFilter":              {"apply": true, "options": true, "default": true},
	"Avo::Filters::BooleanFilter":                     {"apply": true, "options": true, "default": true},
	"Avo::Filters::TextFilter":                        {"apply": true, "default": true},
	"Avo::Filters::DateTimeFilter":                    {"apply": true, "default": true},
}

// taskSuperclasses names, per library base class, the methods its runner
// calls on a subclass when an operator starts the task.
var taskSuperclasses = map[string]map[string]bool{
	"MaintenanceTasks::Task": {"collection": true, "process": true, "count": true, "csv_collection": true},
}

// singletonHooks names, per library base class, the class methods the library
// calls on a subclass: an MCP server calls a registered tool's call.
var singletonHooks = map[string]map[string]bool{
	"MCP::Tool": {"call": true},
}

var componentBases = map[string]bool{"ViewComponent::Base": true}

// renderLifecycle is what building and rendering a view component runs, in
// order; a plain instance runs only the first.
var renderLifecycle = []string{"initialize", "before_render", "render?", "call"}

type class struct {
	name       string
	component  string
	superclass string
	mixins     map[string]bool
	names      map[string]bool
	includers  []*class
	classes    map[string]*class
}

// lookup finds the class a constant written inside `from` names, the way Ruby
// resolves it: an absolute name as written, else each enclosing namespace of
// `from` outward, then the top level.
func lookup(classes map[string]*class, ref, from string) *class {
	if strings.HasPrefix(ref, "::") {
		return classes[strings.TrimPrefix(ref, "::")]
	}
	for scope := from; scope != ""; {
		i := strings.LastIndex(scope, "::")
		if i < 0 {
			break
		}
		scope = scope[:i]
		if c := classes[scope+"::"+ref]; c != nil {
			return c
		}
	}
	return classes[ref]
}

// ancestry is the class and its superclasses as far as the store declares
// them; what a superclass includes or is, the subclass inherits.
func (c *class) ancestry() []*class {
	out := []*class{c}
	seen := map[string]bool{c.name: true}
	for cur := c; cur.superclass != ""; {
		next := lookup(c.classes, cur.superclass, cur.name)
		if next == nil || seen[next.name] {
			break
		}
		seen[next.name] = true
		out = append(out, next)
		cur = next
	}
	return out
}

func base(superclass string) string {
	return strings.TrimPrefix(superclass, "::")
}

// taskRunBy, singletonHookedBy and hookedBy name the library base class or
// mixin whose row roots the method, or nothing.
func (c *class) taskRunBy(method string) string {
	for _, a := range c.ancestry() {
		if taskSuperclasses[base(a.superclass)][method] {
			return base(a.superclass)
		}
	}
	return ""
}

func (c *class) singletonHookedBy(method string) string {
	for _, a := range c.ancestry() {
		if singletonHooks[base(a.superclass)][method] {
			return base(a.superclass)
		}
	}
	return ""
}

func (c *class) isComponent() bool {
	for _, a := range c.ancestry() {
		if a.component == "component" || componentBases[base(a.superclass)] {
			return true
		}
	}
	return false
}

// includedIntoFramework says a module is mixed into a class a framework drives
// by its kind or its base class, so the callbacks its included block registers
// run on that class.
func (c *class) includedIntoFramework() bool {
	for _, includer := range c.includers {
		for _, a := range includer.ancestry() {
			if a.component != "" || a.superclass != "" {
				return true
			}
		}
	}
	return false
}

func (c *class) frameworkBound() bool {
	return c.component != "" || c.superclass != "" || len(c.mixins) > 0
}

func (c *class) hookedBy(method string) string {
	for _, a := range c.ancestry() {
		if superclassHooks[base(a.superclass)][method] {
			return base(a.superclass)
		}
		for _, mixin := range sortedKeys(a.mixins) {
			if mixinHooks[mixin][method] {
				return mixin
			}
		}
	}
	return ""
}

func (c *class) runsJobs() bool {
	for _, a := range c.ancestry() {
		if a.component == "job" || jobSuperclasses[a.superclass] {
			return true
		}
		for mixin := range a.mixins {
			if jobMixins[mixin] {
				return true
			}
		}
	}
	return false
}

type Binder struct{}

func New() *Binder { return &Binder{} }

func (b *Binder) Name() string { return "framework-roots" }

func (b *Binder) Stage() plugin.BindStage { return plugin.StagePostLink }

func (b *Binder) Bind(_ context.Context, store *facts.Store) error {
	symbols := map[string]facts.Fact{}
	classes := map[string]*class{}
	for _, f := range store.ByKind(facts.KindSymbol) {
		if f.PropString("language") != "ruby" {
			continue
		}
		symbols[f.Name] = f
		switch f.PropString("symbol_kind") {
		case facts.SymbolClass, facts.SymbolInterface:
			c := &class{name: f.Name, component: f.PropString("rails_component"), superclass: f.PropString("superclass"), mixins: map[string]bool{}, names: map[string]bool{}, classes: classes}
			for _, r := range f.Relations {
				if r.Kind == facts.RelNames {
					c.names[r.Target] = true
				}
			}
			classes[f.Name] = c
		}
	}
	if len(symbols) == 0 {
		return nil
	}
	for _, dep := range store.ByKind(facts.KindDependency) {
		if dep.PropString("mixin_kind") == "" {
			continue
		}
		includer, module, ok := strings.Cut(dep.Name, " -> ")
		if c := classes[includer]; ok && c != nil {
			c.mixins[module] = true
			if m := lookup(classes, module, includer); m != nil {
				m.includers = append(m.includers, c)
			}
		}
	}

	roots := map[string]string{}
	runtimes := map[string]string{}
	routeTargets := map[string]string{}
	unresolvedRoutes := 0
	for _, route := range store.ByKind(facts.KindRoute) {
		if route.PropString("framework") != "rails" {
			continue
		}
		target := actionFor(route.PropString("handler"))
		if target == "" {
			continue
		}
		if _, ok := symbols[target]; !ok {
			unresolvedRoutes++
			continue
		}
		roots[target] = MechanismRoute
		runtimes[target] = RuntimeWeb
		routeTargets[routeKey(route)] = target
	}
	for name, f := range symbols {
		kind := f.PropString("symbol_kind")
		if kind != facts.SymbolMethod && kind != facts.SymbolFunc {
			continue
		}
		if _, already := roots[name]; already {
			continue
		}
		owner, method, singleton := splitMember(name)
		c := classes[owner]
		if c == nil {
			continue
		}
		if kind == facts.SymbolFunc {
			if library := c.singletonHookedBy(method); singleton && library != "" {
				roots[name] = MechanismHook
				runtimes[name] = libraryRuntimes[library]
			}
			continue
		}
		if mechanism, runtime := mechanismFor(c, f, method, singleton); mechanism != "" {
			roots[name] = mechanism
			runtimes[name] = runtime
		}
	}

	adjacency := edges(store, symbols, classes)
	// One walk per mechanism and runtime: the walks a runtime splits a
	// mechanism into cover the same symbols the mechanism's single walk did.
	reached := map[string]map[string]bool{}
	reachedRuntimes := map[string]map[string]bool{}
	type origin struct{ mechanism, runtime string }
	walks := map[origin][]string{}
	for name, m := range roots {
		o := origin{m, runtimes[name]}
		walks[o] = append(walks[o], name)
	}
	for _, mechanism := range mechanisms {
		for _, runtime := range []string{"", RuntimeJob, RuntimeWeb} {
			frontier := walks[origin{mechanism, runtime}]
			if len(frontier) == 0 {
				continue
			}
			walk(frontier, adjacency, roots, func(target string) {
				if reached[target] == nil {
					reached[target] = map[string]bool{}
				}
				reached[target][mechanism] = true
				if runtime == "" {
					return
				}
				if reachedRuntimes[target] == nil {
					reachedRuntimes[target] = map[string]bool{}
				}
				reachedRuntimes[target][runtime] = true
			})
		}
	}
	perMechanism := map[string]int{}
	perRuntime := map[string]int{}
	store.UpdateWhere(func(f *facts.Fact) {
		switch f.Kind {
		case facts.KindRoute:
			target, ok := routeTargets[routeKey(*f)]
			if !ok {
				return
			}
			setProp(f, RootProp, MechanismRoute)
			setProp(f, RootRuntimeProp, RuntimeWeb)
			if !f.HasRelation(facts.RelHandledBy, target) {
				f.Relations = append(f.Relations, facts.Relation{Kind: facts.RelHandledBy, Target: target})
			}
		case facts.KindSymbol:
			if mechanism, ok := roots[f.Name]; ok {
				setProp(f, RootProp, mechanism)
				perMechanism[mechanism]++
				if runtime := runtimes[f.Name]; runtime != "" {
					setProp(f, RootRuntimeProp, runtime)
					perRuntime[runtime]++
				}
				return
			}
			if by, ok := reached[f.Name]; ok {
				setProp(f, ReachedFromProp, joinSorted(by))
			}
			if in, ok := reachedRuntimes[f.Name]; ok {
				setProp(f, ReachedRuntimeProp, joinSorted(in))
			}
		}
	})

	unreached := 0
	for name, f := range symbols {
		if f.PropString("symbol_kind") != facts.SymbolMethod {
			continue
		}
		if _, isRoot := roots[name]; isRoot {
			continue
		}
		if _, ok := reached[name]; !ok {
			unreached++
		}
	}
	if len(roots) > 0 {
		log.Printf("[framework-roots] %d root(s) (route %d, job %d, mailer %d, hook %d, task %d, callback %d; runtime web %d, job %d); %d route handler(s) name no known action; %d symbol(s) reached (%d with a runtime), %d method(s) unreached",
			len(roots), perMechanism[MechanismRoute], perMechanism[MechanismJob], perMechanism[MechanismMailer], perMechanism[MechanismHook], perMechanism[MechanismTask], perMechanism[MechanismCallback],
			perRuntime[RuntimeWeb], perRuntime[RuntimeJob], unresolvedRoutes, len(reached), len(reachedRuntimes), unreached)
	}
	return nil
}

func mechanismFor(c *class, f facts.Fact, method string, singleton bool) (mechanism, runtime string) {
	if !singleton {
		switch {
		case method == "perform" && c.runsJobs():
			return MechanismJob, RuntimeJob
		case c.component == "mailer" && f.PropBool("exported"):
			return MechanismMailer, ""
		case c.component == "channel" && (method == "subscribed" || method == "unsubscribed"):
			return MechanismHook, RuntimeWeb
		}
		if library := c.hookedBy(method); library != "" {
			return MechanismHook, libraryRuntimes[library]
		}
		if isMigration(f.File) && (method == "change" || method == "up" || method == "down") {
			return MechanismTask, ""
		}
		if library := c.taskRunBy(method); library != "" {
			return MechanismTask, libraryRuntimes[library]
		}
	}
	if (c.frameworkBound() || c.includedIntoFramework()) && c.names[method] {
		return MechanismCallback, ""
	}
	return "", ""
}

// walk visits, breadth first, every symbol the frontier reaches, telling visit
// of each one that is not itself a root.
func walk(frontier []string, adjacency map[string][]string, roots map[string]string, visit func(string)) {
	seen := map[string]bool{}
	for len(frontier) > 0 {
		next := []string{}
		for _, name := range frontier {
			for _, target := range adjacency[name] {
				if seen[target] {
					continue
				}
				seen[target] = true
				if _, isRoot := roots[target]; !isRoot {
					visit(target)
				}
				next = append(next, target)
			}
		}
		frontier = next
	}
}

func isMigration(file string) bool {
	return strings.Contains(file, "db/migrate/")
}

// actionFor turns a Rails handler such as `admin/accounts#show` into the
// symbol the graph names, `Admin::AccountsController#show`.
func actionFor(handler string) string {
	path, action, ok := strings.Cut(handler, "#")
	if !ok || path == "" || action == "" || strings.Contains(path, "://") || strings.ContainsAny(path, " :") {
		return ""
	}
	segments := strings.Split(path, "/")
	for i, segment := range segments {
		parts := strings.Split(segment, "_")
		for j, part := range parts {
			if part != "" {
				parts[j] = strings.ToUpper(part[:1]) + part[1:]
			}
		}
		segments[i] = strings.Join(parts, "")
	}
	return strings.Join(segments, "::") + "Controller#" + action
}

func routeKey(f facts.Fact) string {
	return f.Repo + "\x00" + f.File + "\x00" + f.Name + "\x00" + f.PropString("method") + "\x00" + f.PropString("handler")
}

func splitMember(name string) (owner, method string, singleton bool) {
	if hash := strings.LastIndex(name, "#"); hash > 0 {
		return name[:hash], name[hash+1:], false
	}
	if dot := strings.LastIndex(name, "."); dot > 0 && !strings.Contains(name[dot:], "::") {
		return name[:dot], name[dot+1:], true
	}
	return "", "", false
}

// edges collects, per symbol, the symbols its walked relations resolve to. A
// target that is already a symbol name resolves as written; a bare name
// resolves the way Ruby looks up a method on an implicit receiver: the
// caller's own class, the modules it includes and its superclasses, and
// nowhere else. Building an instance runs its initialize, and building a view
// component runs the lifecycle its render calls.
func edges(store *facts.Store, symbols map[string]facts.Fact, classes map[string]*class) map[string][]string {
	out := map[string][]string{}
	add := func(source, target string) {
		if resolved := resolve(source, target, symbols, classes); resolved != "" && resolved != source {
			out[source] = append(out[source], resolved)
		}
	}
	walk := func(source string, r facts.Relation) {
		if !walkedRelations[r.Kind] {
			return
		}
		add(source, r.Target)
		if r.Kind != facts.RelInstantiates {
			return
		}
		owner, _, _ := splitMember(source)
		if owner == "" {
			owner = source
		}
		built := classes[r.Target]
		if built == nil {
			built = lookup(classes, r.Target, owner)
		}
		if built == nil {
			return
		}
		hooks := renderLifecycle[:1]
		if built.isComponent() {
			hooks = renderLifecycle
		}
		for _, hook := range hooks {
			for _, a := range built.ancestry() {
				if _, ok := symbols[a.name+"#"+hook]; ok {
					out[source] = append(out[source], a.name+"#"+hook)
					break
				}
			}
		}
	}
	for _, f := range store.ByKind(facts.KindSymbol) {
		if _, ok := symbols[f.Name]; !ok {
			continue
		}
		for _, r := range f.Relations {
			walk(f.Name, r)
		}
	}
	for _, f := range store.ByKind(facts.KindDependency) {
		source, _, ok := strings.Cut(f.Name, " -> ")
		if !ok {
			continue
		}
		if _, prefixed, hasPrefix := strings.Cut(source, ": "); hasPrefix {
			source = prefixed
		}
		if _, known := symbols[source]; !known {
			continue
		}
		for _, r := range f.Relations {
			walk(source, r)
		}
	}
	return out
}

func resolve(source, target string, symbols map[string]facts.Fact, classes map[string]*class) string {
	owner, _, _ := splitMember(source)
	if owner == "" {
		owner = source
	}
	if namespaced := lexical(owner, target, symbols); namespaced != "" {
		return namespaced
	}
	if _, ok := symbols[target]; ok {
		return target
	}
	if strings.ContainsAny(target, "#.:") {
		return ""
	}
	for _, candidate := range []string{owner + "#" + target, owner + "." + target} {
		if _, ok := symbols[candidate]; ok {
			return candidate
		}
	}
	c := classes[owner]
	if c == nil {
		return ""
	}
	for _, a := range c.ancestry() {
		if _, ok := symbols[a.name+"#"+target]; ok && a != c {
			return a.name + "#" + target
		}
		for _, module := range sortedKeys(a.mixins) {
			if m := lookup(classes, module, a.name); m != nil {
				if _, ok := symbols[m.name+"#"+target]; ok {
					return m.name + "#" + target
				}
			}
		}
	}
	return ""
}

// lexical finds a constant-led target the way Ruby finds a constant written
// inside owner: each enclosing namespace of owner outward, before the name as
// written. A target starting with :: or naming a method on self is left alone.
func lexical(owner, target string, symbols map[string]facts.Fact) string {
	if strings.HasPrefix(target, "::") || strings.Contains(target, "#") || target == "" || target[0] < 'A' || target[0] > 'Z' {
		return ""
	}
	for scope := owner; ; {
		if _, ok := symbols[scope+"::"+target]; ok {
			return scope + "::" + target
		}
		i := strings.LastIndex(scope, "::")
		if i < 0 {
			return ""
		}
		scope = scope[:i]
	}
}

func setProp(f *facts.Fact, key, value string) {
	if f.Props == nil {
		f.Props = map[string]any{}
	}
	f.SetProp(key, value)
}

func sortedKeys(set map[string]bool) []string {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func joinSorted(set map[string]bool) string {
	names := make([]string, 0, len(set))
	for name := range set {
		names = append(names, name)
	}
	sort.Strings(names)
	return strings.Join(names, ",")
}
