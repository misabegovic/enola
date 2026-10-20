package rubyextractor

import (
	"testing"

	"github.com/enola-labs/enola/internal/facts"
)

func queueFacts(ff []facts.Fact) []facts.Fact {
	var out []facts.Fact
	for _, f := range ff {
		if f.Kind == facts.KindStorage && f.PropString("storage_kind") == facts.StorageKindTopic {
			out = append(out, f)
		}
	}
	return out
}

func TestRubyJobQueueDeclared_LiteralForms(t *testing.T) {
	src := `class WelcomeEmailJob < ApplicationJob
  queue_as :mailers
end

class DigestJob < ApplicationJob
  queue_as "digests"
end

class QuotedJob < ApplicationJob
  queue_as :"quoted"
end

class BillingWorker
  include Sidekiq::Job
  sidekiq_options queue: "critical", retry: 3
end

class LegacyWorker
  include Sidekiq::Worker
  sidekiq_options :queue => :legacy
end

module Billing
  class ChargeJob < ApplicationJob
    self.queue_as 'billing'
  end
end
`
	ff, counts := extractFileASTCounted([]byte(src), "app/jobs/all_jobs.rb", true, true)
	want := map[string]struct {
		messaging, symbol string
		line              int
	}{
		"mailers":  {"activejob", "WelcomeEmailJob", 2},
		"digests":  {"activejob", "DigestJob", 6},
		"quoted":   {"activejob", "QuotedJob", 10},
		"critical": {"sidekiq", "BillingWorker", 15},
		"legacy":   {"sidekiq", "LegacyWorker", 20},
		"billing":  {"activejob", "Billing::ChargeJob", 25},
	}
	got := queueFacts(ff)
	if len(got) != len(want) {
		t.Fatalf("got %d queue facts, want %d: %+v", len(got), len(want), got)
	}
	for _, f := range got {
		w, ok := want[f.Name]
		if !ok {
			t.Errorf("unexpected queue fact %q", f.Name)
			continue
		}
		if f.File != "app/jobs/all_jobs.rb" || f.Line != w.line {
			t.Errorf("%s at %s:%d, want app/jobs/all_jobs.rb:%d", f.Name, f.File, f.Line, w.line)
		}
		for key, value := range map[string]string{
			facts.PropMessaging:          w.messaging,
			facts.PropMessagingRole:      facts.MessagingRoleConsumer,
			facts.PropMessagingOperation: facts.MessagingOperationSubscribe,
			facts.PropSource:             facts.MessagingSourceRubyJobQueue,
			"code_symbol":                w.symbol,
			"language":                   "ruby",
		} {
			if f.PropString(key) != value {
				t.Errorf("%s %s = %q, want %q", f.Name, key, f.PropString(key), value)
			}
		}
		if !f.HasRelation(facts.RelDeclares, "app/jobs") {
			t.Errorf("%s must be declared in its directory: %+v", f.Name, f.Relations)
		}
	}
	if counts.declared != len(want) || counts.total() != 0 {
		t.Fatalf("counts = %+v, want %d declared and nothing left out", counts, len(want))
	}
}

func TestRubyJobQueueDeclared_WhateverTheComponent(t *testing.T) {
	src := `class Mailman
  include Sidekiq::Job
  sidekiq_options queue: :mail
end
`
	for _, isRails := range []bool{true, false} {
		ff, _ := extractFileASTCounted([]byte(src), "lib/mailman.rb", isRails, true)
		got := queueFacts(ff)
		if len(got) != 1 || got[0].Name != "mail" || got[0].PropString("code_symbol") != "Mailman" {
			t.Fatalf("isRails=%v: a worker outside app/jobs still declares its queue, got %+v", isRails, got)
		}
	}
}

