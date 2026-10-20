package server_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func gradeStatus(t *testing.T, s *session, label string) (string, int) {
	t.Helper()
	res := s.call(t, "grade", map[string]any{"fail_on": []string{"constraints"}, "format": "json"})
	if res.IsError {
		t.Fatalf("%s: grade refused: %s", label, text(res))
	}
	var verdict struct {
		Status   string            `json:"status"`
		Failures []json.RawMessage `json:"failures"`
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &verdict); err != nil {
		t.Fatalf("%s: %v\n%s", label, err, raw)
	}
	var fromText struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal([]byte(text(res)), &fromText); err != nil || fromText.Status != verdict.Status {
		t.Errorf("%s: the json text and the structured content disagree: %q against %q (%v)", label, fromText.Status, verdict.Status, err)
	}
	return verdict.Status, len(verdict.Failures)
}

// grade answers check's question on a kept-up server: against the pin, from
// the loaded snapshot while the tree has not moved, and from a regenerate held
// in memory once it has. Neither moves what the server serves, the snapshot on
// disk, or the pin.
func TestGradeGradesAgainstThePinAndMovesNothing(t *testing.T) {
	s, alpha := lawSession(t)
	outDir := filepath.Join(alpha, ".enola")
	read := func(rel string) []byte {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(outDir, rel))
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	facts, pin := read("facts.jsonl"), read(filepath.Join("baseline", "facts.jsonl"))
	receipt := text(s.call(t, "snapshot_receipt", map[string]any{"output_mode": "full"}))

	if status, failures := gradeStatus(t, s, "unchanged tree"); status != "clean" || failures != 0 {
		t.Errorf("an unchanged tree graded %s with %d failures, want clean", status, failures)
	}

	api := filepath.Join(alpha, "internal", "api", "api.go")
	if err := os.WriteFile(api, []byte("package api\n\nimport \"alpha/internal/store\"\n\nfunc Serve() { store.Put() }\n\nfunc Again() { store.Put() }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if status, failures := gradeStatus(t, s, "edited tree"); status != "regression" || failures == 0 {
		t.Errorf("a second api call into store graded %s with %d failures, want a regression", status, failures)
	}

	if !bytes.Equal(read("facts.jsonl"), facts) || !bytes.Equal(read(filepath.Join("baseline", "facts.jsonl")), pin) {
		t.Error("grade wrote the snapshot or the pin")
	}
	if got := text(s.call(t, "snapshot_receipt", map[string]any{"output_mode": "full"})); got != receipt {
		t.Errorf("grade moved the snapshot the server serves:\n%s\n---\n%s", receipt, got)
	}
}
