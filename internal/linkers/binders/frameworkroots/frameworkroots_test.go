package frameworkroots

import (
	"context"
	"testing"

	"github.com/enola-labs/enola/internal/facts"
)

func symbol(name, kind, file string, props map[string]any, relations ...facts.Relation) facts.Fact {
	p := map[string]any{"symbol_kind": kind, "language": "ruby", "framework": "rails", "exported": true}
	for k, v := range props {
		p[k] = v
	}
	return facts.Fact{Kind: facts.KindSymbol, Name: name, File: file, Repo: "app", Props: p, Relations: relations}
}

func calls(targets ...string) []facts.Relation {
	out := []facts.Relation{}
	for _, t := range targets {
		out = append(out, facts.Relation{Kind: facts.RelCalls, Target: t})
	}
	return out
}

func names(targets ...string) []facts.Relation {
	out := []facts.Relation{}
	for _, t := range targets {
		out = append(out, facts.Relation{Kind: facts.RelNames, Target: t})
	}
	return out
}

func route(handler string) facts.Fact {
	return facts.Fact{Kind: facts.KindRoute, Name: "/things", File: "config/routes.rb", Repo: "app",
		Props: map[string]any{"framework": "rails", "language": "ruby", "method": "GET", "handler": handler}}
}

func propOf(store *facts.Store, kind, name, prop string) string {
	for _, f := range store.ByKind(kind) {
		if f.Name == name {
			return f.PropString(prop)
		}
	}
	return ""
}

func bind(t *testing.T, store *facts.Store) {
	t.Helper()
	if err := New().Bind(context.Background(), store); err != nil {
		t.Fatal(err)
	}
}

// A route roots the action it resolves to and binds to it; a handler naming
// no known action roots nothing.
func TestBind_RoutesRootTheActionsTheyResolveTo(t *testing.T) {
	store := facts.NewStore()
	store.Add(
		route("admin/accounts#show"),
		route("vanished#index"),
		symbol("Admin::AccountsController", facts.SymbolClass, "app/controllers/admin/accounts_controller.rb", map[string]any{"rails_component": "controller"}),
		symbol("Admin::AccountsController#show", facts.SymbolMethod, "app/controllers/admin/accounts_controller.rb", nil),
	)
	bind(t, store)
	if got := propOf(store, facts.KindSymbol, "Admin::AccountsController#show", RootProp); got != MechanismRoute {
		t.Fatalf("action root = %q", got)
	}
	bound := 0
	for _, r := range store.ByKind(facts.KindRoute) {
		if r.HasRelation(facts.RelHandledBy, "Admin::AccountsController#show") {
			bound++
			if r.PropString(RootProp) != MechanismRoute {
				t.Fatal("bound route carries no root")
			}
		} else if r.PropString(RootProp) != "" {
			t.Fatalf("unresolved route %s carries a root", r.PropString("handler"))
		}
	}
	if bound != 1 {
		t.Fatalf("want one bound route, got %d", bound)
	}
}

