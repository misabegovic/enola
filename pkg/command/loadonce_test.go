package command

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/enola-labs/enola/internal/config"
	"github.com/enola-labs/enola/internal/engine"
	"github.com/enola-labs/enola/internal/facts"
	"github.com/enola-labs/enola/pkg/bootstrap"
)

// countingExtractor detects every repository, emits nothing, and counts its runs.
// Only a plugin.FileOwner gets an extractor-cache key, so a plain extractor runs on
// every generate, and a check that did not generate never runs it.
type countingExtractor struct{ runs *int }

func (c countingExtractor) Name() string                { return "counting" }
func (c countingExtractor) Detect(string) (bool, error) { return true, nil }
func (c countingExtractor) Extract(context.Context, string, []string) ([]facts.Fact, error) {
	*c.runs++
	return nil, nil
}

// repoEngine is a fresh engine over repoPaths with the counting extractor enabled,
// the way a new process would build one: nothing published until it reads or
// generates.
func repoEngine(t *testing.T, repoPaths []string, runs *int) *bootstrap.Engine {
	t.Helper()
	cfg := "repo: " + repoPaths[0] + "\nextractors: [go, counting]\n"
	cfgPath := filepath.Join(t.TempDir(), "mcp-arch.yaml")
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	eng, _, err := bootstrap.NewEngine(bootstrap.Options{ConfigPath: cfgPath})
	if err != nil {
		t.Fatal(err)
	}
	eng.RegisterExtractor(countingExtractor{runs: runs})
	return eng
}

// generateAndPin writes every member's snapshot the way --generate does for a
// cluster (linking deferred to the last turn) and pins the anchor's.
func generateAndPin(t *testing.T, repoPaths []string) {
	t.Helper()
	var runs int
	eng := repoEngine(t, repoPaths, &runs)
	for i, repo := range repoPaths {
		eng.SetDeferLinking(i < len(repoPaths)-1)
		if _, err := eng.GenerateSnapshot(context.Background(), repo, i > 0); err != nil {
			t.Fatal(err)
		}
	}
	for _, repo := range repoPaths {
		if err := eng.WriteArtifacts(repo); err != nil {
			t.Fatal(err)
		}
	}
	if err := eng.SetBaseline(repoPaths[0]); err != nil {
		t.Fatal(err)
	}
}

func countSnapshotReads(t *testing.T) *int {
	t.Helper()
	n := 0
	load, restore := loadSnapshotDir, restoreSnapshot
	loadSnapshotDir = func(dir string) (*facts.Snapshot, error) {
		n++
		return load(dir)
	}
	restoreSnapshot = func(e *bootstrap.Engine, cfg *config.Config) bool {
		n++
		return restore(e, cfg)
	}
	t.Cleanup(func() { loadSnapshotDir, restoreSnapshot = load, restore })
	return &n
}

func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	prev := os.Stderr
	os.Stderr = w
	done := make(chan string)
	go func() {
		out, _ := io.ReadAll(r)
		done <- string(out)
	}()
	fn()
	os.Stderr = prev
	_ = w.Close()
	return <-done
}

