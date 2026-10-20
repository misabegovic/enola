package command

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/enola-labs/enola/pkg/bootstrap"
	mcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// twinFixture builds the binary, copies go_sample into a scratch directory with a
// scratch HOME, and generates its snapshot once, the way a user would.
func twinFixture(t *testing.T) (bin, home, repo string) {
	t.Helper()
	if testing.Short() {
		t.Skip("builds the binary")
	}
	bin = filepath.Join(t.TempDir(), "enola")
	if out, err := exec.Command("go", "build", "-o", bin, "../../cmd/enola").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	home = t.TempDir()
	repo = filepath.Join(t.TempDir(), "go_sample")
	copyFixture(t, filepath.Join("..", "..", "internal", "engine", "testdata", "repos", "go_sample"), repo)
	if code, _, stderr := runTwin(t, bin, home, repo, "--generate", "."); code != 0 {
		t.Fatalf("generate: exit %d\n%s", code, stderr)
	}
	return bin, home, repo
}

func copyFixture(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), data, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func runTwin(t *testing.T, bin, home, dir string, args ...string) (int, string, string) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "HOME="+home, "ENOLA_NO_UPDATE_CHECK=1")
	var stdout, stderr strings.Builder
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

// toolSession restores the fixture's snapshot into a fresh engine and connects a
// client to the MCP server a session would run over it.
func toolSession(t *testing.T, home, repo string) *mcp.ClientSession {
	t.Helper()
	t.Setenv("HOME", home)
	eng, cfg, err := bootstrap.NewEngine(bootstrap.Options{ConfigPath: filepath.Join(repo, "mcp-arch.yaml")})
	if err != nil {
		t.Fatal(err)
	}
	cfg.Repo = repo
	if !bootstrap.LoadDashboardSnapshot(eng, cfg) {
		t.Fatal("the fixture's snapshot did not restore")
	}
	srv, err := bootstrap.NewServer(eng, cfg)
	if err != nil {
		t.Fatal(err)
	}
	serverT, clientT := mcp.NewInMemoryTransports()
	if _, err := srv.MCP().Connect(context.Background(), serverT, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "twin-test", Version: "0"}, nil).Connect(context.Background(), clientT, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func toolText(t *testing.T, cs *mcp.ClientSession, tool string, args map[string]any) (string, bool) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("%s %v: %v", tool, args, err)
	}
	var sb strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			sb.WriteString(tc.Text)
		}
	}
	return sb.String(), res.IsError
}

// impactTraversed reports whether impact_analysis resolved its target to
// something it walked, read from the tool's own structured answer.
func impactTraversed(t *testing.T, cs *mcp.ClientSession, args map[string]any) bool {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "impact_analysis", Arguments: args})
	if err != nil {
		t.Fatalf("impact_analysis %v: %v", args, err)
	}
	doc, _ := res.StructuredContent.(map[string]any)
	resolution, _ := doc["resolution"].(map[string]any)
	if resolution == nil {
		return true
	}
	matched, _ := resolution["matched"].(string)
	return matched != ""
}

