package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/enola-labs/enola/internal/config"
	"github.com/enola-labs/enola/internal/engine"
	"github.com/enola-labs/enola/internal/version"
	"github.com/enola-labs/enola/pkg/check"
	"github.com/enola-labs/enola/pkg/grade"
	"github.com/enola-labs/enola/pkg/lint"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var _ grade.Engine = (*engine.Engine)(nil)

// Binary names the command that runs this server, for the remedies a tool
// suggests and the tool a SARIF verdict is attributed to. Without it a server
// names the command it was started under and this build's version.
func Binary(name, version string) Option {
	return func(s *Server) { s.binaryName, s.binaryVersion = name, version }
}

func (s *Server) binary() (string, string) {
	name, ver := s.binaryName, s.binaryVersion
	if name == "" {
		name = commandName()
	}
	if ver == "" {
		ver = version.Version
	}
	return name, ver
}

type constraintsLedgerArgs struct {
	Repo      string `json:"repo,omitempty" jsonschema:"Repository whose law to report, by its label. Needed when the snapshot holds several repositories; with one, naming it changes nothing and naming another is refused."`
	Breaches  bool   `json:"breaches,omitempty" jsonschema:"List under each rule every breach it counted: identity, title, evidence file and symbol, and whether a suppression matched."`
	Format    string `json:"format,omitempty" jsonschema:"How to write the text: 'text' (DEFAULT, what the command prints) or 'json' (what it prints with --json). The structured content is the JSON document either way."`
	MaxTokens int    `json:"max_tokens,omitempty" jsonschema:"Cap on output size in approximate tokens (four characters each); output over it is cut on a line boundary with a notice. Default: 8000. A negative value means no cap."`
}

type constraintsLintArgs struct {
	Repo      string `json:"repo,omitempty" jsonschema:"Repository whose declaration to lint, by its label. Without it every repository of the snapshot is linted, each under its own name."`
	Format    string `json:"format,omitempty" jsonschema:"How to write the text: 'text' (DEFAULT, what the command prints) or 'json' (what it prints with --json). The structured content is the JSON document either way."`
	MaxTokens int    `json:"max_tokens,omitempty" jsonschema:"Cap on output size in approximate tokens (four characters each); output over it is cut on a line boundary with a notice. Default: 8000. A negative value means no cap."`
}

type gradeArgs struct {
	Repo           string   `json:"repo,omitempty" jsonschema:"Repository whose pin to grade against, by its label. Needed when the snapshot holds several repositories; with one, naming it changes nothing and naming another is refused."`
	Baseline       string   `json:"baseline,omitempty" jsonschema:"What to compare against: 'pinned' (DEFAULT, the snapshot a pin froze), 'previous' (the snapshot before the last generate), or a directory holding facts.jsonl."`
	FailOn         []string `json:"fail_on,omitempty" jsonschema:"Explainers whose new findings fail the grade. Default: none, so the grade reports and its status is clean."`
	MinConfidence  float64  `json:"min_confidence,omitempty" jsonschema:"Confidence floor within the fail_on explainers. Default: 1.00."`
	WarnOnly       bool     `json:"warn_only,omitempty" jsonschema:"Downgrade a fail_on or max_spillover policy to warnings."`
	Focus          string   `json:"focus,omitempty" jsonschema:"Narrow the delta to entries referencing this module, file or symbol."`
	Target         string   `json:"target,omitempty" jsonschema:"The symbol, type or package the change was meant to touch; packages reached outside its predicted radius are reported as spillover."`
	Expected       []string `json:"expected,omitempty" jsonschema:"Packages the change was expected to touch, beside the predicted radius."`
	MaxSpillover   *int     `json:"max_spillover,omitempty" jsonschema:"Fail when more than this many packages are reached outside the declared scope. Default: report only, never fail."`
	Reviewers      bool     `json:"reviewers,omitempty" jsonschema:"Report who owns the modules the change touched and who should review it (reads git author names)."`
	ReviewerWindow int      `json:"reviewer_window,omitempty" jsonschema:"With reviewers, how many recent commits authorship is measured over. Default: 500."`
	Author         string   `json:"author,omitempty" jsonschema:"With reviewers, whose change this is. Default: git config user.name."`
	Format         string   `json:"format,omitempty" jsonschema:"How to write the text: 'text' (DEFAULT), 'json', 'sarif' or 'annotations', as check -format writes them. The structured content is the JSON verdict either way."`
	Host           string   `json:"host,omitempty" jsonschema:"With format annotations, the CI that shows them: buildkite or github."`
	Link           string   `json:"link,omitempty" jsonschema:"With host buildkite, the pull request's files view to link each line into."`
	Detail         bool     `json:"detail,omitempty" jsonschema:"With format text, add the full delta (changed edges and facts) under the verdict."`
	MaxTokens      int      `json:"max_tokens,omitempty" jsonschema:"Cap on output size in approximate tokens (four characters each); output over it is cut on a line boundary with a notice. Default: 8000. A negative value means no cap."`
}