// Each table row requires class-level evidence: a perform is a job root on a
// job component, an ActiveJob subclass or a Sidekiq includer, and nothing
// else; a service object's call is not a root.
func TestBind_FrameworkInvokedMethodsRootOnlyWithClassEvidence(t *testing.T) {
	store := facts.NewStore()
	store.Add(
		symbol("ImportJob", facts.SymbolClass, "app/jobs/import_job.rb", map[string]any{"rails_component": "job"}),
		symbol("ImportJob#perform", facts.SymbolMethod, "app/jobs/import_job.rb", nil),
		symbol("Legacy::Worker", facts.SymbolClass, "lib/legacy/worker.rb", nil),
		symbol("Legacy::Worker#perform", facts.SymbolMethod, "lib/legacy/worker.rb", nil),
		facts.Fact{Kind: facts.KindDependency, Name: "Legacy::Worker -> Sidekiq::Worker", File: "lib/legacy/worker.rb", Repo: "app",
			Props: map[string]any{"language": "ruby", "mixin_kind": "include"}, Relations: []facts.Relation{{Kind: facts.RelImplements, Target: "Sidekiq::Worker"}}},
		symbol("Reports::Build", facts.SymbolClass, "app/services/reports/build.rb", map[string]any{"rails_component": "service"}),
		symbol("Reports::Build#call", facts.SymbolMethod, "app/services/reports/build.rb", nil),
		symbol("Reports::Build#perform", facts.SymbolMethod, "app/services/reports/build.rb", nil),
		symbol("CandidateMailer", facts.SymbolClass, "app/mailers/candidate_mailer.rb", map[string]any{"rails_component": "mailer"}),
		symbol("CandidateMailer#welcome", facts.SymbolMethod, "app/mailers/candidate_mailer.rb", nil),
		symbol("CandidateMailer#headers_for", facts.SymbolMethod, "app/mailers/candidate_mailer.rb", map[string]any{"exported": false}),
		symbol("ChatChannel", facts.SymbolClass, "app/channels/chat_channel.rb", map[string]any{"rails_component": "channel"}),
		symbol("ChatChannel#subscribed", facts.SymbolMethod, "app/channels/chat_channel.rb", nil),
		symbol("AddIndexToThings", facts.SymbolClass, "db/migrate/20260821000000_add_index_to_things.rb", map[string]any{"superclass": "ActiveRecord::Migration[8.0]"}),
		symbol("AddIndexToThings#change", facts.SymbolMethod, "db/migrate/20260821000000_add_index_to_things.rb", nil),
	)
	bind(t, store)
	want := map[string]string{
		"ImportJob#perform":           MechanismJob,
		"Legacy::Worker#perform":      MechanismJob,
		"Reports::Build#call":         "",
		"Reports::Build#perform":      "",
		"CandidateMailer#welcome":     MechanismMailer,
		"CandidateMailer#headers_for": "",
		"ChatChannel#subscribed":      MechanismHook,
		"AddIndexToThings#change":     MechanismTask,
	}
	for name, mechanism := range want {
		if got := propOf(store, facts.KindSymbol, name, RootProp); got != mechanism {
			t.Errorf("%s root = %q, want %q", name, got, mechanism)
		}
	}
}

// A method its own class body names is a callback root; a name the class
// body uses that is not one of its methods roots nothing; and the walk from
// a root reaches a bare-named method only through the caller's own class.
func TestBind_CallbacksAndReachabilityFollowTheClass(t *testing.T) {
	store := facts.NewStore()
	store.Add(
		route("things#index"),
		symbol("ThingsController", facts.SymbolClass, "app/controllers/things_controller.rb", map[string]any{"rails_component": "controller"}, names("load_thing", "things")...),
		symbol("ThingsController#index", facts.SymbolMethod, "app/controllers/things_controller.rb", nil, calls("render_list", "Things::Query.run", "helper_elsewhere")...),
		symbol("ThingsController#load_thing", facts.SymbolMethod, "app/controllers/things_controller.rb", map[string]any{"exported": false}),
		symbol("ThingsController#render_list", facts.SymbolMethod, "app/controllers/things_controller.rb", map[string]any{"exported": false}),
		symbol("Things::Query", facts.SymbolClass, "app/queries/things/query.rb", nil),
		symbol("Things::Query.run", facts.SymbolMethod, "app/queries/things/query.rb", nil, calls("scope_for")...),
		symbol("Things::Query.scope_for", facts.SymbolMethod, "app/queries/things/query.rb", nil),
		symbol("Other#helper_elsewhere", facts.SymbolMethod, "app/models/other.rb", nil),
		symbol("Other#idle", facts.SymbolMethod, "app/models/other.rb", nil),
	)
	for i := 0; i < 2; i++ {
		bind(t, store)
	}
	if got := propOf(store, facts.KindSymbol, "ThingsController#load_thing", RootProp); got != MechanismCallback {
		t.Fatalf("callback root = %q", got)
	}
	for name, want := range map[string]string{
		"ThingsController#render_list": MechanismRoute,
		"Things::Query.run":            MechanismRoute,
		"Things::Query.scope_for":      MechanismRoute,
		"Other#helper_elsewhere":       "",
		"Other#idle":                   "",
	} {
		if got := propOf(store, facts.KindSymbol, name, ReachedFromProp); got != want {
			t.Errorf("%s reached_from = %q, want %q", name, got, want)
		}
	}
	if got := propOf(store, facts.KindSymbol, "ThingsController#index", ReachedFromProp); got != "" {
		t.Fatalf("a root is not also reached: %q", got)
	}
}

