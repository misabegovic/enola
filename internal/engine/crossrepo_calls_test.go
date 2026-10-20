package engine_test

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/enola-labs/enola/internal/facts"
	"github.com/enola-labs/enola/pkg/bootstrap"
)

// writeAtlasAndJobs lays out a Rust workspace whose pic crate depends on
// nothing, with a strict law saying so, beside a Rails application with one
// job on the default queue. pic calls Walk::default(), derived from Default
// and so measured as no method, and s.kind.atlas(), its own method on a
// receiver the walker cannot type, named like the workspace's repository.
func writeAtlasAndJobs(t *testing.T) (atlas, jobs string) {
	t.Helper()
	root := t.TempDir()
	atlas, jobs = filepath.Join(root, "atlas"), filepath.Join(root, "jobs")
	writeFile(t, filepath.Join(atlas, "Cargo.toml"),
		"[package]\nname = \"atlas\"\nversion = \"0.0.1\"\nedition = \"2021\"\n\n[workspace]\nmembers = [\"crates/pic\"]\n\n[dependencies]\npic = { path = \"crates/pic\" }\n")
	writeFile(t, filepath.Join(atlas, "src", "main.rs"), "fn main() {\n    println!(\"{}\", pic::write());\n}\n")
	writeFile(t, filepath.Join(atlas, "crates", "pic", "Cargo.toml"),
		"[package]\nname = \"pic\"\nversion = \"0.0.1\"\nedition = \"2021\"\n\n[dependencies]\n")
	writeFile(t, filepath.Join(atlas, "crates", "pic", "src", "lib.rs"), `#[derive(Default)]
pub struct Walk {
    pub out: String,
}

#[derive(Clone, Copy)]
pub enum Kind {
    Plain,
    Part,
}

impl Kind {
    pub fn atlas(self) -> bool {
        matches!(self, Kind::Part)
    }
}

pub struct Shape {
    pub kind: Kind,
}

pub fn write() -> String {
    let walk = Walk::default();
    walk.out
}

pub fn placed(s: &Shape) -> bool {
    s.kind.atlas()
}
`)
	writeFile(t, filepath.Join(atlas, "enola", "constraints", "the-crate.yaml"), `components:
  - name: entry
    match: ["src/main.rs"]
  - name: pic
    match: ["crates/pic/src/**"]
rules:
  - id: pic-calls-nothing-else
    allow: pic
    only: [pic]
    via: calls
    mode: strict
    because: pic has no dependencies, so Cargo refuses any call out of it.
`)
	writeFile(t, filepath.Join(jobs, "Gemfile"), "source \"https://rubygems.org\"\ngem \"rails\"\n")
	writeFile(t, filepath.Join(jobs, "app", "jobs", "application_job.rb"), "class ApplicationJob < ActiveJob::Base\nend\n")
	writeFile(t, filepath.Join(jobs, "app", "jobs", "report_job.rb"),
		"class ReportJob < ApplicationJob\n  queue_as :default\n\n  def perform\n  end\nend\n")
	return atlas, jobs
}

func strictBreaches(insights []facts.Insight) []string {
	var out []string
	for _, in := range insights {
		if strings.HasPrefix(in.Title, "Strict constraint pic-calls-nothing-else violated") {
			out = append(out, in.Title)
		}
	}
	return out
}

// Generated alone the workspace keeps its law; appended to a Rails
// application it must keep it too, since nothing in pic changed. The union
// holds a queue named default and a service node named atlas, and neither is
// anything pic calls: no breach, and neither call carries the other fact's id.
func TestCrossRepoCalls_AUnionAddsNoBreachToALawKeptAlone(t *testing.T) {
	atlas, jobs := writeAtlasAndJobs(t)

	alone, _, err := bootstrap.NewEngine(bootstrap.Options{})
	if err != nil {
		t.Fatal(err)
	}
	snap, err := alone.GenerateSnapshot(context.Background(), atlas, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := strictBreaches(snap.Insights); len(got) != 0 {
		t.Fatalf("alone: breaches = %v, want none", got)
	}

	eng, _, err := bootstrap.NewEngine(bootstrap.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng.GenerateSnapshot(context.Background(), jobs, false); err != nil {
		t.Fatal(err)
	}
	snap, err = eng.GenerateSnapshot(context.Background(), atlas, true)
	if err != nil {
		t.Fatal(err)
	}

	var queue, service bool
	for _, f := range eng.Store().FactsRef() {
		queue = queue || (f.Kind == facts.KindStorage && f.Name == "default" && f.Repo == "jobs")
		service = service || (f.Kind == facts.KindService && f.Name == "atlas")
	}
	if !queue || !service {
		t.Fatalf("precondition: the union must hold the queue (%v) and the service node (%v) the calls are named like", queue, service)
	}
	if got := strictBreaches(snap.Insights); len(got) != 0 {
		t.Errorf("union: breaches = %v, want none", got)
	}

	var buf bytes.Buffer
	if err := eng.Store().WriteJSONL(&buf); err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var f struct {
			Name      string `json:"name"`
			Relations []struct {
				Kind     string `json:"kind"`
				Target   string `json:"target"`
				TargetID string `json:"target_id"`
			} `json:"relations"`
		}
		if err := json.Unmarshal([]byte(line), &f); err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(f.Name, "crates/pic/src.") {
			continue
		}
		for _, r := range f.Relations {
			if r.Kind == facts.RelCalls && (r.Target == "default" || r.Target == "atlas") && r.TargetID != "" {
				t.Errorf("%s calls %s with target_id %s; the name meets nothing the call reaches", f.Name, r.Target, r.TargetID)
			}
		}
	}
}
