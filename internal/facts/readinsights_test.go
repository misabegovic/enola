package facts

import (
	"strings"
	"testing"
)

// insights.json carries the identity a writer stamps; the findings read back
// from it carry none, so a snapshot read from disk holds what a generate does.
func TestReadInsightsLeavesTheStampOnDisk(t *testing.T) {
	s := NewStore()
	data, err := s.MarshalInsights([]Insight{{Title: "t", Source: "cycles", Identity: "0123456789abcdef"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"identity": "0123456789abcdef"`) {
		t.Fatalf("insights.json does not carry the identity it was handed:\n%s", data)
	}
	ins, err := ReadInsights(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(ins) != 1 || ins[0].Title != "t" || ins[0].Identity != "" {
		t.Errorf("read back %+v, want the finding without its stamp", ins)
	}
}