// A library a class mixes in calls methods of its own naming on that class:
// Sidekiq's iterable jobs drive build_enumerator and each_iteration, Pundit
// asks pundit_user. The row applies through the mixin fact and nowhere else.
func TestBind_MixedInLibrariesRootTheHooksTheyCall(t *testing.T) {
	store := facts.NewStore()
	mixin := func(includer, module string) facts.Fact {
		return facts.Fact{Kind: facts.KindDependency, Name: includer + " -> " + module, File: "app/x.rb", Repo: "app",
			Props: map[string]any{"language": "ruby", "mixin_kind": "include"}, Relations: []facts.Relation{{Kind: facts.RelImplements, Target: module}}}
	}
	store.Add(
		symbol("TrackEventsWorker", facts.SymbolClass, "app/workers/track_events_worker.rb", nil),
		symbol("TrackEventsWorker#build_enumerator", facts.SymbolMethod, "app/workers/track_events_worker.rb", nil),
		symbol("TrackEventsWorker#each_iteration", facts.SymbolMethod, "app/workers/track_events_worker.rb", nil),
		symbol("TrackEventsWorker#helper", facts.SymbolMethod, "app/workers/track_events_worker.rb", nil),
		mixin("TrackEventsWorker", "Sidekiq::Job::Iterable"),
		symbol("Api::BaseController", facts.SymbolClass, "app/controllers/api/base_controller.rb", map[string]any{"rails_component": "controller"}),
		symbol("Api::BaseController#pundit_user", facts.SymbolMethod, "app/controllers/api/base_controller.rb", nil),
		mixin("Api::BaseController", "Pundit::Authorization"),
		symbol("Plain", facts.SymbolClass, "lib/plain.rb", nil),
		symbol("Plain#each_iteration", facts.SymbolMethod, "lib/plain.rb", nil),
	)
	bind(t, store)
	for name, want := range map[string]string{
		"TrackEventsWorker#build_enumerator": MechanismHook,
		"TrackEventsWorker#each_iteration":   MechanismHook,
		"TrackEventsWorker#helper":           "",
		"Api::BaseController#pundit_user":    MechanismHook,
		"Plain#each_iteration":               "",
	} {
		if got := propOf(store, facts.KindSymbol, name, RootProp); got != want {
			t.Errorf("%s root = %q, want %q", name, got, want)
		}
	}
}

