package intent

import (
	"regexp"
	"testing"
)

// TokenPattern and validToken admit the same names.
func TestTokenPatternIsTheTokenValidator(t *testing.T) {
	pattern := regexp.MustCompile(TokenPattern)
	for _, s := range []string{"", "a", "api", "api-2", "a--b", "-", "9", "API", "a_b", "a b", "a.b", "é", "a/b", "store-v1", "ghost\n"} {
		if pattern.MatchString(s) != validToken(s) {
			t.Errorf("%q: TokenPattern says %v, validToken says %v", s, pattern.MatchString(s), validToken(s))
		}
	}
}

// Every example a dialect states is admitted by its validator, and a pattern
// outside its forms is refused.
func TestDialectsStateWhatTheirValidatorsAdmit(t *testing.T) {
	for _, f := range MatchDialect.Forms {
		if !validConstraintMatch(f.Example) {
			t.Errorf("match dialect example %q (%s) is refused by validConstraintMatch", f.Example, f.Form)
		}
	}
	for _, refused := range []string{"app/*/x.rb", "app/**/x.rb", "**/*", "app/[ab].rb", "**/a*b*"} {
		if validConstraintMatch(refused) {
			t.Errorf("match dialect admits %q, which none of its forms states", refused)
		}
	}
	for _, f := range NamePatternDialect.Forms {
		if !ValidNamePattern(f.Example) {
			t.Errorf("name dialect example %q (%s) is refused by ValidNamePattern", f.Example, f.Form)
		}
	}
	for _, refused := range []string{"*", "A*B", "*A*", "A?", "[A]"} {
		if ValidNamePattern(refused) {
			t.Errorf("name dialect admits %q, which none of its forms states", refused)
		}
	}
}
