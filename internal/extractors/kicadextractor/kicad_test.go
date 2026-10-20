package kicadextractor

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/enola-labs/enola/internal/facts"
)

func writeTree(t *testing.T, files map[string]string) (string, []string) {
	t.Helper()
	dir := t.TempDir()
	var rel []string
	for f, c := range files {
		p := filepath.Join(dir, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
		rel = append(rel, f)
	}
	sort.Strings(rel)
	return dir, rel
}

func extractTree(t *testing.T, dir string, rel []string) []facts.Fact {
	t.Helper()
	ok, err := New().DetectFiles(dir, rel)
	if err != nil || !ok {
		t.Fatalf("DetectFiles = %v, %v", ok, err)
	}
	ff, err := New().Extract(context.Background(), dir, rel)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	return ff
}

func extractKiCad(t *testing.T, files map[string]string) []facts.Fact {
	t.Helper()
	dir, rel := writeTree(t, files)
	return extractTree(t, dir, rel)
}

func find(ff []facts.Fact, kind, name string) *facts.Fact {
	for i := range ff {
		if ff[i].Kind == kind && ff[i].Name == name {
			return &ff[i]
		}
	}
	return nil
}

func mustFind(t *testing.T, ff []facts.Fact, kind, name string) *facts.Fact {
	t.Helper()
	f := find(ff, kind, name)
	if f == nil {
		var names []string
		for _, g := range ff {
			if g.Kind == kind {
				names = append(names, g.Name)
			}
		}
		t.Fatalf("no %s %q among %q", kind, name, names)
	}
	return f
}

func propText(f *facts.Fact, key string) string {
	v, ok := f.Props[key]
	if !ok {
		return "<absent>"
	}
	return fmt.Sprint(v)
}

func wantProps(t *testing.T, f *facts.Fact, want map[string]string) {
	t.Helper()
	for k, v := range want {
		if got := propText(f, k); got != v {
			t.Errorf("%s %s: %s = %s, want %s", f.Kind, f.Name, k, got, v)
		}
	}
}

func targets(f *facts.Fact, kind string) []string {
	var out []string
	for _, r := range f.Relations {
		if r.Kind == kind {
			out = append(out, r.Target)
		}
	}
	return out
}

func coverage(t *testing.T, ff []facts.Fact, name string) (resolved int, causes string) {
	t.Helper()
	f := mustFind(t, ff, facts.KindExtraction, name)
	entries, _ := f.Props["edge_coverage"].([]map[string]any)
	if len(entries) != 1 {
		t.Fatalf("%s edge_coverage = %v", name, f.Props["edge_coverage"])
	}
	resolved, _ = entries[0]["resolved"].(int)
	causes, _ = f.Props["unresolved_macros"].(string)
	return resolved, causes
}

func wantCoverage(t *testing.T, ff []facts.Fact, name string, resolved int, causes string) {
	t.Helper()
	gotResolved, gotCauses := coverage(t, ff, name)
	if gotResolved != resolved || gotCauses != causes {
		t.Errorf("%s = resolved %d, causes %q; want %d, %q", name, gotResolved, gotCauses, resolved, causes)
	}
}

// ampProject is a KiCad 9 project three sheets deep: amp.kicad_sch places the
// Sensor sheet, which places the Deep sheet, with a part on each level. Its board
// names every net through the numbered table and holds the awkward footprints a
// real board carries.
func ampProject() map[string]string {
	return map[string]string{
		"hw/amp.kicad_pro": `{
  "meta": {"filename": "amp.kicad_pro", "version": 3},
  "net_settings": {
    "classes": [{"name": "Default", "clearance": 0.2}, {"name": "Power", "clearance": 0.3}],
    "netclass_assignments": null,
    "netclass_patterns": [
      {"netclass": "Power", "pattern": "GND"},
      {"netclass": "Power", "pattern": "+5?"},
      {"netclass": "Signal", "pattern": "/Sensor/*"},
      {"netclass": "Default", "pattern": "unconnected-*"},
      {"netclass": "Power", "pattern": "unconnected-(U1*"}
    ]
  }
}
`,
		"hw/amp.kicad_sch": `(kicad_sch
	(version 20250114)
	(generator "eeschema")
	(uuid "aaaaaaaa-0000-0000-0000-000000000001")
	(lib_symbols
		(symbol "power:GND"
			(power)
			(property "Reference" "#PWR")
			(property "Value" "GND")
			(symbol "GND_1_1"
				(pin power_in line (at 0 0 270) (length 0) (name "GND") (number "1"))
			)
		)
		(symbol "power:+5V"
			(power)
			(property "Value" "+5V")
			(symbol "+5V_1_1"
				(pin power_in line (at 0 0 90) (length 0) (name "+5V") (number "1"))
			)
		)
		(symbol "power:+12V"
			(power)
			(property "Value" "+12V")
			(symbol "+12V_1_1"
				(pin power_in line (at 0 0 90) (length 0) (name "+12V") (number "1"))
			)
		)
		(symbol "power:PWR_FLAG"
			(power)
			(property "Value" "PWR_FLAG")
			(symbol "PWR_FLAG_0_0"
				(pin power_out line (at 0 0 90) (length 0) (name "pwr") (number "1"))
			)
		)
		(symbol "Device:R"
			(property "Reference" "R")
			(symbol "R_1_1"
				(pin passive line (name "~") (number "1"))
				(pin passive line (name "~") (number "2"))
			)
		)
	)
	(symbol (lib_id "Device:R") (uuid "aaaaaaaa-0000-0000-0000-0000000000a1")
		(property "Reference" "R1") (property "Value" "10k"))
	(symbol (lib_id "power:GND") (uuid "aaaaaaaa-0000-0000-0000-0000000000a2")
		(property "Reference" "#PWR01") (property "Value" "GND"))
	(symbol (lib_id "power:+5V") (uuid "aaaaaaaa-0000-0000-0000-0000000000a3")
		(property "Reference" "#PWR02") (property "Value" "+5V"))
	(symbol (lib_id "power:+12V") (uuid "aaaaaaaa-0000-0000-0000-0000000000a4")
		(property "Reference" "#PWR03") (property "Value" "+12V"))
	(symbol (lib_id "power:PWR_FLAG") (uuid "aaaaaaaa-0000-0000-0000-0000000000a5")
		(property "Reference" "#FLG01") (property "Value" "PWR_FLAG"))
	(sheet
		(at 100 50)
		(uuid "bbbbbbbb-0000-0000-0000-000000000001")
		(property "Sheetname" "Sensor")
		(property "Sheetfile" "sensor.kicad_sch")
		(pin "SDA" bidirectional (at 100 55 180))
		(pin "EN" input (at 100 60 180))
		(pin "OUT" output (at 120 55 0))
		(pin "IRQ" output (at 120 60 0))
	)
)
`,
		"hw/sensor.kicad_sch": `(kicad_sch
	(version 20250114)
	(generator "eeschema")
	(uuid "bbbbbbbb-0000-0000-0000-00000000f11e")
	(lib_symbols
		(symbol "power:GND"
			(power)
			(property "Value" "GND")
			(symbol "GND_1_1" (pin power_in line (name "GND") (number "1")))
		)
	)
	(symbol (lib_id "Sensor:BME280") (uuid "bbbbbbbb-0000-0000-0000-0000000000b1")
		(property "Reference" "U1") (property "Value" "SENSOR"))
	(symbol (lib_id "power:GND") (uuid "bbbbbbbb-0000-0000-0000-0000000000b2")
		(property "Reference" "#PWR04") (property "Value" "GND"))
	(sheet
		(at 10 10)
		(uuid "cccccccc-0000-0000-0000-000000000001")
		(property "Sheetname" "Deep")
		(property "Sheetfile" "deep.kicad_sch")
	)
)
`,
		"hw/deep.kicad_sch": `(kicad_sch
	(version 20250114)
	(uuid "cccccccc-0000-0000-0000-00000000f11e")
	(symbol (lib_id "Device:C") (uuid "cccccccc-0000-0000-0000-0000000000c1")
		(property "Reference" "C1") (property "Value" "100n"))
)
`,
		"hw/amp.kicad_pcb": `(kicad_pcb
	(version 20241229)
	(generator "pcbnew")
	(generator_version "9.0")
	(net 0 "")
	(net 1 "GND")
	(net 2 "+5V")
	(net 3 "/Sensor/SDA")
	(net 4 "unconnected-(U1-NC-Pad4)")
	(footprint "Resistor_SMD:R_0603"
		(layer "F.Cu")
		(uuid "dddddddd-0000-0000-0000-0000000000d1")
		(property "Reference" "R1" (at 0 0 0))
		(property "Value" "10k")
		(property "Footprint" "Resistor_SMD:R_0603")
		(property "Datasheet" "")
		(property "Description" "~")
		(property "LCSC" "C25804")
		(property ki_fp_filters "R_*")
		(path "/aaaaaaaa-0000-0000-0000-0000000000a1")
		(sheetname "/")
		(sheetfile "amp.kicad_sch")
		(pad "1" smd rect (at 0 0) (net 2 "+5V"))
		(pad "2" smd rect (at 1 0) (net 3 "/Sensor/SDA"))
	)
	(footprint "Package_SO:SOIC-8"
		(layer "F.Cu")
		(uuid "dddddddd-0000-0000-0000-0000000000d2")
		(property "Reference" "U1")
		(property "Value" "SENSOR")
		(path "/bbbbbbbb-0000-0000-0000-000000000001/bbbbbbbb-0000-0000-0000-0000000000b1")
		(sheetname "/Elsewhere/")
		(sheetfile "another-project.kicad_sch")
		(pad "1" smd rect (net 1 "GND"))
		(pad "2" smd rect (net 3 "/Sensor/SDA"))
		(pad "3" smd rect (net 0 ""))
		(pad "4" smd rect (net 4 "unconnected-(U1-NC-Pad4)"))
		(pad "5" smd rect (net 9 "GHOST"))
		(pad "6" smd rect)
		(pad "8" smd rect (net 1 "GND"))
	)
	(footprint "Capacitor_SMD:C_0402"
		(layer "F.Cu")
		(uuid "dddddddd-0000-0000-0000-0000000000d3")
		(property "Reference" "C1")
		(property "Value" "100n")
		(path "/bbbbbbbb-0000-0000-0000-000000000001/cccccccc-0000-0000-0000-000000000001/cccccccc-0000-0000-0000-0000000000c1")
		(pad "1" smd rect (net 2 "+5V"))
		(pad "2" smd rect (net 1 "GND"))
	)
	(footprint "TestPoint:TestPoint_Pad_1.0x1.0mm"
		(layer "F.Cu")
		(uuid "dddddddd-0000-0000-0000-0000000000e1")
		(property "Reference" "TP1")
		(property "Value" "TestPoint")
		(path "/aaaaaaaa-0000-0000-0000-0000000000e1")
		(pad "1" smd rect (net 1 "GND"))
	)
	(footprint "TestPoint:TestPoint_Pad_1.0x1.0mm"
		(layer "B.Cu")
		(uuid "dddddddd-0000-0000-0000-0000000000e2")
		(property "Reference" "TP1")
		(property "Value" "TestPoint")
		(pad "1" smd rect (net 1 "GND"))
	)
	(footprint "Symbol:OSHW-Logo"
		(layer "F.Cu")
		(uuid "dddddddd-0000-0000-0000-0000000000f1")
		(property "Reference" "G***")
		(property "Value" "LOGO")
		(sheetfile "amp.kicad_sch")
	)
	(footprint "Connector:Pin_1"
		(layer "F.Cu")
		(uuid "dddddddd-0000-0000-0000-0000000000f2")
		(property "Reference" "J1")
		(property "Value" "Conn")
		(path "/eeeeeeee-0000-0000-0000-000000000000/eeeeeeee-0000-0000-0000-0000000000f2")
		(pad "1" thru_hole circle (net 2 "+5V"))
	)
	(segment (start 0 0) (end 1 1) (width 0.2) (layer "F.Cu") (net 1))
)
`,
	}
}

func TestKiCad_BoardModuleCountsWhatThePadsReach(t *testing.T) {
	ff := extractKiCad(t, ampProject())
	board := mustFind(t, ff, facts.KindModule, "hw/amp.kicad_pcb")
	wantProps(t, board, map[string]string{
		"language":        "kicad",
		"format_version":  "20241229",
		"parts":           "7",
		"nets":            "4",
		"multi_pad_nets":  "3",
		"single_pad_nets": "1",
		"sheet_files":     "3",
		"net_classes":     "[Default Power]",
	})
	if _, isText := board.Props["format_version"].(string); !isText {
		t.Errorf("format_version = %#v; a date stamp read back as a JSON number prints as 2.0241229e+07", board.Props["format_version"])
	}
	if dep := mustFind(t, ff, facts.KindDependency, "hw/amp.kicad_pcb -> hw/amp.kicad_sch"); !dep.HasRelation(facts.RelImports, "hw/amp.kicad_sch") {
		t.Errorf("board -> root sheet = %+v, want an imports edge", dep)
	}
}

func TestKiCad_TwoLevelHierarchyPlacesAPartOnEachLevel(t *testing.T) {
	ff := extractKiCad(t, ampProject())
	for _, m := range []string{"hw/amp.kicad_sch", "hw/sensor.kicad_sch", "hw/deep.kicad_sch"} {
		wantProps(t, mustFind(t, ff, facts.KindModule, m), map[string]string{"language": "kicad", "format_version": "20250114"})
	}
	sensor := mustFind(t, ff, facts.KindDependency, "hw/amp.kicad_sch -> hw/sensor.kicad_sch")
	wantProps(t, sensor, map[string]string{
		"sheet_name": "Sensor", "pins": "4", "pins_input": "1", "pins_output": "2", "pins_bidirectional": "1",
	})
	if !sensor.HasRelation(facts.RelImports, "hw/sensor.kicad_sch") {
		t.Errorf("sheet dependency = %+v, want imports to the child file", sensor)
	}
	if deep := mustFind(t, ff, facts.KindDependency, "hw/sensor.kicad_sch -> hw/deep.kicad_sch"); deep.Props["sheet_name"] != "Deep" {
		t.Errorf("deep sheet dependency = %+v", deep)
	}

	for name, sheet := range map[string]string{
		"hw/amp.kicad_pcb#R1": "hw/amp.kicad_sch",
		"hw/amp.kicad_pcb#U1": "hw/sensor.kicad_sch",
		"hw/amp.kicad_pcb#C1": "hw/deep.kicad_sch",
	} {
		part := mustFind(t, ff, facts.KindSymbol, name)
		if got := targets(part, facts.RelDeclares); !reflect.DeepEqual(got, []string{sheet}) {
			t.Errorf("%s declares %v, want [%s]", name, got, sheet)
		}
	}
}

// The KES board's footprints all name a sheet file outside their own project.
// The UUID path is what places a part; the copied field is not read at all.
func TestKiCad_PathWinsOverTheSheetFields(t *testing.T) {
	ff := extractKiCad(t, ampProject())
	u1 := mustFind(t, ff, facts.KindSymbol, "hw/amp.kicad_pcb#U1")
	if got := targets(u1, facts.RelDeclares); !reflect.DeepEqual(got, []string{"hw/sensor.kicad_sch"}) {
		t.Errorf("U1 declares %v, want the sheet its path names", got)
	}
	for key := range u1.Props {
		if key == "field:Sheetfile" || key == "field:Sheetname" || key == "field:sheetfile" {
			t.Errorf("U1 copied the sheet field %s", key)
		}
	}
}

func TestKiCad_PartsCarryWhatTheFootprintStates(t *testing.T) {
	ff := extractKiCad(t, ampProject())
	r1 := mustFind(t, ff, facts.KindSymbol, "hw/amp.kicad_pcb#R1")
	wantProps(t, r1, map[string]string{
		"language":            "kicad",
		"symbol_kind":         "part",
		"reference":           "R1",
		"value":               "10k",
		"footprint":           "Resistor_SMD:R_0603",
		"field:LCSC":          "C25804",
		"field:Datasheet":     "<absent>",
		"field:Description":   "<absent>",
		"field:Reference":     "<absent>",
		"field:Footprint":     "<absent>",
		"field:ki_fp_filters": "<absent>",
		"board_only":          "<absent>",
	})
	if got := targets(r1, facts.RelDependsOn); !reflect.DeepEqual(got, []string{"hw/amp.kicad_pcb#net:+5V", "hw/amp.kicad_pcb#net:/Sensor/SDA"}) {
		t.Errorf("R1 depends_on %v", got)
	}
	if r1.File != "hw/amp.kicad_pcb" || r1.Line != 10 || r1.EndLine != 25 || r1.Column != 2 || r1.EndColumn != 3 {
		t.Errorf("R1 span = %s %d-%d col %d-%d, want hw/amp.kicad_pcb 10-25 col 2-3", r1.File, r1.Line, r1.EndLine, r1.Column, r1.EndColumn)
	}
	u1 := mustFind(t, ff, facts.KindSymbol, "hw/amp.kicad_pcb#U1")
	if got := targets(u1, facts.RelDependsOn); !reflect.DeepEqual(got, []string{"hw/amp.kicad_pcb#net:/Sensor/SDA", "hw/amp.kicad_pcb#net:GND", "hw/amp.kicad_pcb#net:unconnected-(U1-NC-Pad4)"}) {
		t.Errorf("U1 depends_on %v, want each distinct net once and nothing for net 0, a missing number or a bare pad", got)
	}
}

func TestKiCad_ADuplicateReferenceSuffixesEveryFootprintSharingIt(t *testing.T) {
	ff := extractKiCad(t, ampProject())
	if f := find(ff, facts.KindSymbol, "hw/amp.kicad_pcb#TP1"); f != nil {
		t.Fatalf("a colliding reference kept its bare name: %+v", f)
	}
	placed := mustFind(t, ff, facts.KindSymbol, "hw/amp.kicad_pcb#TP1@dddddddd-0000-0000-0000-0000000000e1")
	wantProps(t, placed, map[string]string{"duplicate_reference": "true", "reference": "TP1", "board_only": "<absent>"})
	if got := targets(placed, facts.RelDeclares); !reflect.DeepEqual(got, []string{"hw/amp.kicad_sch"}) {
		t.Errorf("placed TP1 declares %v", got)
	}
	copyOnBoard := mustFind(t, ff, facts.KindSymbol, "hw/amp.kicad_pcb#TP1@dddddddd-0000-0000-0000-0000000000e2")
	wantProps(t, copyOnBoard, map[string]string{"duplicate_reference": "true", "board_only": "true"})
	wantProps(t, mustFind(t, ff, facts.KindSymbol, "hw/amp.kicad_pcb#R1"), map[string]string{"duplicate_reference": "<absent>"})
	wantCoverage(t, ff, "kicad:references", 5, "colliding=2")
}

func TestKiCad_ABoardOnlyFootprintDeclaresTheBoard(t *testing.T) {
	ff := extractKiCad(t, ampProject())
	logo := mustFind(t, ff, facts.KindSymbol, "hw/amp.kicad_pcb#G***")
	wantProps(t, logo, map[string]string{"board_only": "true", "footprint": "Symbol:OSHW-Logo"})
	if got := targets(logo, facts.RelDeclares); !reflect.DeepEqual(got, []string{"hw/amp.kicad_pcb"}) {
		t.Errorf("logo declares %v, want the board", got)
	}
	if got := targets(logo, facts.RelDependsOn); len(got) != 0 {
		t.Errorf("a footprint with no pads depends on %v", got)
	}
	j1 := mustFind(t, ff, facts.KindSymbol, "hw/amp.kicad_pcb#J1")
	wantProps(t, j1, map[string]string{"board_only": "<absent>"})
	if got := targets(j1, facts.RelDeclares); !reflect.DeepEqual(got, []string{"hw/amp.kicad_pcb"}) {
		t.Errorf("a part whose sheet is not in the tree declares %v, want the board", got)
	}
	wantCoverage(t, ff, "kicad:placement", 4, "no_path=2,path_not_in_tree=1")
}

func TestKiCad_NetsComeFromPadsOnly(t *testing.T) {
	ff := extractKiCad(t, ampProject())
	gnd := mustFind(t, ff, facts.KindSymbol, "hw/amp.kicad_pcb#net:GND")
	wantProps(t, gnd, map[string]string{"language": "kicad", "symbol_kind": "net", "pads": "5"})
	if got := targets(gnd, facts.RelDeclares); !reflect.DeepEqual(got, []string{"hw/amp.kicad_pcb"}) {
		t.Errorf("GND declares %v", got)
	}
	if gnd.Line != 6 {
		t.Errorf("GND line = %d, want its net table entry on 6", gnd.Line)
	}
	wantProps(t, mustFind(t, ff, facts.KindSymbol, "hw/amp.kicad_pcb#net:unconnected-(U1-NC-Pad4)"), map[string]string{"pads": "1"})
	for _, name := range []string{"hw/amp.kicad_pcb#net:", "hw/amp.kicad_pcb#net:GHOST"} {
		if f := find(ff, facts.KindSymbol, name); f != nil {
			t.Errorf("net 0 or a number the table does not hold drew %+v", f)
		}
	}
	wantCoverage(t, ff, "kicad:pads", 11, "net_not_in_table=1")
}

func TestKiCad_RailsFromPowerSymbols(t *testing.T) {
	ff := extractKiCad(t, ampProject())
	for _, n := range []string{"GND", "+5V"} {
		wantProps(t, mustFind(t, ff, facts.KindSymbol, "hw/amp.kicad_pcb#net:"+n), map[string]string{"rail": "true"})
	}
	wantProps(t, mustFind(t, ff, facts.KindSymbol, "hw/amp.kicad_pcb#net:/Sensor/SDA"), map[string]string{"rail": "<absent>"})
	if f := find(ff, facts.KindSymbol, "hw/amp.kicad_pcb#net:+12V"); f != nil {
		t.Errorf("a rail on no pad was emitted: %+v", f)
	}
	// PWR_FLAG is a power symbol with an output pin: were it read as a rail, the
	// count on no pad would be 2.
	wantCoverage(t, ff, "kicad:rails", 2, "on_no_pad=1")
}

func TestKiCad_ClassesFromThePatterns(t *testing.T) {
	ff := extractKiCad(t, ampProject())
	for net, want := range map[string]map[string]string{
		"GND":                      {"net_class": "Power", "net_class_defined": "true"},
		"+5V":                      {"net_class": "Power", "net_class_defined": "true"},
		"/Sensor/SDA":              {"net_class": "Signal", "net_class_defined": "false"},
		"unconnected-(U1-NC-Pad4)": {"net_class": "<absent>", "net_class_defined": "<absent>"},
	} {
		wantProps(t, mustFind(t, ff, facts.KindSymbol, "hw/amp.kicad_pcb#net:"+net), want)
	}
	wantCoverage(t, ff, "kicad:classes", 2, "claimed_by_two=1,undefined_class=1")
	wantCoverage(t, ff, "kicad:boards", 4, "")
}

// OpenFC's KiCad 10 board has no net table: every pad names its net inline.
func TestKiCad_KiCad10InlineNets(t *testing.T) {
	ff := extractKiCad(t, map[string]string{
		"fc.kicad_sch": `(kicad_sch
	(version 20260306)
	(generator "eeschema")
	(generator_version "10.0")
	(uuid "11111111-0000-0000-0000-000000000000")
	(lib_symbols
		(symbol "power:GND"
			(power global)
			(property "Value" "GND")
			(symbol "GND_1_1" (pin power_in line (name "GND") (number "1") (hide yes)))
		)
	)
	(symbol (lib_id "power:GND") (uuid "11111111-0000-0000-0000-0000000000a1")
		(property "Reference" "#PWR01") (property "Value" "GND"))
	(symbol (lib_id "MCU:RP2354A") (uuid "11111111-0000-0000-0000-0000000000a2")
		(property "Reference" "U10") (property "Value" "RP2354A"))
)
`,
		"fc.kicad_pcb": `(kicad_pcb
	(version 20260206)
	(generator "pcbnew")
	(generator_version "10.0")
	(footprint "lib:QFN-60"
		(layer "F.Cu")
		(uuid "22222222-0000-0000-0000-0000000000b1")
		(property "Reference" "U10")
		(property "Value" "RP2354A")
		(property "MPN" "RP2354A")
		(path "/11111111-0000-0000-0000-0000000000a2")
		(pad "1" smd rect (net "GND"))
		(pad "2" smd rect (net "/IMU/SPI1.MISO"))
		(pad "3" smd rect (net "GND"))
		(pad "4" smd rect (net ""))
		(pad "5" smd rect (net 7))
	)
	(via (at 1 1) (net "GND"))
)
`,
	})
	board := mustFind(t, ff, facts.KindModule, "fc.kicad_pcb")
	wantProps(t, board, map[string]string{"format_version": "20260206", "parts": "1", "nets": "2", "multi_pad_nets": "1", "single_pad_nets": "1", "sheet_files": "1", "net_classes": "<absent>"})
	gnd := mustFind(t, ff, facts.KindSymbol, "fc.kicad_pcb#net:GND")
	wantProps(t, gnd, map[string]string{"pads": "2", "rail": "true"})
	if gnd.Line != 0 {
		t.Errorf("an inline net has no table entry to point at, got line %d", gnd.Line)
	}
	wantProps(t, mustFind(t, ff, facts.KindSymbol, "fc.kicad_pcb#net:/IMU/SPI1.MISO"), map[string]string{"pads": "1"})
	u10 := mustFind(t, ff, facts.KindSymbol, "fc.kicad_pcb#U10")
	wantProps(t, u10, map[string]string{"value": "RP2354A", "field:MPN": "RP2354A"})
	if got := targets(u10, facts.RelDeclares); !reflect.DeepEqual(got, []string{"fc.kicad_sch"}) {
		t.Errorf("U10 declares %v", got)
	}
	wantCoverage(t, ff, "kicad:pads", 3, "net_not_in_table=1")
	wantCoverage(t, ff, "kicad:rails", 1, "")
}

// KiCad 6 spells the sheet fields with a space, names a footprint's reference and
// value in fp_text, and gives the footprint a tstamp rather than a uuid.
func TestKiCad_KiCad6SpellingsAndNumberedTable(t *testing.T) {
	ff := extractKiCad(t, map[string]string{
		"bot/main.kicad_pro": `{"meta": {"version": 1}, "net_settings": {"classes": [
  {"name": "Default", "nets": []},
  {"name": "POWER", "nets": ["GND", "/V_BATT", "/NOT_ON_BOARD"]}
]}}
`,
		"bot/main.kicad_sch": `(kicad_sch (version 20211123) (generator eeschema)
  (uuid 5c64ed50-0000-0000-0000-000000000000)
  (sheet (at 10 10) (size 20 20)
    (uuid 5c64ed50-0000-0000-0000-00000000aaaa)
    (property "Sheet name" "Motors" (id 0) (at 10 9 0))
    (property "Sheet file" "motors.kicad_sch" (id 1) (at 10 31 0))
    (pin "PWM" input (at 10 15 180))
  )
)
`,
		"bot/motors.kicad_sch": `(kicad_sch (version 20211123) (generator eeschema)
  (uuid 5c64ed50-0000-0000-0000-00000000bbbb)
)
`,
		"bot/main.kicad_pcb": `(kicad_pcb (version 20211014) (generator pcbnew)
  (net 0 "")
  (net 1 "GND")
  (net 2 "/V_BATT")
  (net 3 "/Motors/PWM")
  (footprint "bot:R_Axial" (layer "F.Cu")
    (tedit 5AE5139B) (tstamp 07b6c824-bd1d-412f-a132-82a59e0285f4)
    (property "Note" "any type, but small, 0.2\" span")
    (property "Sheetfile" "main.kicad_sch")
    (property "Sheet file" "main.kicad_sch")
    (property "Sheetname" "")
    (path "/5c64ed50-0000-0000-0000-00000000aaaa/9d000000-0000-0000-0000-000000000001")
    (fp_text reference "R11" (at 3.81 0.254 -90) (layer "F.SilkS")
      (effects (font (size 1 1) (thickness 0.15)))
      (tstamp 29ef3e6d-5693-4c0b-b380-339925af8286)
    )
    (fp_text value "4k7" (at 3.8 0 -90) (layer "F.Fab"))
    (pad "1" thru_hole oval (at 0 0 270) (net 2 "/V_BATT") (pintype "passive"))
    (pad "2" thru_hole oval (at 7.62 0 270) (net 3 "/Motors/PWM") (pintype "passive"))
    (pad "3" thru_hole oval (at 7.62 0 270) (net 1 "GND") (pintype "passive"))
  )
)
`,
	})
	wantProps(t, mustFind(t, ff, facts.KindModule, "bot/main.kicad_pcb"), map[string]string{"sheet_files": "2", "net_classes": "[Default POWER]"})
	motors := mustFind(t, ff, facts.KindDependency, "bot/main.kicad_sch -> bot/motors.kicad_sch")
	wantProps(t, motors, map[string]string{"sheet_name": "Motors", "pins": "1", "pins_input": "1"})
	r11 := mustFind(t, ff, facts.KindSymbol, "bot/main.kicad_pcb#R11")
	wantProps(t, r11, map[string]string{"reference": "R11", "value": "4k7", "footprint": "bot:R_Axial", "field:Note": `any type, but small, 0.2" span`, "field:Sheet file": "<absent>"})
	if got := targets(r11, facts.RelDeclares); !reflect.DeepEqual(got, []string{"bot/motors.kicad_sch"}) {
		t.Errorf("R11 declares %v", got)
	}
	for net, class := range map[string]string{"GND": "POWER", "/V_BATT": "POWER", "/Motors/PWM": "<absent>"} {
		wantProps(t, mustFind(t, ff, facts.KindSymbol, "bot/main.kicad_pcb#net:"+net), map[string]string{"net_class": class})
	}
	wantCoverage(t, ff, "kicad:classes", 2, "")
}

func TestKiCad_UnknownVersionAndUnparsableFilesAreCountedNotRead(t *testing.T) {
	ff := extractKiCad(t, map[string]string{
		"archives/old/PSLab.kicad_pcb": "(kicad_pcb (version 4) (host pcbnew 4.0.7)\n  (net 0 \"\")\n  (module R_0603 (layer F.Cu) (fp_text reference R1 (at 0 0)))\n)\n",
		"broken/b.kicad_pcb":           "(kicad_pcb (version 20221018) (net 1 \"GND\")\n  (footprint \"R\" (property \"Reference\" \"R1\")\n",
		"future/f.kicad_pcb":           "(kicad_pcb (version 20270101) (footprint \"R\" (property \"Reference\" \"R1\")))\n",
		"future/f.kicad_sch":           "(kicad_sch (version 20270101))\n",
		"ok/ok.kicad_pcb":              "(kicad_pcb (version 20240108) (net 1 \"GND\") (footprint \"R\" (uuid \"u1\") (property \"Reference\" \"R1\") (path \"/p1\") (pad \"1\" (net 1 \"GND\"))))\n",
		"ok/ok.kicad_sch":              "(kicad_sch (version 20231120) (uuid \"root\") (sheet (uuid \"s1\") (property \"Sheetfile\" \"old.kicad_sch\")) (sheet (uuid \"s2\") (property \"Sheetfile\" \"gone.kicad_sch\")))\n",
		"ok/old.kicad_sch":             "(kicad_sch (version 20200310))\n",
	})
	for _, name := range []string{"archives/old/PSLab.kicad_pcb", "broken/b.kicad_pcb", "future/f.kicad_pcb", "future/f.kicad_sch", "ok/old.kicad_sch", "ok/gone.kicad_sch"} {
		if f := find(ff, facts.KindModule, name); f != nil {
			t.Errorf("%s was refused and still emitted %+v", name, f)
		}
	}
	for _, f := range ff {
		if f.File == "archives/old/PSLab.kicad_pcb" || f.File == "broken/b.kicad_pcb" || f.File == "future/f.kicad_pcb" {
			t.Errorf("a refused board emitted %s %s", f.Kind, f.Name)
		}
	}
	// A footprint on a board whose schematic is refused is left on the board.
	wantProps(t, mustFind(t, ff, facts.KindModule, "ok/ok.kicad_pcb"), map[string]string{"sheet_files": "1"})
	if f := find(ff, facts.KindDependency, "ok/ok.kicad_sch -> ok/old.kicad_sch"); f != nil {
		t.Errorf("a sheet whose file was refused drew %+v", f)
	}
	wantCoverage(t, ff, "kicad:boards", 2, "missing=1,unknown_version_20200310=1,unknown_version_20270101=2,unknown_version_4=1,unparsable=1")
}

func TestKiCad_ASchematicWithNoBoardYieldsItsTreeOnly(t *testing.T) {
	ff := extractKiCad(t, map[string]string{
		"idea/idea.kicad_pro": `{"meta": {"version": 3}}`,
		"idea/idea.kicad_sch": `(kicad_sch (version 20250114) (uuid "r")
	(lib_symbols (symbol "power:VCC" (power) (property "Value" "VCC") (symbol "VCC_1_1" (pin power_in line (name "VCC") (number "1")))))
	(symbol (lib_id "power:VCC") (uuid "p1") (property "Reference" "#PWR1") (property "Value" "VCC"))
	(symbol (lib_id "Device:R") (uuid "r1") (property "Reference" "R1") (property "Value" "1k"))
	(sheet (uuid "s1") (property "Sheetname" "Child") (property "Sheetfile" "child.kicad_sch"))
)
`,
		"idea/child.kicad_sch":   "(kicad_sch (version 20250114) (uuid \"c\"))\n",
		"stray/lonely.kicad_sch": "(kicad_sch (version 20250114) (uuid \"l\"))\n",
	})
	mustFind(t, ff, facts.KindModule, "idea/idea.kicad_sch")
	mustFind(t, ff, facts.KindModule, "idea/child.kicad_sch")
	mustFind(t, ff, facts.KindDependency, "idea/idea.kicad_sch -> idea/child.kicad_sch")
	for _, f := range ff {
		if f.Kind == facts.KindSymbol {
			t.Errorf("a project with no board emitted the symbol %s", f.Name)
		}
	}
	if f := find(ff, facts.KindModule, "stray/lonely.kicad_sch"); f != nil {
		t.Errorf("a sheet no project reaches was read as %+v", f)
	}
	wantCoverage(t, ff, "kicad:boards", 1, "outside_any_project=1,schematic_without_board=1")
	wantCoverage(t, ff, "kicad:rails", 0, "on_no_pad=1")
}

func TestKiCad_NothingToReadEmitsNothing(t *testing.T) {
	dir := t.TempDir()
	files := []string{"main.go", "README.md", "board.kicad_pro", "lib/part.kicad_sym", "lib/part.pretty/R.kicad_mod"}
	if ok, _ := New().DetectFiles(dir, files); ok {
		t.Fatal("a project file, a symbol library and a footprint library are not a board or a sheet")
	}
	ff, err := New().Extract(context.Background(), dir, []string{"main.go", "README.md"})
	if err != nil || len(ff) != 0 {
		t.Fatalf("Extract over no KiCad file = %v, %v; want nothing, not a confident zero", ff, err)
	}
	for _, f := range []string{"a.kicad_pcb", "a.kicad_sch", "a.kicad_pro"} {
		if !New().OwnsFile(f) {
			t.Errorf("OwnsFile(%s) = false", f)
		}
	}
	if New().OwnsFile("a.kicad_sym") {
		t.Error("a symbol library is not read, so it is not owned")
	}
}

func TestKiCad_Deterministic(t *testing.T) {
	dir, rel := writeTree(t, ampProject())
	first := extractTree(t, dir, rel)
	reversed := append([]string(nil), rel...)
	sort.Sort(sort.Reverse(sort.StringSlice(reversed)))
	second := extractTree(t, dir, reversed)
	if !reflect.DeepEqual(first, second) {
		t.Fatal("two extractions of one tree differ")
	}
	a, errA := json.Marshal(first)
	b, errB := json.Marshal(second)
	if errA != nil || errB != nil || string(a) != string(b) {
		t.Fatalf("two extractions of one tree marshal differently (%v, %v)", errA, errB)
	}
}

func TestWildcard(t *testing.T) {
	for _, c := range []struct {
		pattern, name string
		want          bool
	}{
		{"GND", "GND", true},
		{"GND", "GNDA", false},
		{"+5?", "+5V", true},
		{"+5?", "+5", false},
		{"/ELRS/*", "/ELRS/WIFI_ESP", true},
		{"/ELRS/*", "/IMU/SPI", false},
		{"*_EN", "/Power/BUCK_EN", true},
		{"a*b*c", "aXXbYYc", true},
		{"a*b*c", "aXXbYY", false},
		{"[0]", "0", false},
		{"[0]", "[0]", true},
		{"gnd", "GND", false},
	} {
		if got := wildcard(c.pattern, c.name); got != c.want {
			t.Errorf("wildcard(%q, %q) = %v, want %v", c.pattern, c.name, got, c.want)
		}
	}
}
