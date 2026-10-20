package install

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestPiHooks_FireInARealSession is TestStopHook_FiresInARealSession for pi: install the
// extension the way a user does, end real pi sessions, and look at what ran. A session
// with no baseline must pin one at start, and a session that closes a dependency cycle
// after a pinned baseline must have the stop report delivered to the model once.
//
// It is opt-in because it spawns real pi sessions against a real model. ENOLA_E2E=1 runs
// it, with ENOLA_E2E_PI_PROVIDER and ENOLA_E2E_PI_MODEL naming the model pi uses. When
// ENOLA_E2E_PI_BASE_URL is set, the test declares that provider as an OpenAI-compatible
// endpoint at that URL (ENOLA_E2E_PI_API_KEY, if the endpoint wants one). pi runs with a
// throwaway agent directory and home directory, so no real ~/.pi or ~/.enola is read or
// written. Every skip names its reason.
func TestPiHooks_FireInARealSession(t *testing.T) {
	if os.Getenv("ENOLA_E2E") != "1" {
		t.Skip("set ENOLA_E2E=1 to run: this spawns real pi sessions")
	}
	if runtime.GOOS == "windows" {
		t.Skip("the hook wrapper is a POSIX shell script")
	}
	pi, err := exec.LookPath("pi")
	if err != nil {
		t.Skip("pi not on PATH: the extension cannot be exercised end to end")
	}
	provider, model := os.Getenv("ENOLA_E2E_PI_PROVIDER"), os.Getenv("ENOLA_E2E_PI_MODEL")
	if provider == "" || model == "" {
		t.Skip("set ENOLA_E2E_PI_PROVIDER and ENOLA_E2E_PI_MODEL to name the model pi runs " +
			"(and ENOLA_E2E_PI_BASE_URL for an OpenAI-compatible endpoint)")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	work := t.TempDir()
	home := filepath.Join(work, "home")
	agentDir := filepath.Join(work, "pi-agent")
	for _, d := range []string{home, agentDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if baseURL := os.Getenv("ENOLA_E2E_PI_BASE_URL"); baseURL != "" {
		key := os.Getenv("ENOLA_E2E_PI_API_KEY")
		if key == "" {
			key = "none"
		}
		models, err := json.MarshalIndent(map[string]any{"providers": map[string]any{provider: map[string]any{
			"baseUrl": baseURL,
			"api":     "openai-completions",
			"apiKey":  key,
			"compat":  map[string]any{"supportsDeveloperRole": false, "supportsReasoningEffort": false},
			"models":  []any{map[string]any{"id": model}},
		}}}, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(agentDir, "models.json"), string(models))
	}
	env := append(os.Environ(),
		"HOME="+home,
		"PI_CODING_AGENT_DIR="+agentDir,
		"PI_OFFLINE=1",
		"ENOLA_NO_UPDATE_CHECK=1",
	)

	enola := buildEnola(ctx, t, work)
	log := filepath.Join(work, "hooks")
	if err := os.MkdirAll(log, 0o755); err != nil {
		t.Fatal(err)
	}
	wrapper := filepath.Join(work, "enola-hook-wrapper")
	writeFile(t, wrapper, "#!/bin/sh\n"+
		"printf '%s\\n' \"$*\" >> '"+filepath.Join(log, "fired.log")+"'\n"+
		"'"+enola+"' \"$@\" 2>> '"+filepath.Join(log, "stderr.log")+"'"+
		" | tee -a '"+filepath.Join(log, "stdout.log")+"'\n")
	if err := os.Chmod(wrapper, 0o755); err != nil {
		t.Fatal(err)
	}
	install := func(repo string) {
		t.Helper()
		if _, err := Install(Options{
			Scope: ScopeLocal, RepoDir: repo, HomeDir: home,
			Hooks: true, HookCommand: wrapper, Targets: []string{"pi"},
		}); err != nil {
			t.Fatalf("Install: %v", err)
		}
	}
	session := func(repo string, args ...string) string {
		t.Helper()
		argv := append([]string{"--provider", provider, "--model", model, "--no-session", "--approve"}, args...)
		cmd := exec.CommandContext(ctx, pi, argv...)
		cmd.Dir, cmd.Env = repo, env
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("pi session failed: %v\n%s", err, out)
		}
		return string(out)
	}

	t.Run("a session with no baseline pins one at start", func(t *testing.T) {
		repo := writeCyclePendingRepo(t, filepath.Join(work, "start"))
		install(repo)
		session(repo, "-p", "Say OK")

		marker := filepath.Join(repo, ".enola", "baseline", ".auto-pinned")
		deadline := time.Now().Add(90 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(marker); err == nil {
				break
			}
			time.Sleep(500 * time.Millisecond)
		}
		fired := readIfPresent(filepath.Join(log, "fired.log"))
		if !strings.Contains(fired, "hook session-start") {
			t.Fatalf("the session-start hook never ran.\nhooks that did run:\n%s", fired)
		}
		if _, err := os.Stat(marker); err != nil {
			t.Errorf("session start ran but pinned no baseline within 90s: %v\nhook stderr:\n%s",
				err, readIfPresent(filepath.Join(log, "stderr.log")))
		}
	})

	t.Run("a session that closes a cycle gets the stop report once", func(t *testing.T) {
		if err := os.Truncate(filepath.Join(log, "stdout.log"), 0); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if err := os.Truncate(filepath.Join(log, "fired.log"), 0); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		repo := writeCyclePendingRepo(t, filepath.Join(work, "stop"))
		// Installed before the pin, as a user would: the extension is a file in the
		// repository, and a baseline taken without it would not be comparable.
		install(repo)
		pin := exec.CommandContext(ctx, enola, "baseline", "pin", repo)
		pin.Dir, pin.Env = repo, env
		if out, err := pin.CombinedOutput(); err != nil {
			t.Fatalf("baseline pin: %v\n%s", err, out)
		}
		writeFile(t, filepath.Join(repo, "a", "a.go"), `package a

import "example.com/e2e/b"

// A now calls back into b, closing a cycle.
func A() string { return "a" + b.B() }
`)
		// The extension grades only a run that called a tool able to change the tree,
		// so the model is asked for one.
		events := session(repo, "--mode", "json", "Run the shell command `true` with your bash tool, then say OK")

		stdout := readIfPresent(filepath.Join(log, "stdout.log"))
		fired := readIfPresent(filepath.Join(log, "fired.log"))
		if n := strings.Count(stdout, "Cyclic dependency detected"); n != 1 {
			t.Errorf("the stop hook reported the cycle %d times, want exactly 1.\nstdout:\n%s\nhooks that ran:\n%s", n, stdout, fired)
		}
		if n := strings.Count(fired, "hook stop"); n > 2 {
			t.Errorf("the stop hook ran %d times in one session, want at most 2 (the report and one suppressed replay):\n%s", n, fired)
		}
		if n := messagesContaining(t, events, "Cyclic dependency detected"); n != 1 {
			t.Errorf("the report reached the model %d times, want exactly 1.\nevents:\n%s", n, events)
		}
	})
}

// messagesContaining counts the messages pi finalised in a --mode json event stream
// whose text contains want, whatever their role: the extension sends its report as a
// custom message that starts one more turn.
func messagesContaining(t *testing.T, events, want string) int {
	t.Helper()
	n := 0
	scanner := bufio.NewScanner(strings.NewReader(events))
	scanner.Buffer(make([]byte, 1<<20), 16<<20)
	for scanner.Scan() {
		var event struct {
			Type    string `json:"type"`
			Message struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(scanner.Bytes(), &event) != nil || event.Type != "message_end" || event.Message.Role == "assistant" {
			continue
		}
		if strings.Contains(string(event.Message.Content), want) {
			n++
		}
	}
	return n
}
