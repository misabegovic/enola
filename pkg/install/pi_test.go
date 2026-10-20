package install

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

func piOpts(t *testing.T, hooks bool) Options {
	t.Helper()
	o := opts(t, hooks)
	o.Targets = []string{"pi"}
	o.HookCommand = "/opt/enola/enola"
	return o
}

// TestPi_TemplateHasEachPlaceholderOnce — the installer substitutes by string replace, so
// a placeholder that went missing or got duplicated in an edit to the template would
// ship an extension that does not parse, or one that ignores --hooks.
func TestPi_TemplateHasEachPlaceholderOnce(t *testing.T) {
	for _, p := range []string{"__ENOLA_COMMAND__", "__ENOLA_HOOKS__"} {
		if n := strings.Count(piExtension, p); n != 1 {
			t.Errorf("%s appears %d times in pi_extension.js, want 1", p, n)
		}
	}
}

// TestPi_LocalWritesTheExtensionWithoutHooks — the extension is what gives Pi the tools
// at all, so it is written by a plain install, with the hooks switched off.
func TestPi_LocalWritesTheExtensionWithoutHooks(t *testing.T) {
	o := piOpts(t, false)
	rs, err := Install(o)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(o.RepoDir, ".pi", "extensions", "enola.js")
	if a, _ := actionFor(rs, filepath.Join(".pi", "extensions", "enola.js")); a != ActionCreated {
		t.Fatalf("extension action = %q, want created; results: %v", a, rs)
	}
	src := read(t, path)
	if !strings.Contains(src, `const ENOLA = "/opt/enola/enola"`) {
		t.Error("the extension does not invoke the binary that installed it")
	}
	if !strings.Contains(src, "const HOOKS = false") || strings.Contains(src, PiHooksMarker) {
		t.Error("hooks are on in an install that did not ask for them")
	}
	if strings.Contains(src, "__ENOLA_") {
		t.Error("a placeholder survived substitution")
	}
}

func TestPi_HooksSwitchFollowsTheFlag(t *testing.T) {
	o := piOpts(t, true)
	if _, err := Install(o); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(read(t, filepath.Join(o.RepoDir, ".pi", "extensions", "enola.js")), PiHooksMarker) {
		t.Error("--hooks did not switch the extension's hooks on")
	}
	if !InstallsSessionHooks(o) {
		t.Error("a Pi install with --hooks runs the session hooks and must say so")
	}
}

// TestPi_QuotedBinaryPathIsUnquoted — hookBinary quotes a path with spaces for a shell.
// The extension spawns without one, so the quotes would become part of the filename.
func TestPi_QuotedBinaryPathIsUnquoted(t *testing.T) {
	o := piOpts(t, false)
	o.HookCommand = `"/Applications/My Tools/enola"`
	if src := piExtensionSource(o); !strings.Contains(src, `const ENOLA = "/Applications/My Tools/enola"`) {
		t.Errorf("binary path not unquoted: %s", src[strings.Index(src, "const ENOLA"):][:60])
	}
}

// TestPi_UninstallIsAByteForByteReversalLocally — enola created `.pi/`, so it goes with
// the extension, and nothing else in the repository is touched.
func TestPi_UninstallIsAByteForByteReversalLocally(t *testing.T) {
	o := piOpts(t, true)
	if _, err := Install(o); err != nil {
		t.Fatal(err)
	}
	if _, err := Uninstall(o); err != nil {
		t.Fatal(err)
	}
	if left := residue(t, o.RepoDir); len(left) != 0 {
		t.Errorf("uninstall left %v behind", left)
	}
}

// TestPi_UninstallKeepsTheUsersOtherExtensions — `.pi/extensions/` is shared with
// whatever else the user loads; only enola's file goes.
func TestPi_UninstallKeepsTheUsersOtherExtensions(t *testing.T) {
	o := piOpts(t, false)
	mine := filepath.Join(o.RepoDir, ".pi", "extensions", "mine.js")
	writeFile(t, mine, "export default function () {}\n")
	if _, err := Install(o); err != nil {
		t.Fatal(err)
	}
	if _, err := Uninstall(o); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(mine); err != nil {
		t.Errorf("uninstall removed the user's own extension: %v", err)
	}
}

func TestPi_GlobalSkipsWithoutPi(t *testing.T) {
	o := piOpts(t, false)
	o.Scope = ScopeGlobal
	if _, err := Install(o); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(o.HomeDir, ".pi")); !os.IsNotExist(err) {
		t.Errorf("~/.pi was created for a user who does not have Pi (err=%v)", err)
	}
}

