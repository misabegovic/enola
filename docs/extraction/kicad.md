# KiCad: the board is the netlist

A KiCad 6 or later project is a handful of plain-text files sharing a stem: a
board (`.kicad_pcb`) and schematic sheets (`.kicad_sch`), both s-expressions
carrying a date-stamped format version, and a project file (`.kicad_pro`, JSON)
holding the net classes. They are read with a small s-expression reader and no
grammar dependency, the hand-rolled tradition HCL, gRPC and OpenAPI keep, and
nothing from KiCad is installed or run.

A schematic stores no nets: it stores wires, junctions and labels, and KiCad
solves the nets from their geometry. The board names the net on every pad. So
the board is read as the netlist, and the schematic contributes its sheet tree
and its power symbols only.

Fixture: [`kicad_sample`](../../internal/engine/testdata/repos/kicad_sample/)

## At a glance

| You write | enola stores | Kind |
|---|---|---|
| a `.kicad_pcb` board | a module named by its path, with its counts as props | `module` |
| each sheet file of the board's schematic tree | a module named by its path | `module` |
| a sheet symbol placing a child sheet | a dependency from the parent file to the child, `imports` the child | `dependency` |
| a footprint on the board | a part named `<board>#<reference>`, declaring the sheet it was placed from | `symbol` |
| a net that reaches at least one pad | a net named `<board>#net:<name>`, declaring the board | `symbol` |
| a pad on a net | `depends_on` from the part to the net, once per net | relation |
| a power symbol naming a net | `rail: true` on that net | prop |
| a class list or pattern in `net_settings` | `net_class` on each net it assigns | prop |
| net 0, an unconnected pad, a footprint's `Sheetfile` field | nothing | none |

## A board and its sheets are modules

```
module      ctl.kicad_pcb    props: format_version=20241229, parts=6, nets=5,
                                    multi_pad_nets=3, single_pad_nets=2, sheet_files=2,
                                    net_classes=[Default Power]
module      ctl.kicad_sch    props: format_version=20250114
module      mcu.kicad_sch    props: format_version=20250114
dependency  ctl.kicad_pcb -> ctl.kicad_sch   relations: imports -> ctl.kicad_sch
dependency  ctl.kicad_sch -> mcu.kicad_sch   props: sheet_name=MCU, pins=3,
                                                    pins_bidirectional=2, pins_input=1
            relations: imports -> mcu.kicad_sch
```

The tree is walked from the root sheet of the board's own stem (`ctl.kicad_sch`
beside `ctl.kicad_pcb`), following each sheet symbol's file field, both the
KiCad 6 spelling (`Sheet file`) and the later one (`Sheetfile`). A file already
in the tree is not walked twice, and a dependency is drawn only to a sheet that
was read, so no `imports` edge points at a module that is not there.

## Footprints become parts, placed by their UUID path

```
symbol  ctl.kicad_pcb#U1   ctl.kicad_pcb:29
        props: symbol_kind=part, reference=U1, value=RP2040,
               footprint=Package_DFN_QFN:QFN-56-1EP_7x7mm_P0.4mm
        relations: declares -> mcu.kicad_sch,
                   depends_on -> ctl.kicad_pcb#net:+3V3, ctl.kicad_pcb#net:/MCU/USB_D+,
                                 ctl.kicad_pcb#net:GND,
                                 ctl.kicad_pcb#net:unconnected-(U1-GPIO29-Pad41)
symbol  ctl.kicad_pcb#J1   ctl.kicad_pcb:14
        props: symbol_kind=part, reference=J1, value=USB_C, field:LCSC=C165948
        relations: declares -> ctl.kicad_sch,
                   depends_on -> ctl.kicad_pcb#net:/MCU/USB_D+, ctl.kicad_pcb#net:GND,
                                 ctl.kicad_pcb#net:VBUS
```

A footprint's `path` is the chain of sheet-symbol UUIDs down to the schematic
symbol it came from. One segment places the part on the root sheet; a longer
path places it on the file of the sheet symbol whose UUID is the last sheet
segment. The `Sheetfile` and `Sheetname` fields a footprint also carries are
never read: they travel with a footprint copied between projects, and on a real
board every footprint named a sheet file of another project while its path
named the right one.

A footprint with no path is on the board only (a logo, a mounting hole, a
copy): it carries `board_only` and declares the board. So does a footprint
whose path leads to no sheet in the tree, counted rather than guessed.

