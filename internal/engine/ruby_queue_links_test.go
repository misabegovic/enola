package engine_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/enola-labs/enola/internal/facts"
	"github.com/enola-labs/enola/pkg/bootstrap"
)

// One repository is named default, the queue both declare and enqueue onto, so the
// Kafka signal's owner lookup (a topic's leading segment names the repository that
// owns it) would resolve shop's queue to that repository if a Ruby queue were ever
// read as a Kafka topic. The test is only as strong as that trap.
func TestRubyQueueNeverLinks(t *testing.T) {
	base := t.TempDir()
	for _, repo := range []string{"default", "shop"} {
		writeRubyQueueRepo(t, filepath.Join(base, repo))
	}
	eng, _, err := bootstrap.NewEngine(bootstrap.Options{ConfigPath: filepath.Join(t.TempDir(), "no-such-config.yaml")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng.GenerateSnapshot(context.Background(), filepath.Join(base, "default"), false); err != nil {
		t.Fatalf("snapshot default: %v", err)
	}
	if _, err := eng.GenerateSnapshot(context.Background(), filepath.Join(base, "shop"), true); err != nil {
		t.Fatalf("snapshot shop: %v", err)
	}
	all := eng.Store().All()

	seen := map[string]bool{}
	for _, f := range all {
		if f.Kind == facts.KindStorage && f.Name == "default" {
			seen[f.Repo+" "+f.PropString(facts.PropSource)] = true
		}
		for key := range f.Props {
			if strings.HasPrefix(key, "messaging_contract") || strings.HasPrefix(key, "messaging_implementation") {
				t.Errorf("%s %s in %s carries %s: a queue bound a messaging contract", f.Kind, f.Name, f.Repo, key)
			}
		}
		if f.Kind == facts.KindDependency && propListHas(f.Props["via"], facts.ViaKafka) {
			t.Errorf("a kafka edge formed on a queue name: %s %+v", f.Name, f.Props)
		}
		if f.Kind == facts.KindService {
			for _, rel := range f.Relations {
				if rel.Kind == facts.RelDependsOn && (rel.Target == "default" || rel.Target == "shop") {
					t.Errorf("service %s depends on %s through a shared queue name", f.Name, rel.Target)
				}
			}
		}
	}
	for _, want := range []string{
		"default " + facts.MessagingSourceRubyJobQueue, "default " + facts.MessagingSourceRubyEnqueue,
		"shop " + facts.MessagingSourceRubyJobQueue, "shop " + facts.MessagingSourceRubyEnqueue,
	} {
		if !seen[want] {
			t.Errorf("no %s fact on the queue named default: the trap was never set (saw %v)", want, seen)
		}
	}
}

func writeRubyQueueRepo(t *testing.T, root string) {
	t.Helper()
	for rel, content := range map[string]string{
		"Gemfile":                             "source \"https://rubygems.org\"\ngem \"rails\"\n",
		"config/application.rb":               "module App\n  class Application < Rails::Application\n  end\nend\n",
		"app/jobs/sync_job.rb":                "class SyncJob < ApplicationJob\n  queue_as :default\n\n  def perform\n  end\nend\n",
		"app/controllers/syncs_controller.rb": "class SyncsController\n  def create\n    SyncJob.perform_later\n  end\nend\n",
	} {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func propListHas(v any, want string) bool {
	switch list := v.(type) {
	case []string:
		for _, s := range list {
			if s == want {
				return true
			}
		}
	case []any:
		for _, s := range list {
			if s == want {
				return true
			}
		}
	case string:
		return list == want
	}
	return false
}