func TestPi_GlobalWritesBlockAndExtension(t *testing.T) {
	o := piOpts(t, false)
	o.Scope = ScopeGlobal
	if err := os.MkdirAll(filepath.Join(o.HomeDir, ".pi", "agent"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(o); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"AGENTS.md", filepath.Join("extensions", "enola.js")} {
		if _, err := os.Stat(filepath.Join(o.HomeDir, ".pi", "agent", rel)); err != nil {
			t.Errorf("global install did not write ~/.pi/agent/%s: %v", rel, err)
		}
	}
}

// TestPi_TrustNoticeIsLocalOnly — a global extension loads without project trust, so
// telling that user to trust something would send them looking for a prompt that never
// comes.
func TestPi_TrustNoticeIsLocalOnly(t *testing.T) {
	o := piOpts(t, false)
	if !InstallsPiExtension(o) {
		t.Error("a local Pi install writes a trust-gated extension")
	}
	o.Scope = ScopeGlobal
	if InstallsPiExtension(o) {
		t.Error("a global Pi install is not trust-gated")
	}
	o.Scope = ScopeLocal
	o.Targets = []string{"claude"}
	if InstallsPiExtension(o) {
		t.Error("a run without the pi target writes no extension")
	}
}

// Nothing outside Node's standard library: the file is dropped into Pi's extension
// directory on its own, with no package.json beside it to install anything from.
func TestPi_ExtensionImportsOnlyNodesStandardLibrary(t *testing.T) {
	imports := regexp.MustCompile(`(?m)^\s*import\s[^"']*["']([^"']+)["']|require\(\s*["']([^"']+)["']\s*\)`)
	for _, m := range imports.FindAllStringSubmatch(piExtension, -1) {
		spec := m[1] + m[2]
		if !strings.HasPrefix(spec, "node:") {
			t.Errorf("the extension imports %q, which is not part of Node's standard library", spec)
		}
	}
}

func TestHooksByTarget_SaysWhoReceivedNothing(t *testing.T) {
	o := opts(t, true)
	o.Targets = []string{"claude", "cursor", "pi", "opencode"}
	rs, err := Install(o)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"claude":   sessionHooks,
		"cursor":   "",
		"pi":       sessionHooks,
		"opencode": "search redirect (plugin)",
	}
	got := HooksByTarget(o, rs)
	if len(got) != len(want) {
		t.Fatalf("HooksByTarget = %+v, want one row per selected target", got)
	}
	for _, th := range got {
		if th.Hooks != want[th.Target] {
			t.Errorf("%s received %q, want %q", th.Target, th.Hooks, want[th.Target])
		}
	}
	if !InstallsSessionHooks(Options{Targets: []string{"pi"}}) {
		t.Error("InstallsSessionHooks does not count pi")
	}

	// Without --hooks Pi still gets its extension, which serves the tools, and is
	// reported as receiving no hook.
	plain := opts(t, false)
	plain.Targets = []string{"pi"}
	rs, err = Install(plain)
	if err != nil {
		t.Fatal(err)
	}
	if got := HooksByTarget(plain, rs); len(got) != 1 || got[0].Hooks != "" {
		t.Errorf("pi without --hooks is reported as %+v, want no hook", got)
	}

	// A settings.json enola cannot parse is left untouched, so Claude Code receives
	// nothing either, whatever was asked for.
	broken := opts(t, true)
	broken.Targets = []string{"claude"}
	writeFile(t, filepath.Join(broken.RepoDir, ".claude", "settings.json"), "{ not json")
	rs, err = Install(broken)
	if err != nil {
		t.Fatal(err)
	}
	if got := HooksByTarget(broken, rs); got[0].Hooks != "" {
		t.Errorf("claude is reported as receiving %q from a settings.json left untouched", got[0].Hooks)
	}
}