// What a superclass includes or is, its subclasses inherit: a controller whose
// base class includes Pundit answers pundit_params_for for the library, a
// worker whose base class is a job runs perform for the queue, and a
// library base class names the hooks it calls on every subclass.
func TestBind_HooksAndJobsAreInheritedThroughTheSuperclassChain(t *testing.T) {
	store := facts.NewStore()
	mixin := func(includer, module string) facts.Fact {
		return facts.Fact{Kind: facts.KindDependency, Name: includer + " -> " + module, File: "app/x.rb", Repo: "app",
			Props: map[string]any{"language": "ruby", "mixin_kind": "include"}, Relations: []facts.Relation{{Kind: facts.RelImplements, Target: module}}}
	}
	store.Add(
		symbol("App::BaseController", facts.SymbolClass, "app/controllers/app/base_controller.rb", map[string]any{"rails_component": "controller"}),
		mixin("App::BaseController", "Pundit::Authorization"),
		symbol("App::Api::NotesController", facts.SymbolClass, "app/controllers/app/api/notes_controller.rb", map[string]any{"rails_component": "controller", "superclass": "App::BaseController"}),
		symbol("App::Api::NotesController#pundit_params_for", facts.SymbolMethod, "app/controllers/app/api/notes_controller.rb", map[string]any{"exported": false}),
		symbol("BaseImportJob", facts.SymbolClass, "app/jobs/base_import_job.rb", map[string]any{"rails_component": "job"}),
		symbol("Imports::CsvJob", facts.SymbolClass, "lib/imports/csv_job.rb", map[string]any{"superclass": "BaseImportJob"}),
		symbol("Imports::CsvJob#perform", facts.SymbolMethod, "lib/imports/csv_job.rb", nil),
		symbol("Api::Scim::V2::GroupsController", facts.SymbolClass, "app/controllers/api/scim/v2/groups_controller.rb", map[string]any{"rails_component": "controller", "superclass": "Scimitar::ActiveRecordBackedResourcesController"}),
		symbol("Api::Scim::V2::GroupsController#storage_class", facts.SymbolMethod, "app/controllers/api/scim/v2/groups_controller.rb", map[string]any{"exported": false}),
		symbol("Loop::A", facts.SymbolClass, "lib/loop/a.rb", map[string]any{"superclass": "Loop::B"}),
		symbol("Loop::B", facts.SymbolClass, "lib/loop/b.rb", map[string]any{"superclass": "Loop::A"}),
		symbol("Loop::A#perform", facts.SymbolMethod, "lib/loop/a.rb", nil),
	)
	bind(t, store)
	for name, want := range map[string]string{
		"App::Api::NotesController#pundit_params_for":   MechanismHook,
		"Imports::CsvJob#perform":                       MechanismJob,
		"Api::Scim::V2::GroupsController#storage_class": MechanismHook,
		"Loop::A#perform":                               "",
	} {
		if got := propOf(store, facts.KindSymbol, name, RootProp); got != want {
			t.Errorf("%s root = %q, want %q", name, got, want)
		}
	}
}

func mixinOf(includer, module string) facts.Fact {
	return facts.Fact{Kind: facts.KindDependency, Name: includer + " -> " + module, File: "app/x.rb", Repo: "app",
		Props: map[string]any{"language": "ruby", "mixin_kind": "include"}, Relations: []facts.Relation{{Kind: facts.RelImplements, Target: module}}}
}

func instantiates(targets ...string) []facts.Relation {
	out := []facts.Relation{}
	for _, t := range targets {
		out = append(out, facts.Relation{Kind: facts.RelInstantiates, Target: t})
	}
	return out
}

// A module's methods are looked up from its includers the way Ruby does, so a
// bare call from a reached controller action reaches the concern's method; the
// callbacks a concern registers in its included block root on every
// framework-bound includer and reach what they call; a module included only
// into plain classes roots nothing.
func TestBind_IncludedModulesAnswerTheirIncludersCalls(t *testing.T) {
	store := facts.NewStore()
	store.Add(
		route("pages#show"),
		symbol("PagesController", facts.SymbolClass, "app/controllers/pages_controller.rb", map[string]any{"rails_component": "controller"}),
		symbol("PagesController#show", facts.SymbolMethod, "app/controllers/pages_controller.rb", nil, calls("from_cdn?")...),
		mixinOf("PagesController", "Cacheable"),
		symbol("Cacheable", facts.SymbolInterface, "app/concerns/cacheable.rb", nil, names("set_headers")...),
		symbol("Cacheable#set_headers", facts.SymbolMethod, "app/concerns/cacheable.rb", nil, calls("surrogate_keys")...),
		symbol("Cacheable#surrogate_keys", facts.SymbolMethod, "app/concerns/cacheable.rb", nil),
		symbol("Cacheable#from_cdn?", facts.SymbolMethod, "app/concerns/cacheable.rb", nil),
		symbol("Cacheable#unused", facts.SymbolMethod, "app/concerns/cacheable.rb", nil),
		symbol("Plain", facts.SymbolClass, "lib/plain.rb", nil),
		mixinOf("Plain", "Helpers"),
		symbol("Helpers", facts.SymbolInterface, "lib/helpers.rb", nil, names("tidy")...),
		symbol("Helpers#tidy", facts.SymbolMethod, "lib/helpers.rb", nil),
	)
	bind(t, store)
	for name, want := range map[string][2]string{
		"Cacheable#set_headers":    {MechanismCallback, ""},
		"Cacheable#surrogate_keys": {"", MechanismCallback},
		"Cacheable#from_cdn?":      {"", MechanismRoute},
		"Cacheable#unused":         {"", ""},
		"Helpers#tidy":             {"", ""},
	} {
		if got := propOf(store, facts.KindSymbol, name, RootProp); got != want[0] {
			t.Errorf("%s root = %q, want %q", name, got, want[0])
		}
		if got := propOf(store, facts.KindSymbol, name, ReachedFromProp); got != want[1] {
			t.Errorf("%s reached_from = %q, want %q", name, got, want[1])
		}
	}
}

