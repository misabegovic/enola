package rubyenqueue

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/enola-labs/enola/internal/facts"
)

func class(repo, name, dir string) facts.Fact {
	return facts.Fact{Kind: facts.KindSymbol, Name: name, File: dir + "/x.rb", Line: 1, Repo: repo,
		Props:     map[string]any{"symbol_kind": facts.SymbolClass, "language": "ruby"},
		Relations: []facts.Relation{{Kind: facts.RelDeclares, Target: dir}}}
}

func queue(repo, name, class, messaging, file string, line int) facts.Fact {
	return facts.Fact{Kind: facts.KindStorage, Name: name, File: file, Line: line, Repo: repo,
		Props: map[string]any{
			"storage_kind": facts.StorageKindTopic, facts.PropMessaging: messaging,
			facts.PropMessagingRole: facts.MessagingRoleConsumer, facts.PropMessagingOperation: facts.MessagingOperationSubscribe,
			facts.PropSource: facts.MessagingSourceRubyJobQueue, "code_symbol": class, "language": "ruby",
		},
		Relations: []facts.Relation{{Kind: facts.RelDeclares, Target: "app/jobs"}}}
}

func caller(repo, name, file, dir string, line int, targets ...string) facts.Fact {
	f := facts.Fact{Kind: facts.KindSymbol, Name: name, File: file, Line: line, Repo: repo,
		Props:     map[string]any{"symbol_kind": facts.SymbolMethod, "language": "ruby"},
		Relations: []facts.Relation{{Kind: facts.RelDeclares, Target: dir}}}
	for _, target := range targets {
		f.Relations = append(f.Relations, facts.Relation{Kind: facts.RelCalls, Target: target})
	}
	return f
}

func producers(all []facts.Fact) map[string]facts.Fact {
	out := map[string]facts.Fact{}
	for _, f := range all {
		if f.Kind == facts.KindStorage && f.PropString(facts.PropSource) == facts.MessagingSourceRubyEnqueue {
			out[f.Repo+" "+f.PropString("code_symbol")+" "+f.PropString("job")] = f
		}
	}
	return out
}

func record(t *testing.T, all []facts.Fact, repo string) facts.Fact {
	t.Helper()
	for _, f := range all {
		if f.Kind == facts.KindExtraction && f.Name == recordName && f.Repo == repo {
			return f
		}
	}
	t.Fatalf("no %s record for %s", recordName, repo)
	return facts.Fact{}
}

func shop() []facts.Fact {
	return []facts.Fact{
		class("shop", "WelcomeEmailJob", "app/jobs"),
		queue("shop", "mailers", "WelcomeEmailJob", "activejob", "app/jobs/welcome_email_job.rb", 2),
		class("shop", "BillingWorker", "app/workers"),
		queue("shop", "critical", "BillingWorker", "sidekiq", "app/workers/billing_worker.rb", 3),
		class("shop", "CleanupJob", "app/jobs"),
		class("shop", "TwiceJob", "app/jobs"),
		queue("shop", "one", "TwiceJob", "activejob", "app/jobs/twice_job.rb", 2),
		queue("shop", "two", "TwiceJob", "activejob", "app/jobs/twice_job.rb", 3),
		class("shop", "Admin::NotifyJob", "app/jobs/admin"),
		queue("shop", "admin", "Admin::NotifyJob", "activejob", "app/jobs/admin/notify_job.rb", 2),
		class("shop", "Reports::NotifyJob", "app/jobs/reports"),
		queue("shop", "reports", "Reports::NotifyJob", "activejob", "app/jobs/reports/notify_job.rb", 2),
		class("shop", "Admin::ExportJob", "app/jobs/admin"),
		queue("shop", "exports", "Admin::ExportJob", "activejob", "app/jobs/admin/export_job.rb", 2),
		class("shop", "Reports::ExportJob", "app/jobs/reports"),
		queue("shop", "report_exports", "Reports::ExportJob", "activejob", "app/jobs/reports/export_job.rb", 2),
		class("shop", "ChargeJob", "app/jobs"),
		queue("shop", "charges", "ChargeJob", "activejob", "app/jobs/charge_job.rb", 2),
		class("shop", "Billing::ChargeJob", "app/jobs/billing"),
		queue("shop", "billing_charges", "Billing::ChargeJob", "activejob", "app/jobs/billing/charge_job.rb", 2),
		caller("shop", "SignupsController#create", "app/controllers/signups_controller.rb", "app/controllers", 4,
			"WelcomeEmailJob.perform_later", "::BillingWorker.perform_async", "CleanupJob.perform_later",
			"TwiceJob.perform_in", "GhostJob.perform_at", "job.perform_later", "Jobs.enqueue", "User.find"),
		caller("shop", "Admin::UsersController#update", "app/controllers/admin/users_controller.rb", "app/controllers/admin", 9,
			"NotifyJob.perform_later"),
		caller("shop", "Admin::SweepJob#perform", "app/jobs/admin/sweep_job.rb", "app/jobs/admin", 3,
			"ExportJob.perform_later"),
		caller("shop", "Billing::InvoicesController#create", "app/controllers/billing/invoices_controller.rb", "app/controllers/billing", 5,
			"ChargeJob.perform_later"),
		class("other", "WelcomeEmailJob", "app/jobs"),
		queue("other", "elsewhere", "WelcomeEmailJob", "activejob", "app/jobs/welcome_email_job.rb", 2),
	}
}

