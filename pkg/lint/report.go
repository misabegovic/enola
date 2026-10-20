package lint

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/enola-labs/enola/internal/explainers/constraints"
	"github.com/enola-labs/enola/internal/explainers/layers"
	"github.com/enola-labs/enola/internal/intent"
)

// Report is `constraints lint --json`: what the text prints, as one
// document a program reads without parsing prose. Every component is listed
// with its state, including the ones a text reader has to recognise by the
// note appended to their line.
type Report struct {
	// Valid is true when no problem was reported, which is exit 0.
	Valid bool `json:"valid"`
	// Resolution says what the components were resolved against: a snapshot,
	// nothing because none exists (validation only), or nothing because
	// nothing is declared.
	Resolution string `json:"resolution"`
	// Snapshot is the directory resolved against, when there was one.
	Snapshot string `json:"snapshot,omitempty"`
	// Problems are every problem the exit code counts, each with the file it
	// was declared in and, where the problem names an entry of that file, the
	// entry's line.
	Problems []Problem `json:"problems"`
	// UnknownKeys are keys the declaration carries that nothing reads. They
	// are warnings: the exit code does not count them.
	UnknownKeys []UnknownKey `json:"unknown_keys"`
	// Notes are the other warnings the text prints beside the problems, such
	// as a shipped recipe a repository overrides or a role bound to nothing.
	Notes      []string    `json:"notes"`
	Components []Component `json:"components"`

	UnevaluableSelectors []constraints.UnevaluableSelector `json:"unevaluable_selectors,omitempty"`
	UnreachableRoles     []constraints.UnreachableRole     `json:"unreachable_roles,omitempty"`
	AbsentExemplars      []constraints.ExemplarNote        `json:"absent_exemplars,omitempty"`
	Layers               []layers.LayerCount               `json:"layers,omitempty"`
}

// What the components were resolved against.
const (
	ResolvedAgainstSnapshot = "snapshot"
	ValidationOnly          = "validation_only"
	NothingDeclared         = "nothing_declared"
)

// Resolutions is the closed set Resolution takes.
var Resolutions = []string{ResolvedAgainstSnapshot, ValidationOnly, NothingDeclared}

// A component's state against the snapshot, in the order lint decides it:
// a service the snapshot does not hold, a selector it cannot evaluate, a
// selector that selects nothing, or members.
const (
	ComponentUnasked        = "unasked"
	ComponentUnevaluable    = "unevaluable"
	ComponentMatchesNothing = "matches_nothing"
	ComponentResolved       = "resolved"
)

// ComponentStates is the closed set Component.State takes.
var ComponentStates = []string{ComponentResolved, ComponentMatchesNothing, ComponentUnevaluable, ComponentUnasked}

// Component is one declared component resolved against the snapshot.
type Component struct {
	Name     string `json:"name"`
	Selector string `json:"selector,omitempty"`
	Members  int    `json:"members"`
	State    string `json:"state"`
	// Service is the repository an unasked component names.
	Service string `json:"service,omitempty"`
}

// Problem is one problem, with where it was declared.
type Problem struct {
	Repo    string `json:"repo,omitempty"`
	File    string `json:"file,omitempty"`
	Line    int    `json:"line,omitempty"`
	Message string `json:"message"`
}

// UnknownKey is one key nothing reads, in the repository that declares it.
type UnknownKey struct {
	Repo string `json:"repo,omitempty"`
	intent.UnknownKey
}

// run carries one lint: where the text goes (nowhere under --json), the
// report the same pass fills, and the repository being read.
type run struct {
	w        io.Writer
	report   *Report
	repo     string
	repoPath string
	lines    map[string]map[string][]int
}

func newRun(w io.Writer) *run {
	return &run{
		w: w,
		report: &Report{
			Problems:    []Problem{},
			UnknownKeys: []UnknownKey{},
			Notes:       []string{},
			Components:  []Component{},
		},
		lines: map[string]map[string][]int{},
	}
}

func (l *run) printf(format string, args ...any) {
	fmt.Fprintf(l.w, format, args...)
}

// problemEntry reads the location a declaration problem opens with: the file
// a constraints-directory problem names, then the section and index of the
// entry, in the words intent's validation writes them.
var problemEntry = regexp.MustCompile(`^(?:([^\s:]+\.(?:yaml|rb)): )?(components|rules)\[(\d+)\]`)

// problemFile reads the file a problem opens with when it opens with one.
var problemFile = regexp.MustCompile(`^([^\s:]+\.(?:yaml|rb)):`)

// problem records one problem the text has just printed. file is where the
// caller knows it was declared, or empty when the problem names its own file.
func (l *run) problem(file, message string) {
	p := Problem{Repo: l.repo, File: file, Message: message}
	if m := problemFile.FindStringSubmatch(message); m != nil {
		p.File = m[1]
		p.Message = strings.TrimSpace(strings.TrimPrefix(message, m[0]))
	}
	if m := problemEntry.FindStringSubmatch(message); m != nil && p.File != "" {
		i, _ := strconv.Atoi(m[3])
		if lines := l.entryLines(p.File)[m[2]]; i < len(lines) {
			p.Line = lines[i]
		}
	}
	l.report.Problems = append(l.report.Problems, p)
}

// entryLines reads a declaration file once for the line each entry of its
// components and rules starts on.
func (l *run) entryLines(file string) map[string][]int {
	key := l.repoPath + "\x00" + file
	if lines, ok := l.lines[key]; ok {
		return lines
	}
	var lines map[string][]int
	if data, err := os.ReadFile(filepath.Join(l.repoPath, filepath.FromSlash(file))); err == nil {
		lines = intent.EntryLines(data)
	}
	l.lines[key] = lines
	return lines
}
