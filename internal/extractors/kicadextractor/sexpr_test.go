package kicadextractor

import "testing"

func TestParseReadsListsAtomsAndStrings(t *testing.T) {
	src := "(kicad_pcb\n\t(version 20241229)\n\t(property \"Note\" \"a \\\"quoted\\\" (paren)\\nline\")\n\t(net 3 \"\")\n)\n"
	doc, err := parse(src)
	if err != nil {
		t.Fatal(err)
	}
	root := doc.root
	if root.head() != "kicad_pcb" {
		t.Fatalf("head = %q", root.head())
	}
	if v, ok := root.first("version").text(1); !ok || v != "20241229" {
		t.Errorf("version = %q, %v", v, ok)
	}
	note := root.first("property")
	if v, _ := note.text(2); v != "a \"quoted\" (paren)\nline" {
		t.Errorf("escaped string = %q", v)
	}
	net := root.first("net")
	if n := net.arg(1); n == nil || n.quoted || n.atom != "3" {
		t.Errorf("a bare number reads as %+v", n)
	}
	if n := net.arg(2); n == nil || !n.quoted || n.atom != "" {
		t.Errorf("an empty quoted string reads as %+v", n)
	}
	line, col := doc.position(net.start)
	endLine, endCol := doc.position(net.end)
	if line != 4 || col != 2 || endLine != 4 || endCol != 11 {
		t.Errorf("net at %d:%d to %d:%d, want 4:2 to 4:11", line, col, endLine, endCol)
	}
}

func TestParseRefusesWhatIsNotOneList(t *testing.T) {
	for name, src := range map[string]string{
		"empty":             "",
		"unclosed list":     "(kicad_pcb (version 1)",
		"unmatched close":   "(kicad_pcb))",
		"unclosed string":   "(kicad_pcb (title \"x))",
		"two roots":         "(kicad_pcb) (kicad_sch)",
		"an atom":           "kicad_pcb",
		"a list then atoms": "(kicad_pcb) trailing",
	} {
		if _, err := parse(src); err == nil {
			t.Errorf("%s: parsed without an error", name)
		}
	}
}
