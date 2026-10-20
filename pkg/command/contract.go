package command

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"reflect"
	"sort"
	"strings"

	"github.com/enola-labs/enola/internal/engine"
	"github.com/enola-labs/enola/internal/explainers/constraints"
	"github.com/enola-labs/enola/internal/facts"
	"github.com/enola-labs/enola/internal/intent"
	"github.com/enola-labs/enola/internal/wireschema"
	"github.com/enola-labs/enola/pkg/check"
)

// ContractVersion numbers the shape of the contract document. It moves when
// a field of the document is renamed or removed, never when a set it prints
// gains a value, which is the rule the artefacts' format_version keeps too.
const ContractVersion = 1

// Contract is `contract --json`: the closed sets a caller of this binary
// would otherwise copy, printed from the tables the engine itself reads, and
// a JSON Schema for each artefact and answer a program reads, generated from
// the Go types their JSON is written from.
type Contract struct {
	ContractVersion int `json:"contract_version"`
	// FormatVersion is the artefact format this build writes, the value every
	// receipt carries as format_version.
	FormatVersion    int                `json:"format_version"`
	Version          string             `json:"version"`
	ExtractorVersion string             `json:"extractor_version"`
	Vocabulary       ContractVocabulary `json:"vocabulary"`
	// Schemas are keyed by the artefact or answer they describe; each is also
	// checked in under docs/schema as <key>.schema.json.
	Schemas map[string]map[string]any `json:"schemas"`
}

// ContractVocabulary is every closed set, each read from the table the
// engine validates or evaluates with.
type ContractVocabulary struct {
	RuleForms        []ContractRuleForm `json:"rule_forms"`
	RuleKeys         []string           `json:"rule_keys"`
	ComponentKeys    []string           `json:"component_keys"`
	CounterpartRoles []string           `json:"counterpart_roles"`
	RuleModes        []string           `json:"rule_modes"`
	GuidanceModes    []string           `json:"guidance_modes"`
	EdgeDirections   []string           `json:"edge_directions"`
	RuleVias         []string           `json:"rule_vias"`
	AccessRuleVias   []string           `json:"access_rule_vias"`
	ComponentKinds   []string           `json:"component_kinds"`
	SeamVias         []string           `json:"seam_vias"`
	TokenPattern     string             `json:"token_pattern"`
	MatchDialect     intent.Dialect     `json:"match_dialect"`
	NameDialect      intent.Dialect     `json:"name_dialect"`
	PageRelations    []string           `json:"page_relations"`
	OriginChannels   []string           `json:"origin_channels"`
	FactKinds        []string           `json:"fact_kinds"`
	RelationKinds    []string           `json:"relation_kinds"`
	CheckStatuses    []ContractStatus   `json:"check_statuses"`
	// LintResolutions and LintComponentStates are what lint --json's
	// resolution and each component's state take.
	LintResolutions     []string `json:"lint_resolutions"`
	LintComponentStates []string `json:"lint_component_states"`
	// UnverdictedReasons is what a ledger row's unverdicted_reason takes.
	UnverdictedReasons []string `json:"unverdicted_reasons"`
	// PageExtensions are the extensions this build reads a page from.
	PageExtensions []string `json:"page_extensions"`
}

// ContractRuleForm is one rule form: its key, whether it resolves components
// against a measured edge, and the keys it requires beside id and because,
// as groups of which one key is enough.
type ContractRuleForm struct {
	Key        string     `json:"key"`
	WalksEdges bool       `json:"walks_edges"`
	Requires   [][]string `json:"requires"`
}

// ContractStatus is one status a check verdict takes, with its exit code.
type ContractStatus struct {
	Status   string `json:"status"`
	ExitCode int    `json:"exit_code"`
}

// contractArtefact is one document a program reads, the file its schema is
// checked in as, and the type its JSON is written from.
type contractArtefact struct {
	key   string
	title string
	typ   reflect.Type
}