Every property the author set other than the reference, value, footprint and
sheet fields, and other than KiCad's own `ki_` fields, is carried as
`field:<name>`, so an author's `LCSC` or `MPN` cannot overwrite a prop the
engine owns. An empty field, or one holding KiCad's `~` placeholder, says
nothing and is not carried.

### A shared reference is told apart, not merged

```
symbol  ctl.kicad_pcb#TP1@5a1e0000-0000-4000-8000-0000000000e1
        props: duplicate_reference=true, reference=TP1
        relations: declares -> ctl.kicad_sch, depends_on -> ctl.kicad_pcb#net:GND
symbol  ctl.kicad_pcb#TP1@5a1e0000-0000-4000-8000-0000000000e2
        props: duplicate_reference=true, board_only=true, reference=TP1
        relations: declares -> ctl.kicad_pcb, depends_on -> ctl.kicad_pcb#net:GND
```

References are not unique on real boards. When two footprints share one, every
footprint sharing it is named with its own UUID, so neither name changes when an
unrelated copy is deleted. A reference nobody else uses keeps its bare name.

## Nets come from pads

```
symbol  ctl.kicad_pcb#net:GND    ctl.kicad_pcb:9
        props: symbol_kind=net, pads=6, rail=true, net_class=Power, net_class_defined=true
        relations: declares -> ctl.kicad_pcb
symbol  ctl.kicad_pcb#net:VBUS   ctl.kicad_pcb:11
        props: symbol_kind=net, pads=1
```

KiCad 6 to 9 boards declare a numbered net table and pads refer to it by number;
KiCad 10 boards have no table and name the net on every pad. Both are read. Net
0, an empty name and an unconnected pad draw nothing. A net reaching one pad is
still a net, KiCad's `unconnected-(...)` names included, and the board's
`single_pad_nets` counts them. A net's name keeps its hierarchical prefix
(`/MCU/USB_D+`), so two sheets' local nets never merge.

### Rails and classes

A power symbol is recognised by its embedded library symbol: KiCad's power
marker and a power-input pin. Its value names a global net, and a board net of
that name carries `rail`. `PWR_FLAG` sits in the same library with an output pin
and names nothing, and a connector's power-input pin (`VBUS` above) makes no
rail either.

Net classes come from `net_settings` in the project file: the member lists of
KiCad 6 and the `netclass_patterns` of later versions, read as wildcards where
`*` and `?` are the only special characters, so the `+` and brackets net names
often hold match themselves. A net one class claims carries `net_class`, with
`net_class_defined` false when the project never defines that class. A net two
classes claim carries neither, and a net nothing assigns carries no class: the
file says nothing, and KiCad's implicit default is not written in.

## What was not read is counted

Six extraction facts say what the reader saw and could not use, each keyed by a
named cause, and a repository with no board or sheet gets none of them:

| Fact | Counts | Causes |
|---|---|---|
| `kicad:boards` | boards and sheets read | `unknown_version_<v>`, `unparsable`, `unreadable`, `missing`, `schematic_without_board`, `outside_any_project` |
| `kicad:pads` | pads whose net resolved | `net_not_in_table`, `net_disagrees_with_table`, `net_unreadable` |
| `kicad:placement` | parts placed on a sheet | `no_path`, `path_not_in_tree`, `no_schematic`, `sheet_not_read`, `sheet_uuid_ambiguous` |
| `kicad:rails` | rails reaching a pad | `on_no_pad`, `board_not_read`, `not_global` |
| `kicad:classes` | nets given one defined class | `claimed_by_two`, `undefined_class`, `assignments_unread`, `project_unparsable`, `project_unreadable` |
| `kicad:references` | parts with a unique reference | `colliding`, `no_reference`, `no_uuid_to_tell_apart` |

## What is deliberately not extracted

- **The schematic net solve.** Wires, labels, buses and their scopes are not
  read; the board already holds the answer.
- **Parts of a project with no board.** Such a project yields its sheet tree
  and is counted as `schematic_without_board`.
- **Pads, pins and geometry.** No pad, pin, track, via, zone, stackup or outline
  becomes a fact, and no design-rule number is read.
- **Other formats.** KiCad 5 and earlier boards are refused by version; legacy
  `.sch` schematics, symbol and footprint libraries, and other tools' files are
  not read.