func editGoFile(t *testing.T, repo string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, "pkg", "a", "a.go"), []byte("package a\n\nfunc A() {}\n\nfunc A2() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// ledger and plan answer from one read of the snapshot. Each restored the
// directory it had just loaded, only to learn how far the tree had moved since,
// which the meta of the first read already answers: on rtk's 6.7 MB of facts that
// second read was 87 to 93 ms of a 228 ms ledger. The staleness they report must
// not move with it, before an edit or after one.
func TestLedgerAndPlanReadTheSnapshotOnce(t *testing.T) {
	repo := writeGoRepo(t)
	generateAndPin(t, []string{repo})
	reads := countSnapshotReads(t)

	for _, edited := range []bool{false, true} {
		if edited {
			editGoFile(t, repo)
		}
		var runs int

		*reads = 0
		res, err := testRunner().ledgerAnswer(context.Background(), target{engine: repoEngine(t, []string{repo}, &runs), repoPaths: []string{repo}}, repo, "", false, true)
		if err != nil {
			t.Fatal(err)
		}
		if res.IsError {
			t.Fatal(resultText(res))
		}
		if *reads != 1 {
			t.Errorf("edited=%v: constraints ledger read the snapshot %d times, want 1", edited, *reads)
		}
		staleness, _ := resultField(res, "staleness").(string)
		if got := strings.Contains(staleness, "1 modified"); got != edited {
			t.Errorf("edited=%v: ledger staleness %q", edited, staleness)
		}

		*reads = 0
		info, measured, err := testRunner().planSnapshot(repoEngine(t, []string{repo}, &runs), repo, true)
		if err != nil {
			t.Fatal(err)
		}
		if *reads != 1 {
			t.Errorf("edited=%v: plan read the snapshot %d times, want 1", edited, *reads)
		}
		if len(measured) == 0 || info.GeneratedAt == "" {
			t.Errorf("edited=%v: plan measured %d facts from a snapshot generated %q", edited, len(measured), info.GeneratedAt)
		}
		if got := strings.Contains(info.Staleness, "1 modified"); got != edited {
			t.Errorf("edited=%v: plan staleness %q", edited, info.Staleness)
		}
		if runs != 0 {
			t.Errorf("edited=%v: a read-only report ran an extractor %d times", edited, runs)
		}
	}
}

// check on a tree that has not moved since its snapshot grades the snapshot on
// disk and runs no extractor, and says so in one line; once a file moves it
// regenerates as it always did, and says nothing new. On rtk the re-extraction of
// an unchanged tree was 341 to 366 ms of every check.
func TestCheckReusesACurrentSnapshotAndRegeneratesAfterAnEdit(t *testing.T) {
	repo := writeGoRepo(t)
	repos := []string{repo}
	generateAndPin(t, repos)
	var runs int
	eng := repoEngine(t, repos, &runs)
	baseDir := engine.ResolveBaselineDir(eng.OutputDir(repo), "pinned")

	stderr := captureStderr(t, func() {
		if err := testRunner().currentSnapshot(context.Background(), eng, repos, baseDir, false); err != nil {
			t.Fatal(err)
		}
	})
	if runs != 0 {
		t.Errorf("an unchanged tree ran the extractors %d times, want 0", runs)
	}
	if !strings.Contains(stderr, "graded without regenerating") || strings.Count(stderr, "without regenerating") != 1 {
		t.Errorf("a reuse is named in exactly one line on stderr, got:\n%s", stderr)
	}
	if eng.Snapshot() == nil || eng.Store().Count() == 0 {
		t.Fatal("the reused snapshot was not published")
	}
	if !strings.Contains(stderr, eng.OutputDir(repo)) || !strings.Contains(stderr, eng.MetaFor(repo).GeneratedAt) {
		t.Errorf("the reuse line names neither the snapshot's directory nor its time:\n%s", stderr)
	}

	editGoFile(t, repo)
	runs = 0
	eng = repoEngine(t, repos, &runs)
	stderr = captureStderr(t, func() {
		if err := testRunner().currentSnapshot(context.Background(), eng, repos, baseDir, false); err != nil {
			t.Fatal(err)
		}
	})
	if runs != 1 {
		t.Errorf("an edited tree ran the extractors %d times, want the one regenerate", runs)
	}
	if strings.Contains(stderr, "without regenerating") {
		t.Errorf("a regenerate claimed a reuse:\n%s", stderr)
	}
	if !factNamed(eng.Store(), "A2") {
		t.Error("the regenerate does not hold the edit")
	}
}

func factNamed(store *facts.Store, suffix string) bool {
	for _, f := range store.FactsRef() {
		if strings.HasSuffix(f.Name, suffix) {
			return true
		}
	}
	return false
}

// The reused state is the regenerated state: the same facts byte for byte, the
// same findings, and every member's own provenance, for one repository and for a
// cluster whose union every member's directory holds. A cluster's --generate defers
// linking to its last turn and check's regenerate does not, so this is also the
// claim pin's reuse rests on, that the two produce one union.
func TestCheckReuseIsTheRegenerate(t *testing.T) {
	for _, members := range []int{1, 2} {
		repos := make([]string, members)
		for i := range repos {
			repos[i] = writeGoRepo(t)
		}
		generateAndPin(t, repos)
		baseDir := engine.ResolveBaselineDir(filepath.Join(repos[0], ".enola"), "pinned")

		var reusedRuns, regenRuns int
		reused := repoEngine(t, repos, &reusedRuns)
		captureStderr(t, func() {
			if err := testRunner().currentSnapshot(context.Background(), reused, repos, baseDir, false); err != nil {
				t.Fatal(err)
			}
		})
		if reusedRuns != 0 {
			t.Fatalf("%d members: the unchanged cluster was regenerated", members)
		}
		regen := repoEngine(t, repos, &regenRuns)
		for i, repo := range repos {
			if _, err := regen.GenerateSnapshot(context.Background(), repo, i > 0); err != nil {
				t.Fatal(err)
			}
		}

		var a, b bytes.Buffer
		if err := reused.Store().WriteJSONL(&a); err != nil {
			t.Fatal(err)
		}
		if err := regen.Store().WriteJSONL(&b); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(a.Bytes(), b.Bytes()) {
			t.Errorf("%d members: the reused facts differ from the regenerated ones (%d vs %d bytes)", members, a.Len(), b.Len())
		}
		ia, _ := json.Marshal(reused.Snapshot().Insights)
		ib, _ := json.Marshal(regen.Snapshot().Insights)
		if !bytes.Equal(ia, ib) {
			t.Errorf("%d members: the reused findings differ from the regenerated ones:\n%s\n%s", members, ia, ib)
		}
		for _, repo := range repos {
			ma, mb := reused.MetaFor(repo), regen.MetaFor(repo)
			// The meta lists file hashes in map order, which moves between any two
			// generates of one tree, so they are compared as the set they are.
			for _, m := range []*facts.SnapshotMeta{&ma, &mb} {
				m.FileHashes = append([]facts.FileHash(nil), m.FileHashes...)
				sort.Slice(m.FileHashes, func(i, j int) bool { return m.FileHashes[i].Path < m.FileHashes[j].Path })
			}
			for _, field := range []struct {
				name string
				a, b any
			}{
				{"repo_path", ma.RepoPath, mb.RepoPath},
				{"repo_label", ma.RepoLabel, mb.RepoLabel},
				{"git", ma.Git, mb.Git},
				{"extractors", ma.Extractors, mb.Extractors},
				{"explainers", ma.Explainers, mb.Explainers},
				{"providers", ma.Providers, mb.Providers},
				{"config_hash", ma.ConfigHash, mb.ConfigHash},
				{"extractor_version", ma.ExtractorVersion, mb.ExtractorVersion},
				{"file_hashes", ma.FileHashes, mb.FileHashes},
				{"census", ma.Census, mb.Census},
			} {
				ja, _ := json.Marshal(field.a)
				jb, _ := json.Marshal(field.b)
				if !bytes.Equal(ja, jb) {
					t.Errorf("%d members: %s of %s differs between the reuse and the regenerate:\n%s\n%s", members, field.name, filepath.Base(repo), ja, jb)
				}
			}
		}
	}
}

// Pin's gate says the snapshot describes the tree. A check also reads two things
// pin does not: the git state it asks for the files the change touched, and the
// time it compares against the baseline's. Where either would read differently
// from a regenerate's, the answer would move, so the check regenerates instead.
func TestCheckRegeneratesWhereTheReusedAnswerWouldMove(t *testing.T) {
	setMetaTime := func(t *testing.T, dir string, at time.Time) {
		t.Helper()
		path := filepath.Join(dir, "snapshot.meta.json")
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		m["generated_at"] = at.UTC().Format(time.RFC3339)
		out, err := json.MarshalIndent(m, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		// A new file renamed over the name, as the engine writes every artifact: a
		// pin links the output dir's files into baseline/, so writing into one name
		// would move the other side's time with it.
		tmp := path + ".tmp"
		if err := os.WriteFile(tmp, out, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(tmp, path); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	cases := []struct {
		name          string
		snapshot, pin time.Time
		reuse         bool
	}{
		{"pinned from the snapshot on disk", now, now, true},
		{"stale against both", now, now.Add(-100 * time.Hour), true},
		{"fresh against the snapshot, stale against today", now.Add(-24 * time.Hour), now.Add(-84 * time.Hour), false},
		{"the snapshot predates the pin, today does not", now.Add(-time.Hour), now.Add(-30 * time.Minute), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := writeGoRepo(t)
			repos := []string{repo}
			generateAndPin(t, repos)
			outDir := filepath.Join(repo, ".enola")
			baseDir := engine.ResolveBaselineDir(outDir, "pinned")
			setMetaTime(t, outDir, tc.snapshot)
			setMetaTime(t, baseDir, tc.pin)

			var runs int
			eng := repoEngine(t, repos, &runs)
			captureStderr(t, func() {
				if err := testRunner().currentSnapshot(context.Background(), eng, repos, baseDir, false); err != nil {
					t.Fatal(err)
				}
			})
			if reused := runs == 0; reused != tc.reuse {
				t.Errorf("reused=%v, want %v", reused, tc.reuse)
			}
		})
	}

	t.Run("HEAD moved and no file did", func(t *testing.T) {
		if _, err := exec.LookPath("git"); err != nil {
			t.Skip("git is not installed")
		}
		repo := writeGoRepo(t)
		git := func(args ...string) {
			t.Helper()
			cmd := exec.Command("git", append([]string{"-C", repo, "-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"}, args...)...)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("git %v: %v\n%s", args, err, out)
			}
		}
		git("init", "-q")
		if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte(".enola/\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		git("add", ".")
		git("commit", "-q", "-m", "first")
		repos := []string{repo}
		generateAndPin(t, repos)
		baseDir := engine.ResolveBaselineDir(filepath.Join(repo, ".enola"), "pinned")

		var runs int
		captureStderr(t, func() {
			if err := testRunner().currentSnapshot(context.Background(), repoEngine(t, repos, &runs), repos, baseDir, false); err != nil {
				t.Fatal(err)
			}
		})
		if runs != 0 {
			t.Fatalf("a committed, unchanged tree was regenerated")
		}
		git("commit", "-q", "--allow-empty", "-m", "second")
		captureStderr(t, func() {
			if err := testRunner().currentSnapshot(context.Background(), repoEngine(t, repos, &runs), repos, baseDir, false); err != nil {
				t.Fatal(err)
			}
		})
		if runs != 1 {
			t.Errorf("HEAD moved since the snapshot and the check reused it (%d extractor runs)", runs)
		}
	})
}

// check --write persists a snapshot and rotates previous/, so it always
// generates one: a reuse would write nothing and rotate nothing.
func TestCheckWriteAlwaysRegenerates(t *testing.T) {
	repo := writeGoRepo(t)
	repos := []string{repo}
	generateAndPin(t, repos)
	var runs int
	eng := repoEngine(t, repos, &runs)
	captureStderr(t, func() {
		if err := testRunner().currentSnapshot(context.Background(), eng, repos, engine.ResolveBaselineDir(eng.OutputDir(repo), "pinned"), true); err != nil {
			t.Fatal(err)
		}
	})
	if runs != 1 {
		t.Errorf("check --write ran the extractors %d times, want 1", runs)
	}
}

// The whole command, twice over one unchanged tree: once reusing the snapshot on
// disk and once regenerating because its recorded hashes were made to disagree
// with the tree. The two verdicts are the same document but for the time the
// current side was generated, and so are their exit codes.
func TestCheckVerdictIsTheSameReusedOrRegenerated(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the binary")
	}
	bin := filepath.Join(t.TempDir(), "enola")
	if out, err := exec.Command("go", "build", "-o", bin, "../../cmd/enola").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	repo := writeGoRepo(t)
	// A cycle, so the verdict carries a finding on both sides rather than
	// comparing two empty documents.
	if err := os.WriteFile(filepath.Join(repo, "pkg", "a", "a.go"), []byte("package a\n\nimport \"testmod/pkg/b\"\n\nfunc A() { b.B() }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) (int, string, string) {
		t.Helper()
		cmd := exec.Command(bin, args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "ENOLA_NO_UPDATE_CHECK=1")
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		code := 0
		if exit, ok := err.(*exec.ExitError); ok {
			code = exit.ExitCode()
		} else if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		return code, stdout.String(), stderr.String()
	}
	if code, _, stderr := run("--generate", "."); code != 0 {
		t.Fatalf("generate: %s", stderr)
	}
	if code, _, stderr := run("baseline", "pin", "."); code != 0 {
		t.Fatalf("pin: %s", stderr)
	}

	withoutCurrentTime := func(t *testing.T, doc string) string {
		t.Helper()
		var v map[string]any
		if err := json.Unmarshal([]byte(doc), &v); err != nil {
			t.Fatalf("not a verdict document: %v\n%s", err, doc)
		}
		d, ok := v["diff"].(map[string]any)
		if !ok {
			t.Fatalf("no diff in the verdict:\n%s", doc)
		}
		if _, ok := d["current_generated_at"]; !ok {
			t.Fatalf("no current_generated_at in the diff:\n%s", doc)
		}
		delete(d, "current_generated_at")
		out, _ := json.Marshal(v)
		return string(out)
	}

	for _, args := range [][]string{
		{"check", "--json", "--fail-on=cycles", "."},
		{"check", "--format", "json", "."},
	} {
		reusedCode, reused, reusedErr := run(args...)
		if !strings.Contains(reusedErr, "graded without regenerating") {
			t.Fatalf("%v: an unchanged tree was not graded from its snapshot:\n%s", args, reusedErr)
		}

		metaPath := filepath.Join(repo, ".enola", "snapshot.meta.json")
		keep, err := os.ReadFile(metaPath)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(metaPath, bytes.Replace(keep, []byte(`"hash": "`), []byte(`"hash": "0`), 1), 0o644); err != nil {
			t.Fatal(err)
		}
		regenCode, regen, regenErr := run(args...)
		if err := os.WriteFile(metaPath, keep, 0o644); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(regenErr, "without regenerating") {
			t.Fatalf("%v: a snapshot whose hashes disagree with the tree was reused:\n%s", args, regenErr)
		}

		if reusedCode != regenCode {
			t.Errorf("%v: exit %d reused, %d regenerated", args, reusedCode, regenCode)
		}
		if a, b := withoutCurrentTime(t, reused), withoutCurrentTime(t, regen); a != b {
			t.Errorf("%v: the verdicts differ beyond current_generated_at:\nreused:      %s\nregenerated: %s", args, a, b)
		}
	}

	editGoFile(t, repo)
	if _, _, stderr := run("check", "--json", "--fail-on=cycles", "."); strings.Contains(stderr, "without regenerating") {
		t.Errorf("an edited tree was graded from a snapshot that predates the edit:\n%s", stderr)
	}
}