func bind(t *testing.T, input []facts.Fact) (*facts.Store, []string) {
	t.Helper()
	store := facts.NewStore()
	store.Add(input...)
	var before []string
	for _, f := range store.FactsRef() {
		line, _ := json.Marshal(f)
		before = append(before, string(line))
	}
	if err := New().Bind(context.Background(), store); err != nil {
		t.Fatal(err)
	}
	return store, before
}

func TestRubyEnqueueBinder_ResolvedConstantGivesOneProducerFact(t *testing.T) {
	store, _ := bind(t, shop())
	got := producers(store.FactsRef())
	for key, w := range map[string]struct {
		queue, messaging, call, file, dir string
		line                              int
	}{
		"shop SignupsController#create WelcomeEmailJob": {"mailers", "activejob", "perform_later", "app/controllers/signups_controller.rb", "app/controllers", 4},
		"shop SignupsController#create BillingWorker":   {"critical", "sidekiq", "perform_async", "app/controllers/signups_controller.rb", "app/controllers", 4},
		"shop Admin::SweepJob#perform Admin::ExportJob": {"exports", "activejob", "perform_later", "app/jobs/admin/sweep_job.rb", "app/jobs/admin", 3},
	} {
		f, ok := got[key]
		if !ok {
			t.Errorf("no producer fact for %s; got %v", key, keys(got))
			continue
		}
		if f.Name != w.queue || f.File != w.file || f.Line != w.line || f.Repo != "shop" {
			t.Errorf("%s = %s at %s:%d in %s", key, f.Name, f.File, f.Line, f.Repo)
		}
		for prop, value := range map[string]string{
			"storage_kind":               facts.StorageKindTopic,
			facts.PropMessaging:          w.messaging,
			facts.PropMessagingRole:      facts.MessagingRoleProducer,
			facts.PropMessagingOperation: facts.MessagingOperationPublish,
			"call":                       w.call,
			"language":                   "ruby",
		} {
			if f.PropString(prop) != value {
				t.Errorf("%s %s = %q, want %q", key, prop, f.PropString(prop), value)
			}
		}
		if len(f.Relations) != 1 || !f.HasRelation(facts.RelDeclares, w.dir) {
			t.Errorf("%s relations = %+v, want only declares %s", key, f.Relations, w.dir)
		}
	}
	if len(got) != 3 {
		t.Errorf("got %d producer facts, want 3: %v", len(got), keys(got))
	}
}

func TestRubyEnqueueBinder_UnresolvedAndQueuelessAreCounted(t *testing.T) {
	store, _ := bind(t, shop())
	shopRecord := record(t, store.FactsRef(), "shop")
	want := "class_with_several_queues=1,class_without_queue=1,constant_ambiguous=2,constant_unknown=1,enqueue_by_argument=1,receiver_not_a_constant=1"
	if got := shopRecord.Props["unresolved_macros"]; got != want {
		t.Fatalf("unresolved_macros = %v, want %s", got, want)
	}
	entry := shopRecord.Props["edge_coverage"].([]map[string]any)[0]
	if entry["resolved"] != 3 || entry["unresolved"] != 7 {
		t.Fatalf("edge_coverage = %+v, want 3 resolved and 7 left out", entry)
	}
	for _, f := range store.FactsRef() {
		if f.Kind == facts.KindExtraction && f.Name == recordName && f.Repo == "other" {
			t.Fatalf("a repository with no enqueue reports nothing: %+v", f)
		}
	}
}

func TestRubyEnqueueBinder_ChangesNoExistingFactOrRelation(t *testing.T) {
	store, before := bind(t, shop())
	all := store.FactsRef()
	var kept []string
	for _, f := range all {
		if f.PropString(facts.PropSource) == facts.MessagingSourceRubyEnqueue || (f.Kind == facts.KindExtraction && f.Name == recordName) {
			continue
		}
		line, _ := json.Marshal(f)
		kept = append(kept, string(line))
	}
	if len(kept) != len(before) {
		t.Fatalf("existing facts %d before, %d after", len(before), len(kept))
	}
	for i := range before {
		if kept[i] != before[i] {
			t.Fatalf("fact %d changed:\nbefore %s\nafter  %s", i, before[i], kept[i])
		}
	}
	if err := New().Bind(context.Background(), store); err != nil {
		t.Fatal(err)
	}
	if again := store.FactsRef(); len(again) != len(all) {
		t.Fatalf("a second bind changed the fact count from %d to %d", len(all), len(again))
	}
}

func TestRubyEnqueueBinder_ResolvesWithinTheCallersRepository(t *testing.T) {
	store, _ := bind(t, shop())
	for _, f := range producers(store.FactsRef()) {
		if f.Name == "elsewhere" {
			t.Fatalf("an enqueue resolved into another repository's class: %+v", f)
		}
	}
}

func TestRubyEnqueueBinder_RefusesWhereTheNestingDecides(t *testing.T) {
	store, _ := bind(t, shop())
	for key := range producers(store.FactsRef()) {
		switch key {
		case "shop Billing::InvoicesController#create ChargeJob", "shop Billing::InvoicesController#create Billing::ChargeJob":
			t.Errorf("ChargeJob from inside Billing names Billing::ChargeJob under nested modules and ChargeJob under a compact class name; a producer was guessed: %s", key)
		case "shop Admin::UsersController#update Admin::NotifyJob", "shop Admin::UsersController#update Reports::NotifyJob":
			t.Errorf("NotifyJob has two declarers no directory decides between; a producer was guessed: %s", key)
		}
	}
}

func keys(m map[string]facts.Fact) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
