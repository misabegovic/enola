package mining

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/enola-labs/enola/internal/intent"
)

func renderDeclarationYAML(components []intent.ConstraintComponent, rule intent.ConstraintRule) string {
	var b strings.Builder
	if len(components) > 0 {
		b.WriteString("components:\n")
	}
	for _, c := range components {
		fmt.Fprintf(&b, "  - name: %s\n", yamlQuote(c.Name))
		if c.Service != "" {
			fmt.Fprintf(&b, "    service: %s\n", yamlQuote(c.Service))
		}
		if c.Kind != "" {
			fmt.Fprintf(&b, "    kind: %s\n", yamlQuote(c.Kind))
		}
		b.WriteString("    match:\n")
		for _, m := range c.Match {
			fmt.Fprintf(&b, "      - %s\n", yamlQuote(m))
		}
	}
	b.WriteString("rules:\n")
	fmt.Fprintf(&b, "  - id: %s\n", yamlQuote(rule.ID))
	switch {
	case rule.Require != "":
		fmt.Fprintf(&b, "    require: %s\n", yamlQuote(rule.Require))
		if rule.WhenPropContains != nil {
			b.WriteString("    when_prop_contains:\n")
			fmt.Fprintf(&b, "      prop: %s\n", yamlQuote(rule.WhenPropContains.Prop))
			fmt.Fprintf(&b, "      value: %s\n", yamlQuote(rule.WhenPropContains.Value))
		}
		b.WriteString("    must_prop_contain:\n")
		fmt.Fprintf(&b, "      prop: %s\n", yamlQuote(rule.MustPropContain.Prop))
		fmt.Fprintf(&b, "      value: %s\n", yamlQuote(rule.MustPropContain.Value))
	case rule.RequireName != "":
		fmt.Fprintf(&b, "    require_name: %s\n", yamlQuote(rule.RequireName))
		fmt.Fprintf(&b, "    pattern: %s\n", yamlQuote(rule.Pattern))
	case rule.RequireDefines != "":
		fmt.Fprintf(&b, "    require_defines: %s\n", yamlQuote(rule.RequireDefines))
		fmt.Fprintf(&b, "    method: %s\n", yamlQuote(rule.Method))
	case rule.Forbid != "":
		fmt.Fprintf(&b, "    forbid: %s\n", yamlQuote(rule.Forbid))
		fmt.Fprintf(&b, "    to: %s\n", yamlQuote(rule.To))
		fmt.Fprintf(&b, "    via: %s\n", yamlQuote(rule.Via))
	case rule.Allow != "":
		fmt.Fprintf(&b, "    allow: %s\n", yamlQuote(rule.Allow))
		b.WriteString("    only:\n")
		for _, o := range rule.Only {
			fmt.Fprintf(&b, "      - %s\n", yamlQuote(o))
		}
		fmt.Fprintf(&b, "    via: %s\n", yamlQuote(rule.Via))
	}
	fmt.Fprintf(&b, "    mode: %s\n", yamlQuote(rule.Mode))
	fmt.Fprintf(&b, "    because: %s\n", yamlQuote(rule.Because))
	return b.String()
}

func yamlQuote(s string) string {
	return strconv.Quote(s)
}

