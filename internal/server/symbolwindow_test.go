package server

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/enola-labs/enola/internal/facts"
)

// TestShowSymbolSpan: a span shorter than the window is shown whole and nothing
// after it; a span longer than the window is cut at the window's length with the
// header saying where it continues; a symbol with no span gets exactly the window
// it got before spans existed.
func TestShowSymbolSpan(t *testing.T) {
	var src strings.Builder
	for i := 1; i <= 200; i++ {
		fmt.Fprintf(&src, "line %d\n", i)
	}
	file := filepath.Join(t.TempDir(), "a.go")
	if err := os.WriteFile(file, []byte(src.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	shown := func(source string) (first, last, count int) {
		lines := strings.Split(strings.TrimSuffix(source, "\n"), "\n")
		fmt.Sscanf(strings.TrimSpace(lines[0]), "%d", &first)
		fmt.Sscanf(strings.TrimSpace(lines[len(lines)-1]), "%d", &last)
		return first, last, len(lines)
	}

	short := facts.Fact{Name: "pkg.Short", File: "a.go", Line: 50, EndLine: 58}
	location, source, err := symbolWindow(file, short, 60)
	if err != nil {
		t.Fatal(err)
	}
	if first, last, n := shown(source); first != 50 || last != 58 || n != 9 {
		t.Errorf("short span shown %d-%d (%d lines), want 50-58", first, last, n)
	}
	if location != "File: a.go  Lines: 50-58" {
		t.Errorf("short span header = %q", location)
	}

	_, window, _ := symbolWindow(file, facts.Fact{File: "a.go", Line: 50}, 60)
	_, _, windowLines := shown(window)
	long := facts.Fact{Name: "pkg.Long", File: "a.go", Line: 50, EndLine: 190}
	location, source, _ = symbolWindow(file, long, 60)
	first, last, n := shown(source)
	if first != 50 || n != windowLines || last != 50+windowLines-1 {
		t.Errorf("long span shown %d-%d (%d lines), want from 50 and %d lines, no more than the window", first, last, n, windowLines)
	}
	if want := fmt.Sprintf("File: a.go  Lines: 50-190  (shown to line %d; continues to line 190)", last); location != want {
		t.Errorf("long span header = %q, want %q", location, want)
	}

	noSpan := facts.Fact{Name: "pkg.NoSpan", File: "a.go", Line: 50}
	location, source, _ = symbolWindow(file, noSpan, 60)
	today, _ := readSourceWindow(file, 50, 60)
	if source != today || location != "File: a.go  Line: 50" {
		t.Errorf("a symbol with no span moved: %q\n%s", location, source)
	}

	// Near the top of a file the window is shorter than context_lines; a span is
	// capped at that shorter window, never at context_lines.
	top := facts.Fact{Name: "pkg.Top", File: "a.go", Line: 2, EndLine: 150}
	_, topWindow, _ := symbolWindow(file, facts.Fact{File: "a.go", Line: 2}, 60)
	_, topSource, _ := symbolWindow(file, top, 60)
	_, _, topWindowLines := shown(topWindow)
	if _, _, got := shown(topSource); got != topWindowLines {
		t.Errorf("span near the top shows %d lines, the window it replaces showed %d", got, topWindowLines)
	}
}
