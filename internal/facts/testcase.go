package facts

// NewTestCase builds a test_case fact. groups are the enclosing labels outermost
// first; runnerName is what the language's runner prints and filters on, which
// each extractor composes in its runner's own way. The span is the test's node.
func NewTestCase(language, relFile string, groups []string, label, runnerName string, line, endLine, column, endColumn int) Fact {
	copied := make([]string, len(groups))
	copy(copied, groups)
	return Fact{
		Kind:      KindTestCase,
		Name:      TestCaseName(relFile, runnerName),
		File:      relFile,
		Line:      line,
		EndLine:   endLine,
		Column:    column,
		EndColumn: endColumn,
		Props: map[string]any{
			"language":    language,
			"label":       label,
			"groups":      copied,
			"runner_name": runnerName,
		},
	}
}