// Classes a framework loads by their base class root the methods the library
// calls: a maintenance task's collection and process, an admin resource's
// fields and an admin action's handle, a filter's apply, and an MCP tool's
// singleton call, its base class named relative to its namespace. The same
// method names on a class of no such base root nothing.
func TestBind_FrameworkLoadedClassesRootWhatTheLibraryCalls(t *testing.T) {
	store := facts.NewStore()
	store.Add(
		symbol("Maintenance::BackfillTask", facts.SymbolClass, "app/tasks/maintenance/backfill_task.rb", map[string]any{"superclass": "MaintenanceTasks::Task"}),
		symbol("Maintenance::BackfillTask#collection", facts.SymbolMethod, "app/tasks/maintenance/backfill_task.rb", nil, calls("scope")...),
		symbol("Maintenance::BackfillTask#process", facts.SymbolMethod, "app/tasks/maintenance/backfill_task.rb", nil),
		symbol("Maintenance::BackfillTask#scope", facts.SymbolMethod, "app/tasks/maintenance/backfill_task.rb", nil),
		symbol("Avo::Resources::Thing", facts.SymbolClass, "app/avo/resources/thing.rb", map[string]any{"superclass": "Avo::BaseResource"}),
		symbol("Avo::Resources::Thing#fields", facts.SymbolMethod, "app/avo/resources/thing.rb", nil),
		symbol("Avo::Resources::Thing#actions", facts.SymbolMethod, "app/avo/resources/thing.rb", nil),
		symbol("Avo::Actions::Recheck", facts.SymbolClass, "app/avo/actions/recheck.rb", map[string]any{"superclass": "Avo::BaseAction"}),
		symbol("Avo::Actions::Recheck#handle", facts.SymbolMethod, "app/avo/actions/recheck.rb", nil),
		symbol("Avo::Filters::Status", facts.SymbolClass, "app/avo/filters/status.rb", map[string]any{"superclass": "Avo::Filters::SelectFilter"}),
		symbol("Avo::Filters::Status#apply", facts.SymbolMethod, "app/avo/filters/status.rb", nil),
		symbol("Tools::Application", facts.SymbolClass, "app/tools/application.rb", map[string]any{"superclass": "::MCP::Tool"}),
		symbol("Tools::AddThing", facts.SymbolClass, "app/tools/add_thing.rb", map[string]any{"superclass": "Application"}),
		symbol("Tools::AddThing.call", facts.SymbolFunc, "app/tools/add_thing.rb", nil, calls("existing")...),
		symbol("Tools::AddThing.existing", facts.SymbolFunc, "app/tools/add_thing.rb", nil),
		symbol("Plain", facts.SymbolClass, "lib/plain.rb", nil),
		symbol("Plain#process", facts.SymbolMethod, "lib/plain.rb", nil),
		symbol("Plain.call", facts.SymbolFunc, "lib/plain.rb", nil),
	)
	bind(t, store)
	for name, want := range map[string][2]string{
		"Maintenance::BackfillTask#collection": {MechanismTask, ""},
		"Maintenance::BackfillTask#process":    {MechanismTask, ""},
		"Maintenance::BackfillTask#scope":      {"", MechanismTask},
		"Avo::Resources::Thing#fields":         {MechanismHook, ""},
		"Avo::Resources::Thing#actions":        {MechanismHook, ""},
		"Avo::Actions::Recheck#handle":         {MechanismHook, ""},
		"Avo::Filters::Status#apply":           {MechanismHook, ""},
		"Tools::AddThing.call":                 {MechanismHook, ""},
		"Tools::AddThing.existing":             {"", MechanismHook},
		"Plain#process":                        {"", ""},
		"Plain.call":                           {"", ""},
	} {
		if got := propOf(store, facts.KindSymbol, name, RootProp); got != want[0] {
			t.Errorf("%s root = %q, want %q", name, got, want[0])
		}
		if got := propOf(store, facts.KindSymbol, name, ReachedFromProp); got != want[1] {
			t.Errorf("%s reached_from = %q, want %q", name, got, want[1])
		}
	}
}

