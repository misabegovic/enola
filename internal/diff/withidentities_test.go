package diff

import (
	"testing"

	"github.com/enola-labs/enola/internal/facts"
)

type identityHolder struct {
	Direct  facts.Insight
	List    []facts.Insight
	Pointer *identityHolder
	Changes []InsightChange
	Other   string
	hidden  []facts.Insight
}

// Every finding reachable through fields, pointers and slices is stamped on a
// copy, and the value handed in keeps what it had.
func TestWithIdentitiesStampsACopy(t *testing.T) {
	in := func(title string) facts.Insight { return facts.Insight{Title: title, Source: "cycles"} }
	shared := []facts.Insight{in("Dependency cycle: a -> b -> a")}
	v := identityHolder{
		Direct:  in("direct 1"),
		List:    shared,
		Pointer: &identityHolder{List: []facts.Insight{in("nested")}},
		Changes: []InsightChange{{Before: in("before"), After: in("after")}},
		Other:   "untouched",
		hidden:  []facts.Insight{in("unexported")},
	}
	out := WithIdentities(v)

	for _, got := range []facts.Insight{out.Direct, out.List[0], out.Pointer.List[0], out.Changes[0].Before, out.Changes[0].After} {
		if got.Identity == "" || got.Identity != FindingIdentity(got) {
			t.Errorf("%q identity = %q, want %q", got.Title, got.Identity, FindingIdentity(got))
		}
	}
	if out.Other != "untouched" {
		t.Errorf("a field with no finding moved: %q", out.Other)
	}
	if shared[0].Identity != "" || v.Pointer.List[0].Identity != "" || v.Direct.Identity != "" {
		t.Error("the value handed in was stamped; the stamp must go on a copy")
	}
	if out.hidden[0].Identity != "" {
		t.Error("an unexported field is not written and is left as it was")
	}
	if ins := WithIdentities([]facts.Insight(nil)); ins != nil {
		t.Errorf("a nil list stays nil, got %v", ins)
	}
}