// document is a JSON document as a command prints it with --json: indented,
// ending on a newline.
func document(v any) (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func answerFormat(format string) (bool, string) {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "", "text":
		return false, ""
	case "json":
		return true, ""
	}
	return false, fmt.Sprintf("unknown format %q: expected text or json", format)
}

// registerAnswerTools registers the three tools that answer what the
// constraints ledger, constraints lint and check commands answer, over the
// loaded snapshot. The commands call them over an in-memory transport, so a
// verb and its tool are one answer.
func (s *Server) registerAnswerTools() {
	addTypedTool(s, &mcp.Tool{
		Name: "constraints_ledger",
		Description: "How much of the declared law is being excused rather than obeyed: every declared rule, its enforcement mode, the breaches it raised in the loaded snapshot, how many of those a suppression or an exemption excused (each excuse with its owner, reason and age), and whether the rule was verdicted at all. " +
			"Read from the snapshot's own intent facts, so the rules counted are the ones that produced the findings counted beside them; how far the tree has moved since is stated. " +
			"The same answer as `constraints ledger`. A report, never a gate.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args constraintsLedgerArgs) (*mcp.CallToolResult, *check.LedgerDocument, error) {
		asJSON, bad := answerFormat(args.Format)
		if bad != "" {
			return errorResult(bad), nil, nil
		}
		snap := s.eng.Snapshot()
		if snap == nil || s.eng.Store().Count() == 0 {
			return errorResult("No snapshot available. Run generate_snapshot first."), nil, nil
		}
		anchored, refusal := s.answerAnchor("constraints_ledger", args.Repo)
		if refusal != "" {
			return errorResult(refusal), nil, nil
		}
		// A malformed suppression ledger is an operational failure here exactly as
		// it is in check: an excuse rate from half an excuse list would understate
		// the number this tool exists to state.
		suppressions, err := check.LoadSuppressions(anchored.path)
		if err != nil {
			return errorResult(err.Error()), nil, nil
		}
		compute := check.ComputeLedger
		if args.Breaches {
			compute = check.ComputeLedgerWithBreaches
		}
		led := compute(s.eng.Store(), suppressions, snap.Insights, time.Now())
		meta := s.metaFor(anchored.path)
		staleness := ""
		if d, err := (memberDrift{s}).Drift(anchored.path); err == nil && (d.Unknown || d.Any()) {
			staleness = d.Summary(5)
		}
		doc := &check.LedgerDocument{Ledger: led, GeneratedAt: meta.GeneratedAt, Staleness: staleness}
		if !asJSON {
			return textResult(capTokens(check.RenderLedger(led, meta, staleness), args.MaxTokens, false)), doc, nil
		}
		text, err := document(doc)
		if err != nil {
			return errorResult(fmt.Sprintf("writing the ledger: %v", err)), nil, nil
		}
		return textResult(capTokens(text, args.MaxTokens, true)), doc, nil
	})

	addTypedTool(s, &mcp.Tool{
		Name: "constraints_lint",
		Description: "Validate the declared constraint vocabulary as it stands in the working tree (enola-intent.yaml, enola/constraints/, recipes and the cluster config's entry): every problem with the file and line that declared it, every key nothing reads, and every declared component resolved against the loaded snapshot with its member count and state, so what a selector selects is seen before a rule built on it verdicts anything. " +
			"The same answer as `constraints lint`; valid is its exit code. Reads only.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args constraintsLintArgs) (*mcp.CallToolResult, *lint.Report, error) {
		asJSON, bad := answerFormat(args.Format)
		if bad != "" {
			return errorResult(bad), nil, nil
		}
		var linted []member
		if args.Repo != "" || len(s.members()) <= 1 {
			anchored, refusal := s.answerAnchor("constraints_lint", args.Repo)
			if refusal != "" {
				return errorResult(refusal), nil, nil
			}
			linted = []member{anchored}
		} else {
			linted = s.members()
		}
		if len(linted) == 0 || linted[0].path == "" {
			return errorResult("constraints_lint: no repository to lint"), nil, nil
		}
		in := make([]lint.Member, len(linted))
		for i, m := range linted {
			in[i] = lint.Member{Path: m.path, Cluster: s.eng.Config().Intent[filepath.Base(m.path)]}
		}
		snapshot := lint.Snapshot{Dir: s.eng.OutputDir(linted[0].path)}
		if snap := s.eng.Snapshot(); snap != nil && s.eng.Store().Count() > 0 {
			// A copy: the lint store adopts every fact it is given, and the
			// published ones are read by every other call at once.
			snapshot.Facts = s.eng.Store().All()
			snapshot.Label = lint.SnapshotLabel(snap)
			snapshot.Loaded = true
		}
		var text bytes.Buffer
		report, err := lint.Run(&text, in, snapshot)
		if err != nil {
			return errorResult(err.Error()), nil, nil
		}
		if !asJSON {
			return textResult(capTokens(text.String(), args.MaxTokens, false)), report, nil
		}
		doc, err := document(report)
		if err != nil {
			return errorResult(fmt.Sprintf("writing the report: %v", err)), nil, nil
		}
		return textResult(capTokens(doc, args.MaxTokens, true)), report, nil
	})

	addTypedTool(s, &mcp.Tool{
		Name: "grade",
		Description: "Grade what a change did to the architecture against the pinned baseline, as `check` does, and answer its verdict: the status (clean, regression, incomparable), new findings split by what the policy fails on, what was resolved, excused or silenced, and the law's excuse summary. " +
			"While the working tree still matches the snapshot on disk, the loaded snapshot is graded as it stands; once it has moved, the tree is re-read in memory. Nothing is written: not the snapshot this server serves, not the one on disk, not the pin. " +
			"Nothing fails unless fail_on or max_spillover says what should. A report, never a gate: the status is for the caller to weigh.",
	}, s.grade)
}

