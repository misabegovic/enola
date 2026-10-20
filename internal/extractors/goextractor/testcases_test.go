package goextractor

import (
	"context"
	"encoding/json"
	"reflect"
	"sort"
	"testing"

	"github.com/enola-labs/enola/internal/facts"
)

const testCaseFixture = `package shapes

import (
	"fmt"
	"testing"
)

func TestArea(t *testing.T) {
	t.Run("square", func(t *testing.T) {
		if Area(2, 2) != 4 {
			t.Fatal("bad")
		}
	})
}

func Test(t *testing.T) {}

func Test_underscore(t *testing.T) {
}

func Testify(t *testing.T) {}

func TestMain(m *testing.M) { m.Run() }

func BenchmarkArea(b *testing.B) {
	for i := 0; i < b.N; i++ {
		Area(1, 1)
	}
}

func FuzzArea(f *testing.F) {
	f.Fuzz(func(t *testing.T, n int) { Area(n, n) })
}

func ExampleArea() {
	fmt.Println(Area(2, 3))
	// Output: 6
}

func ExampleRect() {
	_ = Rect{}
}

func TestWrongSignature(n int) {}

func TestWithResult(t *testing.T) error { return nil }

func helper(t *testing.T) {}
`

func goTestCaseFacts(t *testing.T, files map[string]string, testFile string) ([]facts.Fact, []facts.Fact) {
	t.Helper()
	dir := setupGoProject(t, files)
	ff, err := New().ExtractTestRefs(context.Background(), dir, []string{testFile}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var cases, others []facts.Fact
	for _, f := range ff {
		if f.Kind == facts.KindTestCase {
			cases = append(cases, f)
		} else {
			others = append(others, f)
		}
	}
	return cases, others
}

// TestTestCaseFactsGo pins which Go functions become test_case facts: the ones the
// go command runs, by its own rules. Each carries its span and the runner's name,
// and no relation, while the file's test_ref is left exactly as it was.
func TestTestCaseFactsGo(t *testing.T) {
	file := "shapes/shapes_test.go"
	cases, others := goTestCaseFacts(t, map[string]string{
		"shapes/shapes.go": spanFixture,
		file:               testCaseFixture,
	}, file)

	type row struct {
		name, style string
		span        wantSpan
	}
	var got []row
	for _, c := range cases {
		if len(c.Relations) != 0 {
			t.Errorf("%s carries relations %+v; a test_case carries none", c.Name, c.Relations)
		}
		if c.File != file {
			t.Errorf("%s file = %q", c.Name, c.File)
		}
		runner, _ := c.Props["runner_name"].(string)
		if c.Name != file+"::"+runner || c.Props["label"] != runner || c.Props["language"] != "go" {
			t.Errorf("%s name/label/runner_name/language disagree: %+v", c.Name, c.Props)
		}
		if groups, _ := c.Props["groups"].([]string); groups == nil || len(groups) != 0 {
			t.Errorf("%s groups = %#v, want an empty list", c.Name, c.Props["groups"])
		}
		style, _ := c.Props["style"].(string)
		got = append(got, row{runner, style, spanOfFact(c)})
	}
	want := []row{
		{"TestArea", "test", wantSpan{8, 14, 1, 2}},
		{"Test", "test", wantSpan{16, 16, 1, 27}},
		{"Test_underscore", "test", wantSpan{18, 19, 1, 2}},
		{"BenchmarkArea", "benchmark", wantSpan{25, 29, 1, 2}},
		{"FuzzArea", "fuzz", wantSpan{31, 33, 1, 2}},
		{"ExampleArea", "example", wantSpan{35, 38, 1, 2}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("test cases:\n got %+v\nwant %+v", got, want)
	}

	if len(others) != 1 || others[0].Kind != facts.KindTestRef {
		t.Fatalf("want the file's one test_ref beside the test cases, got %+v", others)
	}
	var targets []string
	for _, r := range others[0].Relations {
		targets = append(targets, r.Target)
	}
	sort.Strings(targets)
	if !reflect.DeepEqual(targets, []string{"f.Fuzz", "fmt.Println", "m.Run", "shapes.Area", "testing.T.Fatal", "testing.T.Run"}) {
		t.Errorf("test_ref targets = %v", targets)
	}
}

// The test_ref a file yields must not move because its tests became facts: the
// same file, before and after, gives the same bytes. want is what the pass wrote for
// this fixture before test_case existed, captured from that build on 2026-09-24, with
// t's calls typed as testing.T since 0.4.28 resolves a call through its parameter's
// type; pristine 0.4.28 writes these bytes for this fixture (checked 2026-10-12).
func TestTestCaseFactsGo_TestRefIsByteIdentical(t *testing.T) {
	file := "shapes/shapes_test.go"
	dir := setupGoProject(t, map[string]string{"shapes/shapes.go": spanFixture, file: testCaseFixture})
	ff, err := New().ExtractTestRefs(context.Background(), dir, []string{file}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var ref []byte
	for _, f := range ff {
		if f.Kind == facts.KindTestRef {
			ref, _ = json.Marshal(f)
		}
	}
	const want = `{"kind":"test_ref","name":"shapes/shapes_test.go","file":"shapes/shapes_test.go","props":{"language":"go"},"relations":[{"kind":"calls","target":"testing.T.Run"},{"kind":"calls","target":"shapes.Area"},{"kind":"calls","target":"testing.T.Fatal"},{"kind":"calls","target":"m.Run"},{"kind":"calls","target":"f.Fuzz"},{"kind":"calls","target":"fmt.Println"}]}`
	if string(ref) != want {
		t.Errorf("test_ref =\n %s\nwant\n %s", ref, want)
	}
}

// A file whose tests call nothing internal still names its tests: the test_ref is
// absent, as before, and the test cases are there.
func TestTestCaseFactsGo_WithoutAReference(t *testing.T) {
	file := "pkg/empty_test.go"
	cases, others := goTestCaseFacts(t, map[string]string{
		file: "package pkg\n\nimport \"testing\"\n\nfunc TestNothing(t *testing.T) {\n}\n",
	}, file)
	if len(others) != 0 {
		t.Errorf("want no test_ref for a file that calls nothing, got %+v", others)
	}
	if len(cases) != 1 || cases[0].Name != file+"::TestNothing" {
		t.Errorf("cases = %+v", cases)
	}
}

// A test file the parser cannot read yields nothing at all, as it did before.
func TestTestCaseFactsGo_UnparseableFileYieldsNothing(t *testing.T) {
	file := "pkg/broken_test.go"
	cases, others := goTestCaseFacts(t, map[string]string{
		file: "package pkg\n\nfunc TestBroken(t *testing.T) {\n",
	}, file)
	if len(cases)+len(others) != 0 {
		t.Errorf("want nothing from an unparseable file, got %+v %+v", cases, others)
	}
}
