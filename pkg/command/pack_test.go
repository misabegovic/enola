package command

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/enola-labs/enola/internal/diff"
	"github.com/enola-labs/enola/pkg/check"
	"github.com/enola-labs/enola/pkg/pack"
)

// dirsAndFilesDigest hashes every file under dir with its path, and names every
// directory, so two digests are equal only when nothing under dir was written,
// renamed, created or removed, an empty directory included.
func dirsAndFilesDigest(t *testing.T, dir string) string {
	t.Helper()
	var lines []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		if d.IsDir() {
			lines = append(lines, "d "+rel)
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		lines = append(lines, "f "+rel+" "+hex.EncodeToString(sum[:]))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(lines)
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return hex.EncodeToString(sum[:])
}

// TestContextPackWritesNothing: a run of pack under a scratch HOME leaves that HOME
// and the snapshot directory byte-identical, file for file.
func TestContextPackWritesNothing(t *testing.T) {
	bin, home, repo := twinFixture(t)
	snapshotDir := filepath.Join(repo, ".enola")
	homeBefore, snapBefore := dirsAndFilesDigest(t, home), dirsAndFilesDigest(t, snapshotDir)
	code, stdout, stderr := runTwin(t, bin, home, repo, "pack", "--budget", "2000", "--names", "Test,Alpha", "--paths", "pkg/a,main.go", ".")
	if code != 0 {
		t.Fatalf("pack: exit %d\n%s", code, stderr)
	}
	if !strings.Contains(stdout, "test TestHelperDoubles") || !strings.Contains(stdout, "symbol pkg/a.Alpha") {
		t.Fatalf("pack did not answer the test and the symbol:\n%s", stdout)
	}
	if got := dirsAndFilesDigest(t, home); got != homeBefore {
		t.Error("pack wrote under HOME")
	}
	if got := dirsAndFilesDigest(t, snapshotDir); got != snapBefore {
		t.Error("pack wrote into the snapshot directory")
	}
}

// TestContextPackRefusesWithoutSnapshot: with no snapshot the answer is refused
// whole, with the reason, exit 2; with no budget it is a usage error, exit 2.
func TestContextPackRefusesWithoutSnapshot(t *testing.T) {
	bin, home, _ := twinFixture(t)
	bare := filepath.Join(t.TempDir(), "go_sample")
	copyFixture(t, filepath.Join("..", "..", "internal", "engine", "testdata", "repos", "go_sample"), bare)
	code, stdout, stderr := runTwin(t, bin, home, bare, "pack", "--budget", "2000", "--paths", "main.go", ".")
	if code != 2 || stdout != "" || !strings.Contains(stderr, "go_sample holds no snapshot") {
		t.Errorf("no snapshot: exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	if _, err := os.Stat(filepath.Join(bare, ".enola")); !os.IsNotExist(err) {
		t.Errorf("a refused pack created a snapshot directory: %v", err)
	}
	code, _, stderr = runTwin(t, bin, home, bare, "pack", "--paths", "main.go", ".")
	if code != 2 || !strings.Contains(stderr, "--budget") {
		t.Errorf("no budget: exit %d, stderr %q", code, stderr)
	}
}

// TestContextPackStaleFile: after one named file is edited, pack names it as
// moved with no range and still answers the file that did not move.
func TestContextPackStaleFile(t *testing.T) {
	bin, home, repo := twinFixture(t)
	edited := filepath.Join(repo, "pkg", "a", "a_test.go")
	data, err := os.ReadFile(edited)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(edited, append([]byte("// edited after the snapshot\n"), data...), 0o644); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := runTwin(t, bin, home, repo, "pack", "--json", "--budget", "8000", "--names", "Test", "--paths", "pkg/a/a_test.go,pkg/a/a_ext_test.go", ".")
	if code != 0 {
		t.Fatalf("pack: exit %d\n%s", code, stderr)
	}
	var report pack.Report
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("%v\n%s", err, stdout)
	}
	if len(report.Moved) != 1 || report.Moved[0].File != "pkg/a/a_test.go" {
		t.Errorf("moved = %+v, want pkg/a/a_test.go alone", report.Moved)
	}
	var answered []string
	for _, it := range report.Items {
		if it.File == "pkg/a/a_test.go" {
			t.Errorf("the edited file got a range: %+v", it)
		}
		answered = append(answered, it.Kind+" "+it.Name)
	}
	if len(answered) != 2 || answered[0] != "test TestGammaFromExternalPackage" || answered[1] != "file " {
		t.Errorf("answered %v, want the external test and its file", answered)
	}
}

// pack refuses on exactly the caveats check treats as blocking, less the provider
// set, which is the list snapshotIsCurrent refuses a pin reuse on.
func TestContextPackRefusesWhatAPinReuseRefuses(t *testing.T) {
	var all diff.Comparability
	for _, kind := range []diff.WarningKind{diff.WarnDifferentRepo, diff.WarnRepoLabel, diff.WarnVersionMismatch,
		diff.WarnExtractorSet, diff.WarnProviderSet, diff.WarnExplainerSet, diff.WarnIgnoreGlobs, diff.WarnInvertedPair,
		diff.WarnStaleBaseline, diff.WarnPreReceipt, diff.WarnUnclassified, diff.WarnUnionMembership} {
		all.Kinds = append(all.Kinds, kind)
	}
	var want []string
	for _, kind := range check.BlockingKinds(all) {
		if kind != diff.WarnProviderSet {
			want = append(want, string(kind))
		}
	}
	var got []string
	for _, kind := range pack.RefusingKinds() {
		got = append(got, string(kind))
	}
	sort.Strings(want)
	sort.Strings(got)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("pack refuses on %v, a pin reuse on %v", got, want)
	}
}
