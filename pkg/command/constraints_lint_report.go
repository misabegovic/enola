package command

import "github.com/enola-labs/enola/pkg/lint"

// LintReport is `constraints lint --json`; the constraints_lint tool answers
// it, and pkg/lint writes it.
type LintReport = lint.Report

// What lint's components were resolved against, and the closed set.
const (
	LintResolvedAgainstSnapshot = lint.ResolvedAgainstSnapshot
	LintValidationOnly          = lint.ValidationOnly
	LintNothingDeclared         = lint.NothingDeclared
)

// LintResolutions is the closed set LintReport.Resolution takes.
var LintResolutions = lint.Resolutions

// LintComponentStates is the closed set a component's state takes.
var LintComponentStates = lint.ComponentStates