func (r *Report) WriteText(w io.Writer, top int) {
	var b strings.Builder
	fmt.Fprintf(&b, "Mined %d candidate constraints over %d facts — proposals for operator review, never self-adopting law.\n", len(r.Candidates), r.FactCount)
	fmt.Fprintf(&b, "Thresholds: support >= %d, confidence >= %.2f, exceptions <= %d per candidate. Ranked by confidence x support.\n",
		r.Config.MinSupport, r.Config.MinConfidence, r.Config.MaxExceptions)
	if r.Grouping == GroupingComponent {
		r.writePartsHeader(&b)
	}

	shown := r.Candidates
	if top > 0 && len(shown) > top {
		shown = shown[:top]
	}
	for i, c := range shown {
		fmt.Fprintf(&b, "\n%3d. %.3f x %-6d [%s", i+1, c.Confidence, c.Denominator, c.Family)
		if c.Kind != "" {
			fmt.Fprintf(&b, " %s", c.Kind)
		}
		fmt.Fprintf(&b, "] %s\n", c.Statement)
		if len(c.Exceptions) == 0 {
			fmt.Fprintf(&b, "     exceptions: none in the mined snapshot\n")
		} else {
			names := make([]string, len(c.Exceptions))
			for j, e := range c.Exceptions {
				if e.File != "" {
					names[j] = e.Name + " (" + e.File + ")"
				} else {
					names[j] = e.Name
				}
			}
			fmt.Fprintf(&b, "     exceptions (%d): %s\n", len(c.Exceptions), strings.Join(names, ", "))
		}
		if c.Support != nil {
			fmt.Fprintf(&b, "     support: %s\n", supportLine(*c.Support))
		}
		fmt.Fprintf(&b, "     would-be rule (adopt by hand: copy into enola/constraints/, rewrite because:, review mode):\n")
		for _, line := range strings.Split(strings.TrimRight(c.YAML, "\n"), "\n") {
			fmt.Fprintf(&b, "       %s\n", line)
		}
	}
	if len(r.Candidates) > len(shown) {
		fmt.Fprintf(&b, "\n... and %d more candidates below rank %d — raise --top or read the --jsonl artifact.\n", len(r.Candidates)-len(shown), len(shown))
	}

	fmt.Fprintf(&b, "\nSuppressed below the floors (counted, never silent):\n")
	tautological := 0
	for _, sc := range r.Suppressed {
		fmt.Fprintf(&b, "  %-17s %d would-be candidates under the support floor of %d; %d over the exception ceiling of %d; %d tautological",
			sc.Family+":", sc.BelowSupportFloor, r.Config.MinSupport, sc.OverExceptionCeiling, r.Config.MaxExceptions, sc.Tautological)
		if r.Grouping == GroupingComponent && !r.Config.WithCrossings {
			fmt.Fprintf(&b, "; %d the code already crosses (--with-crossings proposes them)", sc.Crossed)
		}
		b.WriteString("\n")
		tautological += sc.Tautological
	}
	if tautological > 0 {
		fmt.Fprintf(&b, "%d tautological candidate(s) suppressed — the statement holds by construction; --include-tautologies prints them\n", tautological)
	}
	r.writeSkipped(&b)
	_, _ = io.WriteString(w, b.String())
}

func (r *Report) writePartsHeader(b *strings.Builder) {
	fmt.Fprintf(b, "Grouped by the %d part(s) the declaration names; a pair with no crossing is proposed when its source part's outgoing count clears the support floor.\n", len(r.Parts))
	for _, p := range r.Parts {
		from := p.Source
		if p.Layer != "" {
			from = fmt.Sprintf("layer %s of %s", p.Layer, p.Source)
		}
		fmt.Fprintf(b, "  part %-18s %6d member(s)  (%s)\n", p.Name, p.Members, from)
	}
	for _, u := range r.UnreadParts {
		fmt.Fprintf(b, "  not read: %s: %s\n", u.Name, u.Reason)
	}
	for _, kr := range r.Resolution {
		state := "at or over"
		if kr.BelowFloor {
			state = "under"
		}
		fmt.Fprintf(b, "Resolution: %s %s over the declared parts (%s of %s land on something measured), %s the %s floor.\n",
			kr.Via, percent(kr.Share), thousands(kr.Resolved), thousands(kr.Made), state, percent(r.Config.MinResolution))
	}
}

// supportLine says what a component-mode candidate rests on, in one line.
func supportLine(s Support) string {
	return fmt.Sprintf("%s makes %s resolved %s (of %s it makes, %s): %s inside %s, %s into other parts, %s elsewhere; %s resolve %s over the declared parts",
		s.Part, thousands(s.Outgoing), s.Via, thousands(s.Made), percent(ratio(s.Outgoing, s.Made)),
		thousands(s.Inside), s.Part, thousands(s.Crossing), thousands(s.Elsewhere), s.Via, percent(s.Resolution.Share))
}