func (s *Server) grade(ctx context.Context, req *mcp.CallToolRequest, args gradeArgs) (*mcp.CallToolResult, *check.Verdict, error) {
	ctx = withProgress(ctx, req)
	format := args.Format
	if format == "" {
		format = string(check.FormatText)
	}
	outFormat, err := check.ParseFormat(format)
	if err != nil {
		return errorResult(err.Error()), nil, nil
	}
	outHost, err := check.ParseHost(args.Host)
	if err != nil {
		return errorResult(err.Error()), nil, nil
	}
	if outFormat == check.FormatAnnotations && outHost == check.HostNone {
		return errorResult("format annotations needs host buildkite or host github: the host is named, never detected"), nil, nil
	}
	policy := check.Policy{MinConfidence: args.MinConfidence, WarnOnly: args.WarnOnly}
	if args.MaxSpillover != nil && *args.MaxSpillover >= 0 {
		policy.Thresholds = append(policy.Thresholds, check.Threshold{
			Measurement: "spillover_packages",
			FailAt:      *args.MaxSpillover + 1,
		})
	}
	named, unknown := grade.ParseFailOn(args.FailOn)
	if len(unknown) > 0 {
		return errorResult(fmt.Sprintf("fail_on names what no explainer is called: %s. The %d names it accepts are: %s. "+
			"Refusing to grade: a policy that enforces nothing must not look like a passing one.",
			strings.Join(unknown, ", "), len(config.KnownExplainers), strings.Join(config.KnownExplainers, ", "))), nil, nil
	}
	policy.FailExplainers = named

	anchored, refusal := s.answerAnchor("grade", args.Repo)
	if refusal != "" {
		return errorResult(refusal), nil, nil
	}
	repoPaths := memberPaths(s.members())
	if len(repoPaths) == 0 {
		repoPaths = []string{anchored.path}
	}
	baseDir := engine.ResolveBaselineDir(s.eng.OutputDir(anchored.path), args.Baseline)
	now := time.Now()

	graded := s.eng
	reused := ""
	members, stale := grade.ServesCheck(s.eng, repoPaths, baseDir, now)
	loaded := s.eng.Snapshot()
	servesLoaded := stale == "" && loaded != nil && s.eng.Store().Count() > 0 &&
		loaded.Meta.SnapshotID != "" && loaded.Meta.SnapshotID == members[0].Recorded.SnapshotID
	if !servesLoaded {
		if s.gradeEngine != nil {
			scratch, err := s.gradeEngine()
			if err != nil {
				return errorResult(fmt.Sprintf("making an engine to grade in: %v", err)), nil, nil
			}
			graded = scratch
		}
		s.genMu.Lock()
		restored, err := grade.Publish(ctx, graded, repoPaths, members, stale)
		s.genMu.Unlock()
		if err != nil {
			return errorResult(err.Error()), nil, nil
		}
		if restored {
			reused = grade.ReuseNote(graded, members)
		}
	}

	window := args.ReviewerWindow
	if window <= 0 {
		window = 500
	}
	res, err := grade.Compute(graded, grade.Request{
		Anchor:         anchored.path,
		GradeRepo:      anchored.path,
		RepoPaths:      repoPaths,
		Baseline:       args.Baseline,
		Policy:         policy,
		Focus:          args.Focus,
		Target:         args.Target,
		Expected:       args.Expected,
		Reviewers:      args.Reviewers,
		ReviewerWindow: window,
		Author:         args.Author,
		HistoryDir:     s.cfg.History.Dir,
		OtherSession:   s.baselineNote(baseDir, args.Baseline),
	}, now)
	name, ver := s.binary()
	var baseErr *grade.BaselineError
	if errors.As(err, &baseErr) {
		return errorResult(grade.BaselineHelp(name, baseErr.Selector, baseErr.Dir, anchored.path, baseErr.Err)), nil, nil
	}
	if err != nil {
		return errorResult(err.Error()), nil, nil
	}
	text, err := grade.Render(res, check.Output{Format: outFormat, Host: outHost, Link: args.Link, Tool: check.Tool{Name: name, Version: ver}}, args.Detail)
	if err != nil {
		return errorResult(err.Error()), nil, nil
	}
	result := textResult(capTokens(text, args.MaxTokens, outFormat != check.FormatText))
	var providers bytes.Buffer
	graded.WriteProviderSummary(&providers, repoPaths)
	result.Meta = mcp.Meta{"reused": reused, "providers": providers.String()}
	return result, &res.Verdict, nil
}
