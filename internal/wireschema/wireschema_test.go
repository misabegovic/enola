package wireschema

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

type inner struct {
	Name  string `json:"name"`
	Notes []string
}

type embedded struct {
	Shared string `json:"shared"`
	Deep   int    `json:"deep"`
}

type pointed struct {
	Only string `json:"only"`
}

type tree struct {
	Label    string  `json:"label"`
	Children []*tree `json:"children,omitempty"`
}

type sample struct {
	embedded
	*pointed
	Deep      string         `json:"deep"`
	Title     string         `json:"title"`
	Count     int            `json:"count,omitempty"`
	Ratio     float64        `json:"ratio"`
	On        bool           `json:"on"`
	List      []inner        `json:"list"`
	Opt       []inner        `json:"opt,omitempty"`
	Ptr       *inner         `json:"ptr"`
	OptPtr    *inner         `json:"opt_ptr,omitempty"`
	Props     map[string]any `json:"props,omitempty"`
	Counts    map[string]int `json:"counts"`
	When      time.Time      `json:"when"`
	Tree      tree           `json:"tree"`
	Quoted    int            `json:"quoted,string"`
	Skipped   string         `json:"-"`
	hidden    string
	Anything  any              `json:"anything"`
	Raw       json.RawMessage  `json:"raw,omitempty"`
	ByNumber  map[int]string   `json:"by_number,omitempty"`
	Fixed     [2]string        `json:"fixed"`
	Bytes     []byte           `json:"bytes"`
	Nested    struct{ A int }  `json:"nested"`
	Aliased   aliasedString    `json:"aliased"`
	Interface interface{ M() } `json:"interface,omitempty"`
}

type aliasedString string

func schemaOf(t *testing.T, v any) map[string]any {
	t.Helper()
	s, err := For(reflect.TypeOf(v), "sample")
	if err != nil {
		t.Fatal(err)
	}
	// Round-trip, as a caller would read the checked-in file.
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// What encoding/json writes validates strictly against the schema read off
// its type, for a zero value and for a full one.
func TestSchemaDescribesWhatTheEncoderWrites(t *testing.T) {
	schema := schemaOf(t, sample{})
	full := sample{
		embedded: embedded{Shared: "s", Deep: 3},
		pointed:  &pointed{Only: "o"},
		Deep:     "outer wins",
		Count:    2,
		List:     []inner{{Name: "a", Notes: []string{"n"}}},
		Opt:      []inner{{Name: "b"}},
		Ptr:      &inner{Name: "c"},
		OptPtr:   &inner{Name: "d"},
		Props:    map[string]any{"k": 1.5, "l": []any{"x"}},
		Counts:   map[string]int{"x": 1},
		When:     time.Date(2026, 9, 26, 0, 0, 0, 0, time.UTC),
		Tree:     tree{Label: "root", Children: []*tree{{Label: "leaf"}}},
		Quoted:   7,
		Raw:      json.RawMessage(`{"any":"thing"}`),
		ByNumber: map[int]string{1: "one"},
		Bytes:    []byte("hi"),
	}
	for label, v := range map[string]sample{"zero": {}, "full": full} {
		data, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		if errs := ValidateJSON(schema, data, true); len(errs) > 0 {
			t.Errorf("%s value departs from its own schema:\n%s\n%s", label, strings.Join(errs, "\n"), data)
		}
	}
}

// The schema names exactly the keys encoding/json writes: a shallower field
// hides a promoted one, a field promoted through a pointer is optional, and
// omitempty fields are not required.
func TestSchemaReadsTheTypeTheWayTheEncoderDoes(t *testing.T) {
	schema := schemaOf(t, sample{})
	props := schema["properties"].(map[string]any)
	for _, absent := range []string{"Skipped", "-", "hidden"} {
		if _, ok := props[absent]; ok {
			t.Errorf("property %q is not written by the encoder", absent)
		}
	}
	for _, present := range []string{"shared", "only", "deep"} {
		if _, ok := props[present]; !ok {
			t.Errorf("property %q is written by the encoder and missing", present)
		}
	}
	if typ := props["deep"].(map[string]any)["type"]; typ != "string" {
		t.Errorf("deep is the outer string field, got %v", typ)
	}
	required := map[string]bool{}
	for _, r := range schema["required"].([]any) {
		required[r.(string)] = true
	}
	for _, want := range []string{"shared", "deep", "title", "list", "ptr", "counts", "when", "tree", "quoted"} {
		if !required[want] {
			t.Errorf("%s is always written and must be required", want)
		}
	}
	for _, not := range []string{"only", "count", "opt", "opt_ptr", "props", "raw"} {
		if required[not] {
			t.Errorf("%s is not always written and must not be required", not)
		}
	}
	if typ := props["quoted"].(map[string]any)["type"]; typ != "string" {
		t.Errorf("a ,string int is written as a string, got %v", typ)
	}
}

type selfWriting struct{}

func (selfWriting) MarshalJSON() ([]byte, error) { return []byte(`"x"`), nil }

type holdsSelfWriting struct {
	V selfWriting `json:"v"`
}

// A type that writes its own JSON has no shape the type states, and For says
// which type rather than guessing.
func TestSchemaRefusesATypeThatWritesItself(t *testing.T) {
	_, err := For(reflect.TypeOf(holdsSelfWriting{}), "refused")
	if err == nil || !strings.Contains(err.Error(), "selfWriting") {
		t.Fatalf("err = %v, want a refusal naming selfWriting", err)
	}
}

// A document with a field the schema does not name fails only strictly: a
// reader accepts fields it does not know.
func TestUnknownFieldsFailOnlyStrictly(t *testing.T) {
	schema := schemaOf(t, inner{})
	doc := []byte(`{"name":"a","Notes":null,"added_later":1}`)
	if errs := ValidateJSON(schema, doc, false); len(errs) > 0 {
		t.Errorf("a reader must accept an added field: %v", errs)
	}
	if errs := ValidateJSON(schema, doc, true); len(errs) != 1 {
		t.Errorf("strictly, the added field is the one departure: %v", errs)
	}
	if errs := ValidateJSON(schema, []byte(`{"Notes":["x"]}`), false); len(errs) != 1 {
		t.Errorf("a missing required field is a departure: %v", errs)
	}
}