// contractArtefacts are the artefacts and answers the contract states a
// schema for, in the order it prints them.
func contractArtefacts() []contractArtefact {
	return []contractArtefact{
		{"facts_record", "One line of facts.jsonl", facts.WireFactType()},
		{"receipt", "receipt.json", reflect.TypeOf(facts.Receipt{})},
		{"insights", "insights.json", reflect.SliceOf(facts.WireInsightType())},
		{"ledger", "constraints ledger --json", reflect.TypeOf(LedgerDocument{})},
		{"check", "check --json", reflect.TypeOf(check.Verdict{})},
		{"lint", "constraints lint --json", reflect.TypeOf(LintReport{})},
		{"contract", "contract --json", reflect.TypeOf(Contract{})},
	}
}

// ContractSchemas generates every schema the contract carries, keyed as
// Contract.Schemas is.
func ContractSchemas() (map[string]map[string]any, error) {
	out := map[string]map[string]any{}
	for _, a := range contractArtefacts() {
		s, err := wireschema.For(a.typ, a.title)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", a.key, err)
		}
		out[a.key] = s
	}
	return out, nil
}

// SchemaFileName is the file under docs/schema a schema is checked in as.
func SchemaFileName(key string) string {
	return strings.ReplaceAll(key, "_", "-") + ".schema.json"
}

// ContractVocabularyNow reads every closed set from the engine's tables.
func ContractVocabularyNow() ContractVocabulary {
	v := ContractVocabulary{
		RuleKeys:            intent.RuleKeys(),
		ComponentKeys:       intent.ComponentKeys(),
		RuleModes:           sortedTrue(intent.AllowedRuleModes),
		GuidanceModes:       sortedTrue(intent.AllowedGuidanceModes),
		EdgeDirections:      sortedTrue(intent.AllowedEdgeDirections),
		RuleVias:            append([]string{}, intent.AllRuleVias...),
		AccessRuleVias:      append([]string{}, intent.AccessRuleVias...),
		ComponentKinds:      sortedTrue(intent.AllowedComponentKinds),
		SeamVias:            sortedTrue(facts.AllViaKinds),
		TokenPattern:        intent.TokenPattern,
		MatchDialect:        intent.MatchDialect,
		NameDialect:         intent.NamePatternDialect,
		PageRelations:       sortedTrue(intent.AllowedPageRels),
		OriginChannels:      sortedTrue(intent.AllowedOrigins),
		FactKinds:           append([]string{}, facts.FactKinds...),
		RelationKinds:       append([]string{}, facts.RelationKinds...),
		LintResolutions:     append([]string{}, LintResolutions...),
		LintComponentStates: append([]string{}, LintComponentStates...),
		UnverdictedReasons:  append([]string{}, constraints.UnverdictedReasons...),
		PageExtensions:      intent.PageExtensionsRead(),
	}
	for _, form := range intent.RuleForms {
		requires := form.Requires
		if requires == nil {
			requires = [][]string{}
		}
		v.RuleForms = append(v.RuleForms, ContractRuleForm{Key: form.Key, WalksEdges: form.WalksEdges, Requires: requires})
	}
	for _, role := range intent.CounterpartRoles {
		v.CounterpartRoles = append(v.CounterpartRoles, role.Key)
	}
	for _, s := range check.Statuses {
		v.CheckStatuses = append(v.CheckStatuses, ContractStatus{Status: string(s), ExitCode: s.ExitCode()})
	}
	return v
}

