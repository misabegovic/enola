package command

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/enola-labs/enola/internal/wireschema"
)

var updateSchemas = flag.Bool("update-schemas", false, "rewrite docs/schema/*.schema.json from the types")

const schemaDir = "../../docs/schema"

func encodeSchema(t *testing.T, s map[string]any) []byte {
	t.Helper()
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// The schemas under docs/schema are the ones the types generate. A field
// added to a type without its schema regenerated fails here; regenerate with
//
//	go test ./pkg/command -run TestSchemasAreCheckedIn -update-schemas
//
// and read the diff: it is the change to the contract.
func TestSchemasAreCheckedIn(t *testing.T) {
	schemas, err := ContractSchemas()
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range contractArtefacts() {
		path := filepath.Join(schemaDir, SchemaFileName(a.key))
		want := encodeSchema(t, schemas[a.key])
		if *updateSchemas {
			if err := os.WriteFile(path, want, 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("%s: %v (regenerate with -update-schemas)", path, err)
			continue
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s is not the schema %s's type generates; regenerate with -update-schemas and review the diff", path, a.key)
		}
	}
}

// Every schema file under docs/schema is one the contract generates, so a
// schema whose type left the contract does not stay behind as if current.
func TestNoSchemaFileOutlivesItsType(t *testing.T) {
	known := map[string]bool{}
	for _, a := range contractArtefacts() {
		known[SchemaFileName(a.key)] = true
	}
	entries, err := os.ReadDir(schemaDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".schema.json") && !known[e.Name()] {
			t.Errorf("%s names no artefact the contract states", e.Name())
		}
	}
}

// What the built binary writes for each artefact and answer validates
// strictly against the schema for it: every field required is present,
// every field present is named, every value has the named type.
func TestSchemasDescribeWhatTheBinaryWrites(t *testing.T) {
	schemas, err := ContractSchemas()
	if err != nil {
		t.Fatal(err)
	}
	a := newAnswersRun(t)
	a.mustGenerate()

	validate := func(key, label string, data []byte) {
		t.Helper()
		if errs := wireschema.ValidateJSON(schemas[key], data, true); len(errs) > 0 {
			t.Errorf("%s departs from the %s schema:\n%s", label, key, strings.Join(errs, "\n"))
		}
	}
	read := func(name string) []byte {
		data, err := os.ReadFile(filepath.Join(a.repo, ".enola", name))
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	lines := strings.Split(strings.TrimSpace(string(read("facts.jsonl"))), "\n")
	if len(lines) == 0 {
		t.Fatal("no facts")
	}
	for i, line := range lines {
		validate("facts_record", "facts.jsonl line "+itoa(i+1), []byte(line))
	}
	validate("receipt", "receipt.json", read("receipt.json"))
	validate("insights", "insights.json", read("insights.json"))

	_, lint, _ := a.run("constraints", "lint", "--json", ".")
	validate("lint", "constraints lint --json", []byte(lint))
	_, ledger, _ := a.run("constraints", "ledger", "--json", "--breaches", ".")
	validate("ledger", "constraints ledger --json --breaches", []byte(ledger))
	_, contract, _ := a.run("contract", "--json")
	validate("contract", "contract --json", []byte(contract))

	if code, out, errOut := a.run("baseline", "pin", "."); code != 0 {
		t.Fatalf("pin exit %d\n%s\n%s", code, out, errOut)
	}
	extra := "package api\n\nimport \"example.com/r/internal/store\"\n\nfunc Again() { store.Put1() }\n"
	if err := os.WriteFile(filepath.Join(a.repo, "internal/api/again.go"), []byte(extra), 0o644); err != nil {
		t.Fatal(err)
	}
	_, verdict, _ := a.run("check", "--json", ".")
	validate("check", "check --json", []byte(verdict))
}
