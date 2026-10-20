package server_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeRepo writes a small Go repository named name under a fresh temp dir,
// with files given relative to its root, and returns its path.
func writeRepo(t *testing.T, name string, files map[string]string) string {
	t.Helper()
	repo := filepath.Join(t.TempDir(), name)
	for rel, body := range files {
		path := filepath.Join(repo, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return repo
}

const alphaLaw = "components:\n" +
	"  - {name: api, match: [internal/api/**]}\n" +
	"  - {name: store, match: [internal/store/**]}\n" +
	"rules:\n" +
	"  - {id: api-avoids-store, forbid: api, to: store, via: calls, because: api goes through the service}\n"

// unionSession snapshots two repositories into one server, alpha first and
// beta appended, so beta is the member a last-restored fallback answers for.
// Both hold internal/api/api.go; only alpha declares a law over it.
func unionSession(t *testing.T) (*session, string, string) {
	t.Helper()
	alpha := writeRepo(t, "alpha", map[string]string{
		"go.mod":                  "module alpha\n\ngo 1.21\n",
		"internal/store/store.go": "package store\n\nfunc Put() {}\n",
		"internal/api/api.go":     "package api\n\nimport \"alpha/internal/store\"\n\nfunc Serve() { store.Put() }\n",
		"enola-intent.yaml":       alphaLaw,
	})
	beta := writeRepo(t, "beta", map[string]string{
		"go.mod":              "module beta\n\ngo 1.21\n",
		"internal/api/api.go": "package api\n\nfunc Serve() {}\n",
	})
	eng, cfg := newTestEngine(t)
	s := connect(t, eng, cfg)
	if res := s.call(t, "generate_snapshot", map[string]any{"repo_path": alpha}); res.IsError {
		t.Fatalf("generate alpha: %s", text(res))
	}
	if res := s.call(t, "generate_snapshot", map[string]any{"repo_path": beta, "append": true}); res.IsError {
		t.Fatalf("append beta: %s", text(res))
	}
	return s, alpha, beta
}

func refusedNaming(t *testing.T, what, got string, isError bool, members ...string) {
	t.Helper()
	if !isError {
		t.Errorf("%s: answered instead of refusing:\n%s", what, got)
		return
	}
	for _, m := range members {
		if !strings.Contains(got, m) {
			t.Errorf("%s: the refusal does not name member %q:\n%s", what, m, got)
		}
	}
}

// In a union, a tool that answers for one repository takes it by name, reads
// it from a path that carries it, and refuses by name where neither says.
// Before this, plan_check answered a bare path for the member restored last,
// confidently and for the wrong repository.
func TestUnionAnchoredToolsTakeTheRepositoryOrRefuse(t *testing.T) {
	s, _, _ := unionSession(t)

	res := s.call(t, "plan_check", map[string]any{"paths": []string{"internal/api/api.go"}})
	refusedNaming(t, "plan_check on a bare path", text(res), res.IsError, "alpha", "beta")

	for _, args := range []map[string]any{
		{"repo": "alpha", "paths": []string{"internal/api/api.go"}},
		{"paths": []string{"alpha/internal/api/api.go"}},
	} {
		res := s.call(t, "plan_check", args)
		if res.IsError {
			t.Fatalf("plan_check %v refused: %s", args, text(res))
		}
		var report struct {
			Repo    string `json:"repo"`
			Targets []struct {
				Target        string `json:"target"`
				NoRuleGoverns bool   `json:"no_rule_governs"`
				Components    []struct {
					Component string `json:"component"`
				} `json:"components"`
			} `json:"targets"`
		}
		if err := json.Unmarshal([]byte(text(res)), &report); err != nil {
			t.Fatalf("plan_check %v: %v\n%s", args, err, text(res))
		}
		if report.Repo != "alpha" || len(report.Targets) != 1 {
			t.Fatalf("plan_check %v answered for %q with %d targets, want alpha with one:\n%s", args, report.Repo, len(report.Targets), text(res))
		}
		tr := report.Targets[0]
		if tr.Target != "internal/api/api.go" || tr.NoRuleGoverns || len(tr.Components) != 1 || tr.Components[0].Component != "api" {
			t.Errorf("plan_check %v: want alpha's api component governing internal/api/api.go, got:\n%s", args, text(res))
		}
	}

	res = s.call(t, "plan_check", map[string]any{"repo": "gamma", "paths": []string{"internal/api/api.go"}})
	refusedNaming(t, "plan_check naming no member", text(res), res.IsError, "gamma", "alpha", "beta")

	res = s.call(t, "set_baseline", map[string]any{})
	refusedNaming(t, "set_baseline with no repo", text(res), res.IsError, "alpha", "beta")
	res = s.call(t, "diff_snapshot", map[string]any{})
	refusedNaming(t, "diff_snapshot with no repo", text(res), res.IsError, "alpha", "beta")
	res = s.call(t, "snapshot_receipt", map[string]any{})
	refusedNaming(t, "snapshot_receipt with no repo", text(res), res.IsError, "alpha", "beta")

	if res := s.call(t, "set_baseline", map[string]any{"repo": "alpha"}); res.IsError {
		t.Fatalf("set_baseline alpha: %s", text(res))
	}
	res = s.call(t, "diff_snapshot", map[string]any{"repo": "alpha"})
	if res.IsError || strings.Contains(text(res), "No pinned baseline") {
		t.Errorf("diff_snapshot for alpha must find alpha's pin; got IsError=%v:\n%s", res.IsError, text(res))
	}

	res = s.call(t, "snapshot_receipt", map[string]any{"repo": "beta", "output_mode": "full"})
	if res.IsError {
		t.Fatalf("snapshot_receipt beta: %s", text(res))
	}
	var receipt struct {
		RepoPath string `json:"repo_path"`
	}
	if err := json.Unmarshal([]byte(text(res)), &receipt); err != nil {
		t.Fatalf("snapshot_receipt: %v\n%s", err, text(res))
	}
	if filepath.Base(receipt.RepoPath) != "beta" {
		t.Errorf("snapshot_receipt for beta describes %q", receipt.RepoPath)
	}
}

// With one repository loaded nothing changes: no repo is needed, the loaded
// one's name is accepted, and another name is refused rather than ignored.
func TestSingleRepoAnchoredToolsAnswerAsBefore(t *testing.T) {
	s := startInMemory(t)
	s.snapshot(t)
	label := filepath.Base(s.repo)

	plain := s.call(t, "plan_check", map[string]any{"paths": []string{"pkg/a/a.go"}})
	named := s.call(t, "plan_check", map[string]any{"repo": label, "paths": []string{"pkg/a/a.go"}})
	if plain.IsError || named.IsError || text(plain) != text(named) {
		t.Errorf("naming the one loaded repository must change nothing:\n%s\n---\n%s", text(plain), text(named))
	}
	res := s.call(t, "plan_check", map[string]any{"repo": "elsewhere", "paths": []string{"pkg/a/a.go"}})
	refusedNaming(t, "plan_check naming another repository", text(res), res.IsError, "elsewhere", label)
}