func (r *Report) writeSkipped(b *strings.Builder) {
	if len(r.Unchecked) > 0 {
		fmt.Fprintf(b, "\nCannot be checked here (%d): the kind resolves under the %s floor, so these zeros are the graph's blindness as much as the code's; not proposed:\n", len(r.Unchecked), percent(r.Config.MinResolution))
		for _, c := range r.Unchecked {
			fmt.Fprintf(b, "  %s\n    identity: %s\n    support: %s\n", c.Statement, c.Identity, supportLine(*c.Support))
		}
	}
	if len(r.Declared) > 0 {
		fmt.Fprintf(b, "\nAlready declared (%d): found, and not proposed, because a declared rule states it:\n", len(r.Declared))
		for _, d := range r.Declared {
			fmt.Fprintf(b, "  %s\n    declared by: %s\n", d.Statement, strings.Join(d.DeclaredBy, ", "))
		}
	}
	if len(r.Rejected) > 0 {
		fmt.Fprintf(b, "\nRejected (%d): found, and not proposed, because a person rejected it in %s:\n", len(r.Rejected), RejectionsFileName)
		for _, c := range r.Rejected {
			fmt.Fprintf(b, "  %s\n    rejected by %s on %s: %s\n", c.Statement, c.Rejection.Owner, c.Rejection.Since, c.Rejection.Because)
		}
	}
	if len(r.StaleRejections) > 0 {
		fmt.Fprintf(b, "\nRejections naming no candidate this run (%d), kept as written:\n", len(r.StaleRejections))
		for _, rej := range r.StaleRejections {
			fmt.Fprintf(b, "  %s (rejected by %s on %s)\n", rej.Identity, rej.Owner, rej.Since)
		}
	}
}

func percent(f float64) string {
	return strconv.FormatFloat(f*100, 'f', 1, 64) + "%"
}

func thousands(n int) string {
	s := strconv.Itoa(n)
	if n < 0 {
		return "-" + thousands(-n)
	}
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

func (r *Report) WriteJSONL(w io.Writer) error {
	enc := json.NewEncoder(w)
	header := map[string]any{
		"type":           "mining-report",
		"min_support":    r.Config.MinSupport,
		"min_confidence": r.Config.MinConfidence,
		"max_exceptions": r.Config.MaxExceptions,
		"fact_count":     r.FactCount,
		"candidates":     len(r.Candidates),
	}
	if r.Grouping == GroupingComponent {
		header["grouping"] = r.Grouping
		header["min_resolution"] = r.Config.MinResolution
		header["parts"] = r.Parts
		header["unread_parts"] = r.UnreadParts
		header["resolution"] = r.Resolution
		header["declared_rules"] = r.DeclaredRules
		header["with_crossings"] = r.Config.WithCrossings
	}
	if err := enc.Encode(header); err != nil {
		return err
	}
	for i, c := range r.Candidates {
		line := struct {
			Type string `json:"type"`
			Rank int    `json:"rank"`
			Candidate
		}{Type: "candidate", Rank: i + 1, Candidate: c}
		if err := enc.Encode(line); err != nil {
			return err
		}
	}
	for _, sc := range r.Suppressed {
		line := struct {
			Type string `json:"type"`
			SuppressedCount
		}{Type: "suppressed", SuppressedCount: sc}
		if err := enc.Encode(line); err != nil {
			return err
		}
	}
	for _, c := range r.Unchecked {
		if err := enc.Encode(struct {
			Type string `json:"type"`
			Candidate
		}{Type: "unchecked", Candidate: c}); err != nil {
			return err
		}
	}
	for _, d := range r.Declared {
		if err := enc.Encode(struct {
			Type string `json:"type"`
			DeclaredCandidate
		}{Type: "declared", DeclaredCandidate: d}); err != nil {
			return err
		}
	}
	for _, c := range r.Rejected {
		if err := enc.Encode(struct {
			Type string `json:"type"`
			RejectedCandidate
		}{Type: "rejected", RejectedCandidate: c}); err != nil {
			return err
		}
	}
	for _, rej := range r.StaleRejections {
		if err := enc.Encode(struct {
			Type string `json:"type"`
			Rejection
		}{Type: "stale-rejection", Rejection: rej}); err != nil {
			return err
		}
	}
	return nil
}