// An instance built by reached code runs its initialize, and a view component
// it builds runs the lifecycle the library calls on render, inherited where
// the component does not define it; a component only named as a value is not
// rendered by that and reaches none of its hooks.
func TestBind_RenderedComponentsAreReachedFromTheirRenderer(t *testing.T) {
	store := facts.NewStore()
	store.Add(
		route("cards#show"),
		symbol("CardsController", facts.SymbolClass, "app/controllers/cards_controller.rb", map[string]any{"rails_component": "controller"}),
		symbol("CardsController#show", facts.SymbolMethod, "app/controllers/cards_controller.rb", nil, append(instantiates("Cards::Card", "Report"), calls("Cards::Named")...)...),
		symbol("ApplicationComponent", facts.SymbolClass, "app/components/application_component.rb", map[string]any{"superclass": "ViewComponent::Base", "rails_component": "component"}),
		symbol("ApplicationComponent#render?", facts.SymbolMethod, "app/components/application_component.rb", nil),
		symbol("Cards::Card", facts.SymbolClass, "app/components/cards/card.rb", map[string]any{"superclass": "ApplicationComponent", "rails_component": "component"}),
		symbol("Cards::Card#initialize", facts.SymbolMethod, "app/components/cards/card.rb", nil),
		symbol("Cards::Card#before_render", facts.SymbolMethod, "app/components/cards/card.rb", nil, calls("title")...),
		symbol("Cards::Card#title", facts.SymbolMethod, "app/components/cards/card.rb", nil),
		symbol("Cards::Named", facts.SymbolClass, "app/components/cards/named.rb", map[string]any{"superclass": "ViewComponent::Base", "rails_component": "component"}),
		symbol("Cards::Named#before_render", facts.SymbolMethod, "app/components/cards/named.rb", nil),
		symbol("Report", facts.SymbolClass, "app/models/report.rb", nil),
		symbol("Report#initialize", facts.SymbolMethod, "app/models/report.rb", nil),
		symbol("Report#before_render", facts.SymbolMethod, "app/models/report.rb", nil),
	)
	bind(t, store)
	for name, want := range map[string]string{
		"Cards::Card#initialize":       MechanismRoute,
		"Cards::Card#before_render":    MechanismRoute,
		"Cards::Card#title":            MechanismRoute,
		"ApplicationComponent#render?": MechanismRoute,
		"Cards::Named#before_render":   "",
		"Report#initialize":            MechanismRoute,
		"Report#before_render":         "",
	} {
		if got := propOf(store, facts.KindSymbol, name, ReachedFromProp); got != want {
			t.Errorf("%s reached_from = %q, want %q", name, got, want)
		}
	}
}

