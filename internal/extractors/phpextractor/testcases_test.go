package phpextractor

import (
	"reflect"
	"sort"
	"testing"

	"github.com/enola-labs/enola/internal/facts"
)

// TestSymbolSpanPHP: a class and its method span their declaration.
func TestSymbolSpanPHP(t *testing.T) {
	src := "<?php\nnamespace Shapes;\n\nclass Rect\n{\n    public function area(): int\n    {\n        return 1;\n    }\n}\n"
	got := map[string][4]int{}
	for _, f := range extractFileAST([]byte(src), "src/Rect.php") {
		if f.Kind == facts.KindSymbol {
			got[f.Name] = [4]int{f.Line, f.EndLine, f.Column, f.EndColumn}
		}
	}
	want := map[string][4]int{`Shapes\Rect`: {4, 10, 1, 2}, `Shapes\Rect::area`: {6, 9, 5, 6}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("spans = %v, want %v", got, want)
	}
}

// TestTestCaseFactsPHP: PHPUnit's public test* methods, #[Test] and @test methods
// of a TestCase subclass by Class::method, and Pest's top-level test() and it().
func TestTestCaseFactsPHP(t *testing.T) {
	src := `<?php
namespace App\Tests;

class RectTest extends \PHPUnit\Framework\TestCase
{
    public function testArea(): void {}

    #[\PHPUnit\Framework\Attributes\Test]
    public function square(): void {}

    /** @test */
    public function empty(): void {}

    private function testHidden(): void {}

    public function helper(): void {}
}

class NotATest
{
    public function testNope(): void {}
}

test('pest area', function () {});
it('pest square', function () {})->skip();
`
	var got []string
	ff, unlabelled := extractFileCountingTests([]byte(src), "tests/RectTest.php")
	for _, f := range ff {
		if f.Kind == facts.KindTestCase {
			got = append(got, f.Props["runner_name"].(string))
		}
	}
	sort.Strings(got)
	want := []string{`App\Tests\RectTest::empty`, `App\Tests\RectTest::square`, `App\Tests\RectTest::testArea`, "it pest square", "pest area"}
	if !reflect.DeepEqual(got, want) || unlabelled != 0 {
		t.Errorf("tests = %v (unlabelled %d), want %v", got, unlabelled, want)
	}
}

// TestTestCaseLabelNotLiteral_PHP: a Pest label holding a variable or an escape is
// counted, not named.
func TestTestCaseLabelNotLiteral_PHP(t *testing.T) {
	src := "<?php\ntest(\"side $n\", function () {});\ntest('it\\'s', function () {});\ntest('plain', function () {});\n"
	ff, unlabelled := extractFileCountingTests([]byte(src), "tests/PestTest.php")
	named := 0
	for _, f := range ff {
		if f.Kind == facts.KindTestCase {
			named++
		}
	}
	if named != 1 || unlabelled != 2 {
		t.Errorf("named %d unlabelled %d, want 1 and 2", named, unlabelled)
	}
}