func sortedTrue(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k, on := range set {
		if on {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// buildContract is the whole document for this build.
func (r *Runner) buildContract() (Contract, error) {
	schemas, err := ContractSchemas()
	if err != nil {
		return Contract{}, err
	}
	return Contract{
		ContractVersion:  ContractVersion,
		FormatVersion:    facts.ArtifactFormatVersion,
		Version:          r.buildVersion(),
		ExtractorVersion: engine.ExtractorVersion(),
		Vocabulary:       ContractVocabularyNow(),
		Schemas:          schemas,
	}, nil
}

// Contract is `contract [--json]`: what a program reading this binary may
// rely on, printed rather than copied. It reads nothing on disk and writes
// nothing.
func (r *Runner) Contract(args []string) {
	fs := flag.NewFlagSet("contract", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	asJSON := fs.Bool("json", false, "print the contract as one JSON document, with every schema")
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, "Usage: "+r.name()+" contract [--json]\n\n"+
			"The closed sets a program reading this binary would otherwise copy (rule\n"+
			"forms and the keys each requires, seam vias, the token and pattern\n"+
			"grammars, page relations and origin channels, fact and relation kinds,\n"+
			"check statuses and exit codes, the page extensions this build reads),\n"+
			"read from the engine's own tables, and a JSON Schema for each artefact\n"+
			"and answer a program reads, generated from the Go types it is written\n"+
			"from. The text form summarises; --json prints the whole document.\n")
		fs.PrintDefaults()
		r.writeHelpFooter(os.Stderr, args, "contract")
	}
	if err := fs.Parse(args); err != nil {
		os.Exit(check.StatusUsageError.ExitCode())
	}
	if fs.NArg() > 0 {
		fs.Usage()
		os.Exit(check.StatusUsageError.ExitCode())
	}
	c, err := r.buildContract()
	if err != nil {
		r.cmdFatal("contract", "%v", err)
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(c); err != nil {
			r.cmdFatal("contract", "%v", err)
		}
		return
	}
	writeContractText(os.Stdout, c)
}

func writeContractText(w io.Writer, c Contract) {
	v := c.Vocabulary
	fmt.Fprintf(w, "contract %d · artefact format %d · %s · extractors %s\n\n", c.ContractVersion, c.FormatVersion, c.Version, c.ExtractorVersion)
	line := func(label string, values []string) {
		fmt.Fprintf(w, "%-22s %s\n", label+" ("+fmt.Sprint(len(values))+")", strings.Join(values, ", "))
	}
	forms := make([]string, 0, len(v.RuleForms))
	for _, f := range v.RuleForms {
		forms = append(forms, f.Key)
	}
	line("rule forms", forms)
	line("rule keys", v.RuleKeys)
	line("component keys", v.ComponentKeys)
	line("counterpart roles", v.CounterpartRoles)
	line("rule modes", v.RuleModes)
	line("guidance modes", v.GuidanceModes)
	line("edge directions", v.EdgeDirections)
	line("rule vias", v.RuleVias)
	line("component kinds", v.ComponentKinds)
	line("seam vias", v.SeamVias)
	line("page relations", v.PageRelations)
	line("origin channels", v.OriginChannels)
	line("fact kinds", v.FactKinds)
	line("relation kinds", v.RelationKinds)
	statuses := make([]string, 0, len(v.CheckStatuses))
	for _, s := range v.CheckStatuses {
		statuses = append(statuses, fmt.Sprintf("%s=%d", s.Status, s.ExitCode))
	}
	line("check statuses", statuses)
	line("lint states", v.LintComponentStates)
	line("unverdicted reasons", v.UnverdictedReasons)
	line("page extensions", v.PageExtensions)
	fmt.Fprintf(w, "%-22s %s\n", "token pattern", v.TokenPattern)
	for _, d := range []intent.Dialect{v.MatchDialect, v.NameDialect} {
		examples := make([]string, 0, len(d.Forms))
		for _, f := range d.Forms {
			examples = append(examples, f.Form+" "+f.Example)
		}
		fmt.Fprintf(w, "%-22s %s\n", d.Name, strings.Join(examples, ", "))
	}
	keys := make([]string, 0, len(c.Schemas))
	for _, a := range contractArtefacts() {
		keys = append(keys, a.key)
	}
	fmt.Fprintf(w, "\nschemas: %s\n(checked in under docs/schema as <name>.schema.json; --json prints them whole)\n", strings.Join(keys, ", "))
}