func TestRubyJobQueueDeclared_UnreadFormsYieldNothingAndAreCounted(t *testing.T) {
	src := `class PriorityJob < ApplicationJob
  queue_as do
    arguments.first ? :urgent : :low
  end
end

class BraceJob < ApplicationJob
  queue_as { :low }
end

class MethodJob < ApplicationJob
  queue_as queue_for(Rails.env)
end

class ConstantJob < ApplicationJob
  queue_as QUEUE
end

class InterpolatedJob < ApplicationJob
  queue_as "#{Rails.env}_mailers"
end

class ConstantWorker
  include Sidekiq::Job
  sidekiq_options queue: SOME_QUEUE
end

class CleanupJob < ApplicationJob
  def perform
  end
end

class RetryOnlyWorker
  include Sidekiq::Job
  sidekiq_options retry: 3
end

class MonthlyReportJob < ReportJob
end

class ConditionalJob < ApplicationJob
  queue_as :x if Rails.env.production?
end

module Queued
  extend ActiveSupport::Concern
  included do
    queue_as :shared
  end
end
`
	ff, counts := extractFileASTCounted([]byte(src), "app/jobs/unread.rb", true, true)
	if got := queueFacts(ff); len(got) != 0 {
		t.Fatalf("no queue may be read from these forms, got %+v", got)
	}
	want := map[string]int{
		queueNotLiteral:       6, // block, brace block, method call, constant, interpolation, sidekiq constant
		queueOutsideClassBody: 2, // under a modifier, inside a concern's included block
		jobWithoutQueue:       3, // CleanupJob, RetryOnlyWorker, MonthlyReportJob (a job by its directory)
	}
	for reason, n := range want {
		if counts.misses[reason] != n {
			t.Errorf("%s = %d, want %d (all misses %v)", reason, counts.misses[reason], n, counts.misses)
		}
	}
	if counts.declared != 0 || counts.total() != 11 {
		t.Errorf("counts = %+v, want 0 declared and 11 left out", counts)
	}

	plain := "class PlainService\n  def call\n  end\nend\n"
	if _, counts := extractFileASTCounted([]byte(plain), "lib/plain_service.rb", true, true); counts.total() != 0 {
		t.Errorf("a class that is no job is not a job without a queue: %+v", counts)
	}
}

func TestRubyJobQueueDeclared_NoImpliedDefault(t *testing.T) {
	src := `class CleanupJob < ApplicationJob
  def perform
  end
end
`
	ff, _ := extractFileASTCounted([]byte(src), "app/jobs/cleanup_job.rb", true, true)
	for _, f := range ff {
		if f.Kind == facts.KindStorage && f.Name == "default" {
			t.Fatalf("a job that names no queue must not be given one: %+v", f)
		}
	}
}

func TestRubyJobQueueDeclared_CountedOnTheExtractionRecord(t *testing.T) {
	ff := extractRepo(t, map[string]string{
		"Gemfile":                       "source \"https://rubygems.org\"\n",
		"config/application.rb":         "module App\n  class Application < Rails::Application\n  end\nend\n",
		"app/jobs/welcome_email_job.rb": "class WelcomeEmailJob < ApplicationJob\n  queue_as :mailers\nend\n",
		"app/jobs/priority_job.rb":      "class PriorityJob < ApplicationJob\n  queue_as { :low }\nend\n",
		"app/jobs/cleanup_job.rb":       "class CleanupJob < ApplicationJob\nend\n",
	})
	record, ok := extractionFact(ff, "ruby:job-queues")
	if !ok {
		t.Fatalf("no ruby:job-queues record: %+v", ff)
	}
	if got := record.Props["unresolved_macros"]; got != "job_without_queue=1,queue_not_literal=1" {
		t.Fatalf("unresolved_macros = %v", got)
	}
	entry := record.Props["edge_coverage"].([]map[string]any)[0]
	if entry["resolved"] != 1 || entry["unresolved"] != 2 {
		t.Fatalf("edge_coverage = %+v, want 1 read and 2 left out", entry)
	}
	if got := queueFacts(ff); len(got) != 1 || got[0].Name != "mailers" {
		t.Fatalf("queue facts = %+v", got)
	}
}
