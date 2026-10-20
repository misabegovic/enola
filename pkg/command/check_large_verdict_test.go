package command

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	mcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// largeVerdictBreaches is enough breaches that the verdict document alone is
// larger than one JSON-RPC frame may be: each strict breach with its evidence
// and suggested actions is about 3.3 KB of JSON.
const largeVerdictBreaches = 6000

// `enola check` answers through the grade tool in the same process. It used to
// carry that answer over a newline-framed pipe, whose reader refuses a frame
// above mcp.DefaultMaxLineLength, so a verdict with enough breaches in it
// exited 2 and graded nothing: a gate that cannot report its largest failures.
func TestCheck_CarriesAVerdictLargerThanAFrame(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the binary")
	}
	bin := filepath.Join(t.TempDir(), "enola")
	if out, err := exec.Command("go", "build", "-o", bin, "../../cmd/enola").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}

	repo := filepath.Join(t.TempDir(), "shop")
	write := func(rel, content string) {
		t.Helper()
		path := filepath.Join(repo, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module shop\n\ngo 1.21\n")
	write("enola/constraints/privacy.yaml", frameRule)
	write("customers/store.go", frameBefore)

	home := t.TempDir()
	run := func(args ...string) (int, []byte, string) {
		t.Helper()
		cmd := exec.Command(bin, args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "HOME="+home, "ENOLA_NO_UPDATE_CHECK=1", "ENOLA_NO_PROMPTS=1")
		var stderr strings.Builder
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if exit, ok := err.(*exec.ExitError); ok {
			return exit.ExitCode(), out, stderr.String()
		} else if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		return 0, out, stderr.String()
	}

	if code, _, errOut := run("baseline", "pin", "."); code != 0 {
		t.Fatalf("baseline pin exited %d:\n%s", code, errOut)
	}

	var src strings.Builder
	src.WriteString("package customers\n\nimport \"log\"\n\n")
	for i := 1; i <= largeVerdictBreaches; i++ {
		fmt.Fprintf(&src, "func Erase%d(subject string) { log.Printf(\"erasing %%s\", subject) }\n", i)
	}
	write("customers/store.go", src.String())

	code, out, errOut := run("check", "--json", "--fail-on=constraints", ".")
	if code != 1 {
		t.Fatalf("check exited %d, want 1 (every function breaches the strict rule):\n%s", code, lastLine(errOut))
	}
	if len(out) <= mcp.DefaultMaxLineLength {
		t.Fatalf("the verdict is %d bytes; the fixture must exceed one frame (%d bytes) to test anything", len(out), mcp.DefaultMaxLineLength)
	}
	var doc struct {
		Status string `json:"status"`
		Law    struct {
			Breaches int `json:"breaches"`
		} `json:"law"`
		Failures []json.RawMessage `json:"failures"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatalf("stdout is not one verdict document: %v", err)
	}
	if doc.Status != "regression" || doc.Law.Breaches != largeVerdictBreaches || len(doc.Failures) != largeVerdictBreaches {
		t.Errorf("status %q, %d breaches, %d failures; want regression and %d of each",
			doc.Status, doc.Law.Breaches, len(doc.Failures), largeVerdictBreaches)
	}
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}
