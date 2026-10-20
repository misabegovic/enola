package mcputil

import (
	"strings"
	"testing"
)

func TestCapTokens_ZeroOrNegativeIsNoCap(t *testing.T) {
	s := strings.Repeat("abcdefghij\n", 1000)
	for _, n := range []int{0, -1, -8000} {
		if got := CapTokens(s, n, false); got != s {
			t.Errorf("CapTokens(s, %d) cut the text; maxTokens <= 0 means no cap", n)
		}
	}
}

func TestCapTokens_CuttingTwiceAtOneBudgetIsCuttingOnce(t *testing.T) {
	s := strings.Repeat("abcdefghij\n", 1000)
	for _, first := range []bool{false, true} {
		once := CapTokens(s, 100, first)
		for _, second := range []bool{false, true} {
			if twice := CapTokens(once, 100, second); twice != once {
				t.Errorf("isJSON %v then %v: a second cut at the same budget changed the text:\n%s",
					first, second, twice[len(twice)-300:])
			}
		}
		if smaller := CapTokens(once, 50, first); smaller == once || !strings.Contains(smaller, "max_tokens=50") {
			t.Errorf("isJSON %v: a smaller budget did not cut again", first)
		}
	}
	// Text that merely ends like a notice but runs past the budget is still cut.
	padded := s + textCapNotice(100)
	if got := CapTokens(padded, 100, false); got == padded {
		t.Errorf("text over the budget that ends in a notice was left uncut")
	}
}
