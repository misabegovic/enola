package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// writeScript writes an executable shell provider answering --version with
// version and printing lines on stderr and body on stdout otherwise.
func writeScript(t *testing.T, dir, name, version, stderr, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	script := "#!/bin/sh\n" +
		"for a in \"$@\"; do if [ \"$a\" = \"--version\" ]; then echo " + version + "; exit 0; fi; done\n"
	if stderr != "" {
		script += "echo '" + stderr + "' >&2\n"
	}
	script += "cat <<'EOF'\n" + body + "EOF\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// Every generate ends with one line per provider the config names, after
// everything else on stderr: one that ran with its version and fact count, one
// whose pin is stale as skipped with the mismatch, and one with nothing to read
// as not applicable. The exit code stays 0.
func TestGenerate_EndsWithTheProviderSummary(t *testing.T) {
	bin := enolaBinary(t)
	scripts := t.TempDir()
	ran := writeScript(t, scripts, "ran", "1.0.0", "",
		`{"kind":"symbol","name":"counted: a","file":"main.go","props":{"resolution_level":"name-only"}}`+"\n")
	stale := writeScript(t, scripts, "stale", "0.2.0", "", "")
	nothing := writeScript(t, scripts, "nothing", "0.1.0", "enola-provider-not-applicable: no results file", "")
	repo := writeGovernedRepo(t, filepath.Join(t.TempDir(), "repo"), "summary-rule")
	config := filepath.Join(t.TempDir(), "config.yaml")
	body := "repo: " + repo + "\nhistory:\n  enabled: false\nproviders:\n" +
		"  - name: counting\n    command: [\"" + ran + "\"]\n" +
		"  - name: lint\n    command: [\"" + nothing + "\"]\n" +
		"  - name: prism\n    command: [\"" + stale + "\"]\n    expected_version: \"0.1.0\"\n"
	if err := os.WriteFile(config, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	out := runEnola(t, bin, t.TempDir(), t.TempDir(), "--generate", config)
	want := "\nProviders (declared):\n" +
		"  counting  ran 1.0.0, 1 fact\n" +
		"  lint      not applicable: no results file\n" +
		"  prism     skipped: version mismatch: reported 0.2.0, expected 0.1.0\n"
	if !strings.HasSuffix(out, want) {
		t.Fatalf("the generate does not end with the provider summary.\nwant suffix:\n%s\ngot:\n%s", want, out)
	}
}

func TestGenerate_WithNoProviderPrintsNoSummary(t *testing.T) {
	bin := enolaBinary(t)
	repo := writeGovernedRepo(t, filepath.Join(t.TempDir(), "repo"), "quiet-rule")
	out := runEnola(t, bin, t.TempDir(), t.TempDir(), "--generate", repo)
	if strings.Contains(out, "Providers") {
		t.Fatalf("a generate naming no provider must end as it always did:\n%s", out)
	}
}

// `providers list --json` names the six catalogue entries, each with whether
// it can run on this machine and, when it cannot, why.
func TestProvidersList_JSONNamesTheCatalogue(t *testing.T) {
	bin := enolaBinary(t)
	examples, err := filepath.Abs(filepath.Join("..", "..", "examples", "providers"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "providers", "list", "--json")
	cmd.Dir = t.TempDir()
	cmd.Env = append(sandboxEnv(t.TempDir()), "ENOLA_PROVIDERS_DIR="+examples, "ENOLA_RUBY=/nonexistent/ruby", "ENOLA_NODE=/nonexistent/node", "ENOLA_PROVIDERS=every")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("providers list --json: %v", err)
	}
	var listed struct {
		Mode      string `json:"mode"`
		Directory string `json:"directory"`
		Providers []struct {
			Name   string `json:"name"`
			CanRun bool   `json:"can_run"`
			Reason string `json:"reason"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(out, &listed); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if listed.Mode != "every" || listed.Directory != examples || len(listed.Providers) != 6 {
		t.Fatalf("listed = %+v", listed)
	}
	for _, p := range listed.Providers {
		if p.Name != "rubydex" && p.Name != "prism" && (p.CanRun || !strings.Contains(p.Reason, "/nonexistent/")) {
			t.Errorf("%s = %+v, want cannot run with the missing interpreter named", p.Name, p)
		}
	}
}
