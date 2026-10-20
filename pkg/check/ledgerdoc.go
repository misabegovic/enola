package check

import (
	"fmt"
	"sort"
	"strings"

	"github.com/enola-labs/enola/internal/facts"
)

// LedgerDocument is `constraints ledger --json`: the ledger, when the snapshot
// it was read from was generated, and how far the tree has moved since.
type LedgerDocument struct {
	Ledger
	GeneratedAt string `json:"generated_at,omitempty"`
	Staleness   string `json:"staleness,omitempty"`
}

// RenderLedger is the ledger as `constraints ledger` prints it: the summary
// line, then a row per rule that has something to say. Rules with no breaches and no excuses are counted rather than listed:
// they are the denominator, and printing thirty quiet rows is how a reader
// stops reaching the four that matter.
func RenderLedger(led Ledger, meta facts.SnapshotMeta, staleness string) string {
	var sb strings.Builder
	if led.Summary.Rules == 0 {
		sb.WriteString("No rules declared — nothing to excuse.\n\n" +
			"This repository has not stated a law, so it is unasked rather than clean.\n" +
			"See docs/CONSTRAINTS.md to declare one.\n")
		return sb.String()
	}
	fmt.Fprintf(&sb, "%s\n", led.Summary.Line())
	switch {
	case staleness != "":
		fmt.Fprintf(&sb, "read from the snapshot generated %s, which is STALE relative to the working tree: %s\n"+
			"Re-snapshot before trusting these counts — `enola check` re-extracts and will not agree with them.\n",
			meta.GeneratedAt, staleness)
	case meta.GeneratedAt != "":
		fmt.Fprintf(&sb, "read from the snapshot generated %s — a rule declared since then is not counted here\n",
			meta.GeneratedAt)
	}

	rules := append([]LedgerRule(nil), led.Rules...)
	sort.SliceStable(rules, func(i, j int) bool {
		a, b := rules[i], rules[j]
		if ea, eb := a.Suppressed+a.Exempted, b.Suppressed+b.Exempted; ea != eb {
			return ea > eb
		}
		if a.Breaches != b.Breaches {
			return a.Breaches > b.Breaches
		}
		return a.ID < b.ID
	})

	quiet := 0
	shown := 0
	for _, rule := range rules {
		if rule.Breaches == 0 && rule.Exempted == 0 && len(rule.Excuses) == 0 {
			quiet++
			continue
		}
		if shown == 0 {
			sb.WriteString("\n")
		}
		shown++
		mode := rule.Mode
		if mode == "" {
			mode = "ratchet"
		}
		fmt.Fprintf(&sb, "%s [%s] — %s reported, %d excused",
			rule.ID, mode, plural(rule.Breaches, "breach", "breaches"), rule.Suppressed+rule.Exempted)
		if rule.Source != "" {
			fmt.Fprintf(&sb, " · declared in %s", rule.Source)
		}
		sb.WriteString("\n")
		if rule.Because != "" {
			fmt.Fprintf(&sb, "    because: %s\n", rule.Because)
		}
		for _, ex := range rule.Excuses {
			age := "undated"
			if ex.Date != "" {
				age = ex.Date
				if ex.AgeDays > 0 {
					age = fmt.Sprintf("%s, %s ago", ex.Date, plural(ex.AgeDays, "day", "days"))
				}
			}
			owner := ex.Owner
			if owner == "" {
				owner = "unnamed"
			}
			fmt.Fprintf(&sb, "    %s by %s (%s)", ex.Kind, owner, age)
			if ex.Witness != "" {
				fmt.Fprintf(&sb, " — %s", ex.Witness)
			}
			// An idle excuse is named on its own line rather than dropped: a
			// signature that stopped excusing anything is the one row in this
			// report someone can act on today.
			if !ex.Matched {
				sb.WriteString(" [matched nothing in this snapshot]")
			}
			sb.WriteString("\n")
			if ex.Reason != "" {
				fmt.Fprintf(&sb, "        %q\n", ex.Reason)
			}
		}
	}
	if quiet > 0 {
		fmt.Fprintf(&sb, "\n%s with no breaches and no excuses.\n", plural(quiet, "rule", "rules"))
	}
	if led.Summary.Excused > 0 {
		sb.WriteString("\nA rule whose breaches are mostly excused is a rule to reconsider, not a\n" +
			"team to chase: the excuse rate is what makes that judgement available.\n")
	}
	return sb.String()
}
