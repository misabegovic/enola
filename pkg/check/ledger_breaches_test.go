package check

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/enola-labs/enola/internal/diff"
	"github.com/enola-labs/enola/internal/facts"
)

var update = flag.Bool("update", false, "regenerate golden files in testdata")

// ledgerFixture is one law with every shape a breach can take: counted and
// unexcused, counted and suppressed, exempted (not a breach), two breaches
// whose titles differ only in a number (one identity, two entries), and a
// finding from another explainer the ledger must not count.
func ledgerFixture() (*facts.Store, Policy, []facts.Insight) {
	store := storeWith(
		ruleFact("company-fk", "ratchet", "enola/constraints/billing.yaml", "tenant isolation rides the FK",
			`[{"witness":"users must have fk_constraints","owner":"alice","because":"legacy tables migrate in Q4","since":"2026-01-01"}]`),
		ruleFact("domain-pure", "strict", "", "the domain must not know its delivery mechanisms", ""),
		ruleFact("quiet-rule", "advisory", "enola/constraints/quiet.yaml", "nothing breaches it here", ""),
	)
	breach := func(rule, witness, file, symbol string) facts.Insight {
		in := ledgerBreach(rule, witness)
		in.Evidence = []facts.Evidence{{File: file, Symbol: symbol, Fact: witness, Detail: "forbidden edge"}}
		return in
	}
	findings := []facts.Insight{
		breach("company-fk", "orders must have fk_constraints", "db/schema.rb", "orders"),
		breach("company-fk", "invoices must have fk_constraints", "db/schema.rb", "invoices"),
		breach("domain-pure", "domain -> adapters", "app/domain/billing.rb", "Billing"),
		breach("domain-pure", "domain -> adapters/v2", "app/domain/order.rb", "Order"),
		breach("domain-pure", "domain -> adapters/v3", "app/domain/order.rb", "Order"),
		{Title: "Exempted from constraint company-fk: users must have fk_constraints", Source: "constraints", Confidence: 1.0},
		{Title: "Dependency cycle: a -> b -> a", Source: "cycles", Confidence: 1.0},
	}
	policy := Policy{Suppressions: []Suppression{
		{FindingTitlePrefix: "Constraint domain-pure violated: domain -> adapters/v2", Owner: "bob", Reason: "migration lands in Q4", Date: "2026-08-15"},
	}}
	return store, policy, findings
}

// ledgerJSON renders a ledger the way `constraints ledger --json` encodes it,
// two-space indent and a trailing newline.
func ledgerJSON(t *testing.T, led Ledger) []byte {
	t.Helper()
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	if err := enc.Encode(led); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// The ledger without the list is byte-identical to what it was before the list
// existed. The golden was written from the code before --breaches landed; the
// one field it has gained since, verdicted, was added beside the others and
// moved none of them.
func TestLedger_JSONWithoutTheListIsUnchanged(t *testing.T) {
	store, policy, findings := ledgerFixture()
	got := ledgerJSON(t, ComputeLedger(store, policy.Suppressions, findings, ledgerNow))
	golden := filepath.Join("testdata", "ledger-without-breaches.json")
	if *update {
		if err := os.WriteFile(golden, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("ledger JSON moved without the list asked for:\n--- got\n%s\n--- want\n%s", got, want)
	}
}

// For every rule the list is the count: one entry per counted breach, the
// suppressed one flagged, the exempted one absent, each identity the diff's own,
// and an asked-for rule with no breaches an empty list rather than none.
func TestLedger_TheListIsWhatTheCountCounted(t *testing.T) {
	store, policy, findings := ledgerFixture()
	led := ComputeLedgerWithBreaches(store, policy.Suppressions, findings, ledgerNow)
	byTitle := map[string]facts.Insight{}
	for _, in := range findings {
		byTitle[in.Title] = in
	}
	suppressed := 0
	for _, rule := range led.Rules {
		if rule.BreachList == nil {
			t.Fatalf("%s: no list, want one on every rule when the list is asked for", rule.ID)
		}
		list := *rule.BreachList
		if len(list) != rule.Breaches {
			t.Errorf("%s: %d listed, %d counted", rule.ID, len(list), rule.Breaches)
		}
		for _, b := range list {
			in, found := byTitle[b.Title]
			if !found {
				t.Errorf("%s: listed %q, which is no finding", rule.ID, b.Title)
				continue
			}
			if want := diff.FindingIdentity(in); b.Identity != want {
				t.Errorf("%s: %q identity %s, want diff.FindingIdentity %s", rule.ID, b.Title, b.Identity, want)
			}
			if strings.HasPrefix(b.Title, ExemptedTitlePrefix) {
				t.Errorf("%s: listed the exempted %q, which is no breach", rule.ID, b.Title)
			}
			if b.Suppressed {
				suppressed++
				if b.Title != "Constraint domain-pure violated: domain -> adapters/v2" {
					t.Errorf("%s: %q flagged suppressed, which no ledger entry matched", rule.ID, b.Title)
				}
			}
		}
	}
	if suppressed != 1 {
		t.Errorf("%d breaches flagged suppressed, want the 1 the ledger entry matched", suppressed)
	}
	for _, rule := range led.Rules {
		if rule.ID == "domain-pure" {
			list := *rule.BreachList
			if len(list) != 3 || list[0].File == "" || list[0].Symbol == "" {
				t.Errorf("domain-pure listed %+v, want three entries with their evidence file and symbol", list)
			}
		}
	}
	again := ComputeLedgerWithBreaches(store, policy.Suppressions, findings, ledgerNow)
	if !bytes.Equal(ledgerJSON(t, led), ledgerJSON(t, again)) {
		t.Error("two runs over one input listed differently")
	}
}

// The identity a reader takes from the list is the one check prints: for a
// breach that is new against a baseline, the enola/v1 fingerprint in check's
// SARIF.
func TestLedger_TheListedIdentityIsTheSARIFFingerprint(t *testing.T) {
	store, policy, findings := ledgerFixture()
	led := ComputeLedgerWithBreaches(store, policy.Suppressions, findings, ledgerNow)
	var listed LedgerBreach
	for _, rule := range led.Rules {
		for _, b := range *rule.BreachList {
			if b.Title == "Constraint company-fk violated: orders must have fk_constraints" {
				listed = b
			}
		}
	}
	if listed.Identity == "" {
		t.Fatal("the orders breach is not listed")
	}
	var fresh facts.Insight
	for _, in := range findings {
		if in.Title == listed.Title {
			fresh = in
		}
	}
	verdict := Evaluate(&diff.SnapshotDiff{FindingsNew: []facts.Insight{fresh}}, Policy{FailExplainers: []string{"constraints"}})
	out, err := verdict.SARIF(Tool{})
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Runs []struct {
			Results []struct {
				Message             struct{ Text string } `json:"message"`
				PartialFingerprints map[string]string     `json:"partialFingerprints"`
			} `json:"results"`
		} `json:"runs"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Runs) != 1 || len(doc.Runs[0].Results) != 1 {
		t.Fatalf("SARIF carried %+v, want the one new breach", doc.Runs)
	}
	if got := doc.Runs[0].Results[0].PartialFingerprints["enola/v1"]; got != listed.Identity {
		t.Fatalf("SARIF enola/v1 = %s, the list says %s: one identity, two derivations", got, listed.Identity)
	}
}