// The twins print what the tools answer, byte for byte, over one restored
// snapshot: in every output mode, with and without a token cap, for a name
// that resolves, one that is ambiguous and one that resolves to nothing, and
// for set endpoints.
func TestTwinsAnswerWhatTheToolsAnswer(t *testing.T) {
	bin, home, repo := twinFixture(t)
	cs := toolSession(t, home, repo)

	type twinCase struct {
		tool  string
		args  map[string]any
		flags []string
	}
	var cases []twinCase
	for _, target := range []string{"pkg/a.Alpha", "a", "nosuchthing"} {
		for _, mode := range []struct {
			arg   string
			flags []string
		}{{"", nil}, {"compact", []string{"--output-mode", "compact"}}, {"full", []string{"--output-mode", "full"}}, {"full", []string{"--json"}}} {
			for _, cap := range []int{0, 30} {
				args := map[string]any{"target": target}
				flags := append([]string{"impact"}, mode.flags...)
				if mode.arg != "" {
					args["output_mode"] = mode.arg
				}
				if cap != 0 {
					args["max_tokens"] = cap
					flags = append(flags, "--max-tokens", "30")
				}
				cases = append(cases, twinCase{"impact_analysis", args, append(flags, target, repo)})
			}
		}
	}
	cases = append(cases,
		twinCase{"impact_analysis", map[string]any{"target": "pkg/a.Alpha", "max_depth": 1, "max_nodes": 2, "include_forward": true},
			[]string{"impact", "--max-depth", "1", "--max-nodes", "2", "--include-forward", "pkg/a.Alpha", repo}},
		twinCase{"find_path", map[string]any{"from": "..main", "to": "pkg/a.Alpha"}, []string{"path", "..main", "pkg/a.Alpha", repo}},
		twinCase{"find_path", map[string]any{"from": "a", "to": "Beta"}, []string{"path", "a", "Beta", repo}},
		twinCase{"find_path", map[string]any{"from": "Alpha", "to": "nosuchthing"}, []string{"path", "Alpha", "nosuchthing", repo}},
		twinCase{"find_path", map[string]any{"from": "..main", "to": "pkg/b.Beta", "relation_kinds": []string{"calls", "imports"}, "max_depth": 1},
			[]string{"path", "--relation-kinds", "calls,imports", "--max-depth", "1", "..main", "pkg/b.Beta", repo}},
		twinCase{"find_path", map[string]any{"from_nodes": []string{"..main", "no.such.Source"}, "to_nodes": []string{"pkg/b.Beta"}},
			[]string{"path", "--from-node", "..main", "--from-node", "no.such.Source", "--to-node", "pkg/b.Beta", repo}},
	)

	truncated := 0
	for _, c := range cases {
		want, isError := toolText(t, cs, c.tool, c.args)
		code, stdout, stderr := runTwin(t, bin, home, repo, c.flags...)
		got := stdout
		if isError {
			if code != 2 {
				t.Errorf("%v: the tool errored and the twin exited %d, want 2", c.flags, code)
			}
		} else if c.tool == "impact_analysis" && !impactTraversed(t, cs, c.args) {
			// impact exits 2 on an answer that traversed nothing, so a script cannot read it as "nothing depends on this".
			if code != 2 {
				t.Errorf("%v: the tool traversed nothing and the twin exited %d, want 2", c.flags, code)
			}
		} else if code != 0 {
			t.Errorf("%v: exit %d\n%s", c.flags, code, stderr)
		}
		if got != withFinalNewline(want) {
			t.Errorf("%v: the twin printed\n%s\nthe tool answered\n%s", c.flags, got, want)
		}
		if strings.Contains(want, "[truncated:") {
			truncated++
			if !strings.Contains(got, "\n\n[truncated:") {
				t.Errorf("%v: the cap cut the tool's answer and not the twin's", c.flags)
			}
		}
	}
	if truncated == 0 {
		t.Fatal("no case reached the token cap, so the cap was never compared")
	}
	t.Logf("%d twin calls compared with the tools, %d of them cut by the token cap", len(cases), truncated)
}

// A twin reads a snapshot and never makes one: with none on disk it exits 2
// naming --generate, and the repository and HOME are exactly as they were.
func TestTwinsWithNoSnapshotRefuseAndWriteNothing(t *testing.T) {
	bin, _, _ := twinFixture(t)
	empty := t.TempDir()
	if err := os.WriteFile(filepath.Join(empty, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	quietHome := t.TempDir()
	before := treeDigest(t, empty) + treeDigest(t, quietHome)
	for _, args := range [][]string{{"impact", "main", empty}, {"path", "main", "main", empty}, {"path", "--from-node", "main", "--to-node", "main", empty}} {
		code, _, stderr := runTwin(t, bin, quietHome, empty, args...)
		if code != 2 || !strings.Contains(stderr, "--generate") {
			t.Errorf("%v: exit %d, stderr %q; want 2 and a pointer to --generate", args, code, stderr)
		}
	}
	if after := treeDigest(t, empty) + treeDigest(t, quietHome); after != before {
		t.Fatal("a twin with no snapshot wrote something")
	}
}

// A twin leaves the snapshot it read byte for byte as it found it.
func TestTwinsLeaveTheSnapshotAsTheyFoundIt(t *testing.T) {
	bin, home, repo := twinFixture(t)
	before := treeDigest(t, filepath.Join(repo, ".enola"))
	for _, args := range [][]string{{"impact", "pkg/a.Alpha", repo}, {"path", "..main", "pkg/a.Alpha", repo}} {
		if code, _, stderr := runTwin(t, bin, home, repo, args...); code != 0 {
			t.Fatalf("%v: exit %d\n%s", args, code, stderr)
		}
	}
	if after := treeDigest(t, filepath.Join(repo, ".enola")); after != before {
		t.Fatal("a twin changed the snapshot directory it read")
	}
}

// The usage mistakes each twin refuses, with the usage exit.
func TestTwinsRefuseMisuse(t *testing.T) {
	bin, home, repo := twinFixture(t)
	for _, args := range [][]string{
		{"impact", "--json", "--output-mode", "compact", "pkg/a.Alpha", repo},
		{"impact"},
		{"path", "..main"},
		{"path", "--from-node", "..main", "--to-node", "pkg/a.Alpha", "extra", repo},
	} {
		if code, _, _ := runTwin(t, bin, home, repo, args...); code != 2 {
			t.Errorf("%v: exit %d, want 2", args, code)
		}
	}
}

// treeDigest is a sha256 over every file's path and content under root, so two
// digests agree only when nothing under it was added, removed or changed.
func treeDigest(t *testing.T, root string) string {
	t.Helper()
	var paths []string
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			paths = append(paths, path)
		}
		return nil
	})
	sort.Strings(paths)
	h := sha256.New()
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		rel, _ := filepath.Rel(root, p)
		h.Write([]byte(rel + "\x00"))
		h.Write(data)
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}
