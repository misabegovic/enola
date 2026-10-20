package server_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/enola-labs/enola/internal/server"
)

// plan_check builds the store it reads the contract from once per snapshot and
// declaration, and again only when either moves. It used to copy every fact
// into a fresh store on every call: 25.1 s a call at 2.3M facts.
func TestPlanCheckKeepsItsContractStore(t *testing.T) {
	builds := server.CountContractBuilds(t)
	s, alpha := lawSession(t)
	ask := func() string {
		t.Helper()
		res := s.call(t, "plan_check", map[string]any{"paths": []string{"internal/api/api.go"}})
		if res.IsError {
			t.Fatal(text(res))
		}
		return text(res)
	}

	first := ask()
	if second := ask(); second != first {
		t.Errorf("a kept store answered differently:\n%s\n---\n%s", first, second)
	}
	if *builds != 1 {
		t.Errorf("two calls over one snapshot and one declaration built the store %d times, want 1", *builds)
	}

	law := alphaLaw + "  - {id: store-stays-quiet, forbid: store, to: api, via: calls, because: a law added since the snapshot}\n"
	if err := os.WriteFile(filepath.Join(alpha, "enola-intent.yaml"), []byte(law), 0o644); err != nil {
		t.Fatal(err)
	}
	if out := ask(); !strings.Contains(out, "store-stays-quiet") {
		t.Errorf("an edited declaration was not read:\n%s", out)
	}
	ask()
	if *builds != 2 {
		t.Errorf("an edited declaration built the store %d times in all, want 2", *builds)
	}

	if err := os.WriteFile(filepath.Join(alpha, "internal", "store", "more.go"), []byte("package store\n\nfunc More() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if res := s.call(t, "generate_snapshot", map[string]any{"repo_path": alpha}); res.IsError {
		t.Fatal(text(res))
	}
	ask()
	ask()
	if *builds != 3 {
		t.Errorf("a new snapshot built the store %d times in all, want 3", *builds)
	}
}

// The fan-in plan_check reads a blast radius from is kept with the store, so a
// second call reads it rather than scanning every fact for each member: at
// 2.3M facts the scan was 99% of a 20 s call once the store was kept.
func TestPlanCheckKeepsItsFanIn(t *testing.T) {
	builds := server.CountContractBuilds(t)
	indexes := server.CountFanInBuilds(t)
	s, alpha := lawSession(t)
	ask := func() string {
		t.Helper()
		res := s.call(t, "plan_check", map[string]any{"paths": []string{"internal/store/store.go"}})
		if res.IsError {
			t.Fatal(text(res))
		}
		return text(res)
	}

	first := ask()
	if !strings.Contains(first, `"fan_in": 1`) || !strings.Contains(first, "api.Serve") {
		t.Errorf("the store's caller is missing from its fan-in:\n%s", first)
	}
	if second := ask(); second != first {
		t.Errorf("a kept fan-in answered differently:\n%s\n---\n%s", first, second)
	}
	if *indexes != 1 {
		t.Errorf("two calls over one snapshot and one declaration indexed the fan-in %d times, want 1", *indexes)
	}

	law := alphaLaw + "  - {id: store-stays-quiet, forbid: store, to: api, via: calls, because: a law added since the snapshot}\n"
	if err := os.WriteFile(filepath.Join(alpha, "enola-intent.yaml"), []byte(law), 0o644); err != nil {
		t.Fatal(err)
	}
	ask()
	ask()
	if *indexes != *builds || *builds != 2 {
		t.Errorf("an edited declaration indexed the fan-in %d times over %d stores, want 2 and 2", *indexes, *builds)
	}
}
