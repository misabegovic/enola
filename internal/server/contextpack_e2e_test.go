package server_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/enola-labs/enola/pkg/pack"
)

// context_pack over the wire: the test and symbol asked for come back as ranges
// under the budget, and a call with no budget or no snapshot is refused with a
// reason rather than answered.
func TestContextPackBudget_OverMCP(t *testing.T) {
	s := startInMemory(t)
	res := s.call(t, "context_pack", map[string]any{"paths": []string{"pkg/a"}, "names": []string{"TestHelper"}, "budget": 100})
	if !res.IsError || !strings.Contains(text(res), "holds no snapshot") {
		t.Errorf("before any snapshot: %v %s", res.IsError, text(res))
	}
	s.snapshot(t)
	res = s.call(t, "context_pack", map[string]any{"paths": []string{"pkg/a"}, "names": []string{"TestHelper"}})
	if !res.IsError || !strings.Contains(text(res), "budget") {
		t.Errorf("no budget: %v %s", res.IsError, text(res))
	}
	res = s.call(t, "context_pack", map[string]any{"paths": []string{"pkg/a", "main.go"}, "names": []string{"TestHelper", "Alpha"}, "budget": 2000})
	if res.IsError {
		t.Fatalf("context_pack: %s", text(res))
	}
	var report pack.Report
	if err := json.Unmarshal([]byte(text(res)), &report); err != nil {
		t.Fatalf("%v\n%s", err, text(res))
	}
	if report.Counted > 2000 || report.Estimate != pack.EstimateNote {
		t.Errorf("counted %d of 2000, estimate %q", report.Counted, report.Estimate)
	}
	var got []string
	for _, it := range report.Items {
		got = append(got, it.Kind+" "+it.Name)
	}
	if len(got) < 3 || got[0] != "test TestHelperDoubles" || got[1] != "symbol pkg/a.Alpha" || got[len(got)-1] != "file " {
		t.Errorf("items = %v", got)
	}
}
