package rubyextractor

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/enola-labs/enola/internal/facts"
)

// TestSymbolSpanRuby pins the span Ruby symbols already carried before test cases
// existed: a method from def to end, as Prism reports it.
func TestSymbolSpanRuby(t *testing.T) {
	src := "class Rect\n  def area\n    w * h\n  end\nend\n"
	for _, f := range extractFileAST([]byte(src), "lib/rect.rb", false, false) {
		if f.Kind == facts.KindSymbol && strings.HasSuffix(f.Name, "area") {
			if f.Line != 2 || f.EndLine != 4 || f.Column != 3 || f.EndColumn != 6 {
				t.Errorf("area span = %d-%d %d-%d, want 2-4 3-6", f.Line, f.EndLine, f.Column, f.EndColumn)
			}
			return
		}
	}
	t.Fatal("no area symbol")
}

const rubySpec = `RSpec.describe Rect do
  it "computes its area" do
    expect(1).to eq(1)
  end

  describe "#area" do
    context "when square" do
      specify "is the side squared" do
      end
    end
  end

  describe "sizes", :slow do
    xit "is skipped" do
    end
  end

  shared_examples "a shape" do
    it "is shared" do
    end
  end
end

describe "Plain group" do
  it 'single quoted' do
  end
end
`

// TestTestCaseFactsRuby: RSpec examples named by the full description --dry-run
// prints, and minitest tests by Class#method as minitest prints them.
func TestTestCaseFactsRuby(t *testing.T) {
	type row struct {
		runner, groups string
		line, end      int
	}
	collect := func(reading testCaseReading) []row {
		var out []row
		for _, f := range reading.cases {
			if len(f.Relations) != 0 || f.Kind != facts.KindTestCase {
				t.Errorf("not a bare test_case: %+v", f)
			}
			groups, _ := f.Props["groups"].([]string)
			out = append(out, row{f.Props["runner_name"].(string), strings.Join(groups, "/"), f.Line, f.EndLine})
		}
		return out
	}
	spec := rubyTestCases([]byte(rubySpec), "spec/rect_spec.rb")
	want := []row{
		{"Rect computes its area", "Rect", 2, 4},
		{"Rect#area when square is the side squared", "Rect/#area/when square", 8, 9},
		{"Rect sizes is skipped", "Rect/sizes", 14, 15},
		{"Plain group single quoted", "Plain group", 25, 26},
	}
	if got := collect(spec); !reflect.DeepEqual(got, want) {
		t.Errorf("rspec:\n got %+v\nwant %+v", got, want)
	}

	minitest := rubyTestCases([]byte(`module Shop
  class CartTest < ActiveSupport::TestCase
    test "adds  an item" do
    end

    def test_empty
    end

    def helper
    end

    private

    def test_hidden
    end
  end
end

class Other
  def test_nope
  end
end
`), "test/cart_test.rb")
	wantMini := []row{
		{"Shop::CartTest#test_adds_an_item", "Shop/CartTest", 3, 4},
		{"Shop::CartTest#test_empty", "Shop/CartTest", 6, 7},
	}
	if got := collect(minitest); !reflect.DeepEqual(got, wantMini) {
		t.Errorf("minitest:\n got %+v\nwant %+v", got, wantMini)
	}
}

// TestTestCaseLabelNotLiteral: a label built at run time gets no fact and is
// counted, and the pass reports the count as its coverage.
func TestTestCaseLabelNotLiteral(t *testing.T) {
	src := `describe Rect do
  it { is_expected.to be_valid }
  it "has #{count} sides" do
  end
  context "with #{kind}" do
    it "a" do
    end
    it "b" do
    end
  end
  it "has a \"quoted\" name" do
  end
  it "is literal" do
  end
end
`
	reading := rubyTestCases([]byte(src), "spec/rect_spec.rb")
	if reading.unlabelled != 5 || len(reading.cases) != 1 || reading.cases[0].Props["runner_name"] != "Rect is literal" {
		t.Errorf("unlabelled %d, cases %+v; want 5 counted and only the literal one named", reading.unlabelled, reading.cases)
	}

	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, "spec"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "spec", "rect_spec.rb"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	ff, err := New().ExtractTestRefs(context.Background(), repo, []string{"spec/rect_spec.rb"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var coverage *facts.Fact
	for i := range ff {
		if ff[i].Kind == facts.KindExtraction && ff[i].Name == "ruby:test_cases" {
			coverage = &ff[i]
		}
	}
	if coverage == nil || coverage.Props["unresolved_macros"] != "label_not_literal=5" {
		t.Errorf("coverage fact = %+v", coverage)
	}
}

// TestTestCaseRedefinedMinitest: a test method defined again replaces the first,
// and only the one the class holds is a test.
func TestTestCaseRedefinedMinitest(t *testing.T) {
	src := "class BoxTest < Minitest::Test\n  def test_open\n  end\n\n  def test_shut\n  end\n\n  def test_open\n  end\nend\n"
	var got []string
	for _, f := range rubyTestCases([]byte(src), "test/box_test.rb").cases {
		got = append(got, f.Props["runner_name"].(string)+"@"+strconv.Itoa(f.Line))
	}
	if want := []string{"BoxTest#test_shut@5", "BoxTest#test_open@8"}; !reflect.DeepEqual(got, want) {
		t.Errorf("tests = %v, want %v", got, want)
	}
}