// A constant a call names is found the way Ruby finds it, through the
// caller's enclosing namespaces outward, so a tool calling Schemas.build
// inside Tools reaches Tools::Schemas.build; a name no namespace holds stays
// unresolved.
func TestBind_QualifiedCallsResolveThroughTheCallersNamespace(t *testing.T) {
	store := facts.NewStore()
	store.Add(
		symbol("Tools::Base", facts.SymbolClass, "app/tools/base.rb", map[string]any{"superclass": "MCP::Tool"}),
		symbol("Tools::Fetch", facts.SymbolClass, "app/tools/fetch.rb", map[string]any{"superclass": "Base"}),
		symbol("Tools::Fetch.call", facts.SymbolFunc, "app/tools/fetch.rb", nil, calls("Schemas.build", "Presenter", "Missing.thing")...),
		symbol("Tools::Schemas", facts.SymbolInterface, "app/tools/schemas.rb", nil),
		symbol("Tools::Schemas.build", facts.SymbolFunc, "app/tools/schemas.rb", nil),
		symbol("Tools::Presenter", facts.SymbolClass, "app/tools/presenter.rb", nil),
		symbol("Schemas.build", facts.SymbolFunc, "lib/schemas.rb", nil),
	)
	bind(t, store)
	for name, want := range map[string]string{
		"Tools::Schemas.build": MechanismHook,
		"Tools::Presenter":     MechanismHook,
		"Schemas.build":        "",
	} {
		if got := propOf(store, facts.KindSymbol, name, ReachedFromProp); got != want {
			t.Errorf("%s reached_from = %q, want %q", name, got, want)
		}
	}
}