// The extension's hook logic, run by Node against a stand-in binary: which subcommands
// it calls on which Pi events, with which payload, and how a stop report comes back.
// The stand-in exits at once when started as the server, so no tool is registered and
// only the hooks are exercised.
func TestPi_ExtensionCallsTheHooksAndDeliversAReportOnce(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in binary is a POSIX shell script")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not on PATH: the extension's logic cannot be run")
	}
	work := t.TempDir()
	log := filepath.Join(work, "calls.log")
	fake := filepath.Join(work, "fake-enola")
	writeFile(t, fake, "#!/bin/sh\n"+
		"[ $# -eq 0 ] && exit 0\n"+
		"input=$(cat)\n"+
		"printf '%s|%s\\n' \"$*\" \"$input\" >> '"+log+"'\n"+
		"case \"$*\" in\n"+
		"  'hook stop')\n"+
		"    case \"$input\" in *'\"stop_hook_active\":false'*)\n"+
		"      if [ ! -f '"+log+".reported' ]; then\n"+
		"        touch '"+log+".reported'\n"+
		"        printf '%s\\n' '{\"hookSpecificOutput\":{\"hookEventName\":\"Stop\",\"additionalContext\":\"a cycle appeared\"}}'\n"+
		"      fi;;\n"+
		"    esac;;\n"+
		"esac\n")
	if err := os.Chmod(fake, 0o755); err != nil {
		t.Fatal(err)
	}
	ext := filepath.Join(work, "enola.mjs")
	writeFile(t, ext, piExtensionSource(Options{HookCommand: fake, Hooks: true}))
	noHooks := filepath.Join(work, "nohooks.mjs")
	writeFile(t, noHooks, piExtensionSource(Options{HookCommand: fake, Hooks: false}))
	missing := filepath.Join(work, "missing.mjs")
	writeFile(t, missing, piExtensionSource(Options{HookCommand: filepath.Join(work, "no-such-binary"), Hooks: true}))

	driver := filepath.Join(work, "driver.mjs")
	writeFile(t, driver, `
const [ext, noHooks, missing, cwd] = process.argv.slice(2)
async function drive(path) {
  const handlers = {}, sent = []
  const pi = {
    on: (name, fn) => { (handlers[name] ??= []).push(fn) },
    registerTool: () => {},
    sendMessage: (message, options) => { sent.push({ message, options }) },
  }
  ;(await import(path)).default(pi)
  const ctx = { cwd, hasUI: false, sessionManager: { getSessionId: () => "s-1" } }
  const fire = async (name, event) => { let r; for (const fn of handlers[name] ?? []) r = await fn(event, ctx); return r }
  const run = async (tool) => {
    await fire("agent_start", {})
    if (tool) await fire("tool_call", { toolName: tool })
    await fire("agent_end", {})
  }
  await fire("session_start", { reason: "startup" })
  await fire("session_start", { reason: "new" })
  await fire("session_start", { reason: "resume" })
  await run("read")
  await run("edit")
  await run("bash")
  await run("edit")
  await fire("session_shutdown", {})
  return { sent }
}
const result = await drive(ext)
const before = (await import("node:fs")).readFileSync(cwd + "/calls.log", "utf8")
await drive(noHooks)
const after = (await import("node:fs")).readFileSync(cwd + "/calls.log", "utf8")
let threw = false
try { await drive(missing) } catch { threw = true }
console.log(JSON.stringify({ ...result, noHooksRan: before !== after, threw }))
`)
	out, err := exec.Command(node, driver, ext, noHooks, missing, work).CombinedOutput()
	if err != nil {
		t.Fatalf("driving the extension: %v\n%s", err, out)
	}
	var got struct {
		Sent []struct {
			Message map[string]any  `json:"message"`
			Options map[string]bool `json:"options"`
		} `json:"sent"`
		NoHooksRan bool `json:"noHooksRan"`
		Threw      bool `json:"threw"`
	}
	if err := json.Unmarshal(out[strings.LastIndexByte(strings.TrimSpace(string(out)), '\n')+1:], &got); err != nil {
		t.Fatalf("reading the driver's answer: %v\n%s", err, out)
	}

	calls := strings.Split(strings.TrimSpace(read(t, log)), "\n")
	wantCalls := []struct{ args, active string }{
		{"hook session-start", "null"}, {"hook session-start", "null"},
		{"hook stop", "false"}, {"hook stop", "true"}, {"hook stop", "false"},
	}
	if len(calls) != len(wantCalls) {
		t.Fatalf("the extension ran %d hook commands, want %d (session-start on startup and resume only, "+
			"stop after every run that could change the tree):\n%s", len(calls), len(wantCalls), strings.Join(calls, "\n"))
	}
	for i, c := range calls {
		args, input, _ := strings.Cut(c, "|")
		var payload map[string]any
		if err := json.Unmarshal([]byte(input), &payload); err != nil {
			t.Fatalf("call %d sent %q, not the JSON payload: %v", i, input, err)
		}
		if args != wantCalls[i].args || payload["cwd"] != work || payload["session_id"] != "s-1" ||
			jsonString(payload["stop_hook_active"]) != wantCalls[i].active {
			t.Errorf("call %d = %s %v, want %s with cwd, session id and stop_hook_active=%s",
				i, args, payload, wantCalls[i].args, wantCalls[i].active)
		}
	}
	if len(got.Sent) != 1 || got.Sent[0].Message["content"] != "a cycle appeared" || !got.Sent[0].Options["triggerTurn"] {
		t.Errorf("the report reached the model as %+v, want once, starting one more turn", got.Sent)
	}
	if got.NoHooksRan {
		t.Error("an extension written without --hooks ran a hook")
	}
	if got.Threw {
		t.Error("a missing binary threw into Pi instead of saying nothing")
	}
}

func jsonString(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
