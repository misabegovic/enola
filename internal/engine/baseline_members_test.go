package engine_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/enola-labs/enola/internal/diff"
	"github.com/enola-labs/enola/internal/engine"
	"github.com/enola-labs/enola/internal/facts"
)

// A multi-repo pin records each member's provenance beside its artifacts, and a
// member can then be graded against the pin by replaying it: restore the pinned
// union, re-read only that member, and compare against the member's own recorded
// meta. Both sides are unions built the same way, so the comparison needs no new
// semantics, and the untouched member's slice stays byte-identical.
func TestSetBaseline_RecordsMembers_AndAMemberReplayGrades(t *testing.T) {
	repoA := freezeTestRepo(t)
	repoB := freezeTestRepo(t)

	eng := freezeTestEngine(t)
	ctx := context.Background()
	if _, err := eng.GenerateSnapshot(ctx, repoA, false); err != nil {
		t.Fatalf("generate A: %v", err)
	}
	if _, err := eng.GenerateSnapshot(ctx, repoB, true); err != nil {
		t.Fatalf("append B: %v", err)
	}
	if err := eng.WriteArtifacts(repoA); err != nil {
		t.Fatalf("write artifacts: %v", err)
	}
	if err := eng.SetBaseline(repoA); err != nil {
		t.Fatalf("pin: %v", err)
	}

	baseDir := engine.ResolveBaselineDir(eng.OutputDir(repoA), "pinned")
	members, err := engine.LoadBaselineMembers(baseDir)
	if err != nil {
		t.Fatalf("load members: %v", err)
	}
	if len(members) != 2 {
		t.Fatalf("members = %d, want 2 (%v)", len(members), members)
	}
	absB, _ := filepath.Abs(repoB)
	bm, ok := members[absB]
	if !ok {
		t.Fatalf("no member meta for %s in %v", absB, members)
	}
	labelA, labelB := filepath.Base(repoA), filepath.Base(repoB)
	if bm.RepoLabel != labelB {
		t.Fatalf("member label = %q, want %q", bm.RepoLabel, labelB)
	}
	pinnedACount := eng.Store().CountByRepo(labelA)
	if pinnedACount == 0 {
		t.Fatal("precondition: repo A must contribute facts")
	}

	// The member changes after the pin.
	writeFile(t, filepath.Join(repoB, "extra.go"), "package main\n\nfunc ReplayExtra() {}\n")

	// A fresh engine replays the pin and re-reads only the member.
	replay := freezeTestEngine(t)
	repoPaths := map[string]string{}
	for path, m := range members {
		repoPaths[m.RepoLabel] = path
	}
	if err := replay.RestoreFromDir(baseDir, repoPaths, ""); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if _, err := replay.GenerateSnapshot(ctx, repoB, true); err != nil {
		t.Fatalf("re-read member: %v", err)
	}

	base, err := engine.LoadSnapshotDir(baseDir)
	if err != nil {
		t.Fatalf("load base: %v", err)
	}
	base.Meta = bm
	snap := replay.Snapshot()
	current := &facts.Snapshot{Meta: replay.MetaFor(repoB), Facts: replay.Store().FactsRef(), Insights: snap.Insights}

	d := diff.Compute(base, current)
	if len(d.Comparability.Warnings) != 0 {
		t.Fatalf("a replayed member grade must be comparable, got warnings: %+v", d.Comparability.Warnings)
	}
	var sawExtra bool
	for _, f := range d.FactsAdded {
		if strings.Contains(f.Name, "ReplayExtra") {
			sawExtra = true
		}
		if f.Repo == labelA {
			t.Fatalf("the untouched member's slice moved: added %+v", f)
		}
	}
	for _, f := range d.FactsRemoved {
		if f.Repo == labelA {
			t.Fatalf("the untouched member's slice moved: removed %+v", f)
		}
	}
	if !sawExtra {
		t.Fatalf("the member's change must appear in the delta; added = %d fact(s)", len(d.FactsAdded))
	}
	if got := replay.Store().CountByRepo(labelA); got != pinnedACount {
		t.Fatalf("repo A slice: %d facts after replay, %d at pin", got, pinnedACount)
	}
}

// A single-repo pin records its one member, and a pin with no manifest at all
// (one taken by a build that predates member receipts) reads as unrecorded,
// never as an empty set that looks recorded.
func TestLoadBaselineMembers_SingleRepoAndAbsent(t *testing.T) {
	repo := freezeTestRepo(t)
	eng := freezeTestEngine(t)
	if _, err := eng.GenerateSnapshot(context.Background(), repo, false); err != nil {
		t.Fatal(err)
	}
	if err := eng.WriteArtifacts(repo); err != nil {
		t.Fatal(err)
	}
	if err := eng.SetBaseline(repo); err != nil {
		t.Fatal(err)
	}
	baseDir := engine.ResolveBaselineDir(eng.OutputDir(repo), "pinned")
	members, err := engine.LoadBaselineMembers(baseDir)
	if err != nil || len(members) != 1 {
		t.Fatalf("a single-repo pin records its one member, got (%v, %v)", members, err)
	}

	if err := os.Remove(filepath.Join(baseDir, engine.BaselineMembersFile)); err != nil {
		t.Fatal(err)
	}
	members, err = engine.LoadBaselineMembers(baseDir)
	if err != nil || members != nil {
		t.Fatalf("absent members = (%v, %v), want (nil, nil)", members, err)
	}
}