// A root says which runtime runs it where the library that loads it fixes the
// process: a route's action and the admin resources, admin actions, filters,
// channel hooks and MCP tools a request serves run in web; a job's perform, an
// iterable job's hooks and a maintenance task's steps, which the library's own
// job runs, run in job. A mailer, a migration, a callback and a hook whose
// library runs it wherever its includer runs say nothing. What a root reaches
// lists the runtimes of the roots that reach it, and a root of no known
// runtime adds none.
func TestBind_RootsNameTheRuntimeTheirLibraryRunsThemIn(t *testing.T) {
	store := facts.NewStore()
	store.Add(
		route("things#index"),
		symbol("ThingsController", facts.SymbolClass, "app/controllers/things_controller.rb", map[string]any{"rails_component": "controller"}),
		symbol("ThingsController#index", facts.SymbolMethod, "app/controllers/things_controller.rb", nil, calls("Shared.run", "WebOnly.run")...),
		symbol("ThingsController#pundit_user", facts.SymbolMethod, "app/controllers/things_controller.rb", nil),
		mixinOf("ThingsController", "Pundit::Authorization"),
		symbol("ImportJob", facts.SymbolClass, "app/jobs/import_job.rb", map[string]any{"rails_component": "job"}),
		symbol("ImportJob#perform", facts.SymbolMethod, "app/jobs/import_job.rb", nil, calls("Shared.run")...),
		symbol("Shared", facts.SymbolClass, "app/services/shared.rb", nil),
		symbol("Shared.run", facts.SymbolMethod, "app/services/shared.rb", nil),
		symbol("WebOnly", facts.SymbolClass, "app/services/web_only.rb", nil),
		symbol("WebOnly.run", facts.SymbolMethod, "app/services/web_only.rb", nil),
		symbol("TrackEventsWorker", facts.SymbolClass, "app/workers/track_events_worker.rb", nil),
		symbol("TrackEventsWorker#each_iteration", facts.SymbolMethod, "app/workers/track_events_worker.rb", nil),
		mixinOf("TrackEventsWorker", "Sidekiq::Job::Iterable"),
		symbol("Maintenance::BackfillTask", facts.SymbolClass, "app/tasks/maintenance/backfill_task.rb", map[string]any{"superclass": "MaintenanceTasks::Task"}),
		symbol("Maintenance::BackfillTask#process", facts.SymbolMethod, "app/tasks/maintenance/backfill_task.rb", nil, calls("tidy")...),
		symbol("Maintenance::BackfillTask#tidy", facts.SymbolMethod, "app/tasks/maintenance/backfill_task.rb", nil),
		symbol("Avo::Resources::Thing", facts.SymbolClass, "app/avo/resources/thing.rb", map[string]any{"superclass": "Avo::BaseResource"}),
		symbol("Avo::Resources::Thing#fields", facts.SymbolMethod, "app/avo/resources/thing.rb", nil),
		symbol("Avo::Actions::Recheck", facts.SymbolClass, "app/avo/actions/recheck.rb", map[string]any{"superclass": "Avo::BaseAction"}),
		symbol("Avo::Actions::Recheck#handle", facts.SymbolMethod, "app/avo/actions/recheck.rb", nil),
		symbol("Avo::Filters::Status", facts.SymbolClass, "app/avo/filters/status.rb", map[string]any{"superclass": "Avo::Filters::SelectFilter"}),
		symbol("Avo::Filters::Status#apply", facts.SymbolMethod, "app/avo/filters/status.rb", nil),
		symbol("Tools::AddThing", facts.SymbolClass, "app/tools/add_thing.rb", map[string]any{"superclass": "MCP::Tool"}),
		symbol("Tools::AddThing.call", facts.SymbolFunc, "app/tools/add_thing.rb", nil, calls("Tools::Presenter")...),
		symbol("Tools::Presenter", facts.SymbolClass, "app/tools/presenter.rb", nil),
		symbol("ChatChannel", facts.SymbolClass, "app/channels/chat_channel.rb", map[string]any{"rails_component": "channel"}),
		symbol("ChatChannel#subscribed", facts.SymbolMethod, "app/channels/chat_channel.rb", nil),
		symbol("CandidateMailer", facts.SymbolClass, "app/mailers/candidate_mailer.rb", map[string]any{"rails_component": "mailer"}),
		symbol("CandidateMailer#welcome", facts.SymbolMethod, "app/mailers/candidate_mailer.rb", nil, calls("WebOnly.run")...),
		symbol("AddIndexToThings", facts.SymbolClass, "db/migrate/20260821000000_add_index_to_things.rb", map[string]any{"superclass": "ActiveRecord::Migration[8.0]"}),
		symbol("AddIndexToThings#change", facts.SymbolMethod, "db/migrate/20260821000000_add_index_to_things.rb", nil, calls("Quiet.run")...),
		symbol("Quiet", facts.SymbolClass, "app/services/quiet.rb", nil),
		symbol("Quiet.run", facts.SymbolMethod, "app/services/quiet.rb", nil),
	)
	bind(t, store)
	for name, want := range map[string][2]string{
		"ThingsController#index":            {"web", ""},
		"ThingsController#pundit_user":      {"", ""},
		"ImportJob#perform":                 {"job", ""},
		"Shared.run":                        {"", "job,web"},
		"WebOnly.run":                       {"", "web"},
		"TrackEventsWorker#each_iteration":  {"job", ""},
		"Maintenance::BackfillTask#process": {"job", ""},
		"Maintenance::BackfillTask#tidy":    {"", "job"},
		"Avo::Resources::Thing#fields":      {"web", ""},
		"Avo::Actions::Recheck#handle":      {"web", ""},
		"Avo::Filters::Status#apply":        {"web", ""},
		"Tools::AddThing.call":              {"web", ""},
		"Tools::Presenter":                  {"", "web"},
		"ChatChannel#subscribed":            {"web", ""},
		"CandidateMailer#welcome":           {"", ""},
		"AddIndexToThings#change":           {"", ""},
		"Quiet.run":                         {"", ""},
	} {
		if got := propOf(store, facts.KindSymbol, name, "root_runtime"); got != want[0] {
			t.Errorf("%s root_runtime = %q, want %q", name, got, want[0])
		}
		if got := propOf(store, facts.KindSymbol, name, "reached_runtime"); got != want[1] {
			t.Errorf("%s reached_runtime = %q, want %q", name, got, want[1])
		}
	}
	if got := propOf(store, facts.KindSymbol, "Quiet.run", ReachedFromProp); got != MechanismTask {
		t.Errorf("a root of no known runtime still reaches: reached_from = %q", got)
	}
	if got := propOf(store, facts.KindRoute, "/things", "root_runtime"); got != "web" {
		t.Errorf("a bound route's root_runtime = %q, want %q", got, "web")
	}
}
