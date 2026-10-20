package providers

import (
	"fmt"
	"io"
	"strings"

	"github.com/enola-labs/enola/internal/facts"
)

// SummaryLines renders one line per provider record: ran, with its version and
// fact count; skipped, with the reason; or not applicable, with what was
// missing. A skip used to be one log line among hundreds and a field in a
// receipt nobody opened, so a stale pin could empty a graph of a provider's
// facts with nothing failing; these lines end the output, where a reader
// looks. No record, no lines.
func SummaryLines(records []facts.ProviderRecord) []string {
	width := 0
	for _, r := range records {
		if len(r.Name) > width {
			width = len(r.Name)
		}
	}
	lines := make([]string, 0, len(records))
	for _, r := range records {
		lines = append(lines, fmt.Sprintf("  %-*s  %s", width, r.Name, outcome(r)))
	}
	return lines
}

func outcome(r facts.ProviderRecord) string {
	switch {
	case r.NotApplicable():
		return "not applicable: " + r.Reason
	case r.Skipped:
		return "skipped: " + r.Reason
	}
	noun := "facts"
	if r.FactCount == 1 {
		noun = "fact"
	}
	if r.Version == "" {
		return fmt.Sprintf("ran, %d %s", r.FactCount, noun)
	}
	return fmt.Sprintf("ran %s, %d %s", r.Version, r.FactCount, noun)
}

// SummaryBlock is one repository's records under the heading that names it.
type SummaryBlock struct {
	Heading string
	Records []facts.ProviderRecord
}

// WriteSummary writes the blocks after a blank line, and nothing at all when
// no block holds a record, so a run with no provider ends exactly as it did
// before there was a summary.
func WriteSummary(w io.Writer, blocks []SummaryBlock) {
	var out strings.Builder
	for _, b := range blocks {
		if len(b.Records) == 0 {
			continue
		}
		out.WriteString(b.Heading + "\n")
		for _, line := range SummaryLines(b.Records) {
			out.WriteString(line + "\n")
		}
	}
	if out.Len() == 0 {
		return
	}
	fmt.Fprint(w, "\n"+out.String())
}
