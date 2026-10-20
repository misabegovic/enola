# munola release notes

This file is read by the munola release workflow. A release is refused unless
it carries a "Differs from upstream" section naming what this build adds to
or changes in the upstream version it descends from.

## Differs from upstream

guides: v0.3.1

- Built from the carved `current` channel of misabegovic/mu-enola at the
  tagged commit; the upstream base version is the first three segments of the
  tag, here v0.4.32.
- The recipe catalogue from the enola-guides gem is built in: api-boundaries,
  background-work, data-ownership and ember-conventions bind through
  `constraints init` when their roles resolve; held identical to the gem's
  files at the tag the `guides:` line names.
- `constraints init` binds a recipe role by the role's own match default, so a
  carried recipe can bind at all. A default of one segment is not a place and
  is left unbound.
- `enola check -member <repo>` grades one member of a multi-repository config
  against the union pin by replaying it: `baseline pin` writes `members.json`
  beside its artifacts with each member's provenance, and the check restores
  the pinned bundle, re-reads the named member alone into it and compares on
  that member's own provenance. A pin without member provenance, an extractor
  that no longer matches the pin's, or a member whose label moved since the
  pin is a named decline rather than a wrong grade.
- A constraints sentence written in snake_case reaches a recipe published in
  kebab-case: `:ember_conventions` binds the `ember-conventions` recipe and
  `bind :view_components` fills the `view-components` role, since the recipe
  name, the instance name and every bound role pass through the same
  component token.
- The module-edges binder keys symbol placement by (repository, name) rather
  than by name alone, credits a call to the module of the file that makes it
  rather than the module that won the name, refuses a target that two
  production modules of one repository declare (counted as
  `ambiguous_declaring_module` among the unresolved), counts a target placed
  only in another repository as a cross-repository landing rather than
  unresolved, and reads an import that names an item through the file the
  item's producer carried.
- Cache versions are not upstream's: `cacheVersion` is v427 here and v390
  upstream, so a cache written by one build is not read by the other. This
  channel's own entries take the next free number above upstream's at each
  align, and the changelog comments in `internal/engine/cache.go` record both
  histories.
- The built-in Rubydex provider fetches the Rubydex 0.4.1 library, where
  upstream fetches 0.4.0, with the sha256 pins of its four platform gems.
- A providers mode decides which providers run: `declared` runs the config's
  list as before, `every` also runs each catalogue provider that applies and
  can run on the machine, `none` runs nothing. It is set by `providers_mode`,
  by `ENOLA_PROVIDERS` or by the build. `providers list [--json]` prints the
  catalogue, and every generate, pin and check ends with one stderr line per
  provider saying `ran`, `skipped` or `not applicable`. A provider with
  nothing to read says so rather than reporting a run that found nothing, and
  the receipt carries `applicable: false` beside it.
- `prism` is a built-in provider name: with no command it answers the Prism
  script's names from the Rubydex index already loaded, so a repository gets
  those facts with no Ruby installed, and the seam does not count one engine
  as two producers agreeing. The provider cache serialises its writers, so
  two providers writing at once no longer corrupt the spool or panic a
  generate.
- Test cases are facts. A `test_case` fact names a test as its runner prints
  it, for Go, Python, Ruby (RSpec and minitest), TypeScript and JavaScript,
  Java, Kotlin, Scala, C#, PHP, Swift, Dart, C and C++ and Rust, and a test
  defined twice under one name is one fact. Facts of every one of those
  languages but Ruby, and of Terraform blocks, proto declarations and markdown
  sections, carry their node's span: end line and start and end columns beside
  the line.
- `context_pack` and `enola pack` answer which lines to open within a stated
  token budget: the ranges of the tests and symbols named, then whole files,
  never over the budget. `show_symbol` can be bounded by a span.
- The MCP server has twenty-six tools where upstream has twenty-two: the four
  added are `context_pack`, `constraints_ledger`, `constraints_lint` and
  `grade`, the last answering what `check` prints without writing anything.
  Most tools list an output schema and return structured content. A tool
  that answers for one repository takes `repo` and refuses a union without it.
  Every result is capped at 8000 estimated tokens by default, cut on a line
  boundary with a notice, and `max_tokens` replaces the cap.
- `enola --small` serves a few read tools with one-line descriptions, compact
  JSON and a 2000-token default cap, for clients with a small context window.
- `--listen <path>` (or `ENOLA_LISTEN`) also serves MCP on an owner-only unix
  socket, each connection a session over the graph the server already loaded,
  found through the instance record. The handshake reports the engine's
  extractor version, snapshot id, repositories and whether the binary was
  replaced since the server started; each loaded repository's receipt is a
  resource that subscribers hear about on reload; `generate_snapshot` and a
  `grade` that must re-read the tree send progress notifications.
- `find_path` takes `from_nodes` and `to_nodes` and answers the shortest path
  from any source to any target in one search. `enola path`, `enola impact`
  and `enola pack` run the tools at the command line, and `impact` on a file's
  path reaches what depends on anything the file declares.
- `plan_check` reads each member's fan-in from one index of the relations as
  written, and keeps its contract store between calls while the snapshot and
  the declaration stand, so a warm answer over a very large union takes
  milliseconds. A resident engine reads a file again for the drift check only
  after its size or modification time moved. The startup restore is the
  outermost middleware, so no tool runs before it has published.
- `enola check` grades the snapshot on disk, without regenerating, when no
  file has moved since it was written under the same build and config. It
  carries a verdict of any size instead of failing past 16 MiB, and a
  directory whose config lists a cluster says that it is read alone and how
  many listed repositories it leaves unread. A read-only check links the
  cluster once, at its last turn.
- Artifacts a generate writes are new files renamed into place, never written
  into, so `previous/` and a baseline pin take hard links where the filesystem
  allows and copies where it does not, and hold their bytes either way.
- Constraints gain vocabulary: `forbid`, `allow` and `protect` rules over
  `reads` and `writes` edges (refused with `no_access_edges` where none are measured);
  a component `service` that takes any repository label as written; a fact
  that is a member of more than one component; a component name read in the
  rule's own repository first; a runtime query budget over a capture that
  holds no frame in the component's files is refused with
  `no_frame_in_members` rather than kept; a Rust import outside the
  repository (std, core, alloc, or a crate fetched from elsewhere) is counted
  apart from targets a rule reached no verdict on; and a calls or
  instantiates target counts as measured on another repository's fact only
  when it is a symbol, never a service node.
- The authoring loop reads more. `constraints mine --by-component` mines over
  the parts a declaration names, prints each candidate's support, holds back
  what is already declared and honours `enola/mine-rejections.yaml`;
  `constraints explain` names a declared part's members, edges by kind and
  fan-in; `plan` and `explain` bind a part to name rules and say a forbid
  over literal names; `constraints ledger` reads the union a config names,
  takes `--repo` and `--json --breaches`; `constraints lint --json` answers
  as data and names a key nothing reads; a rule may carry `said:`; `contract`
  prints the closed sets the engine validates with, and the JSON answers have
  schemas under `docs/schema`.
- A page anchor may carry `symbol:`, so the reverse query for a symbol returns
  the page only for that symbol and the methods of a class it names, and a
  build may be linked to read page extensions beside `.md`.
- Ruby: a method reads or writes a model's table (`reads` and `writes` edges)
  only where the call's receiver names the model exactly; a job's declared
  queue is a topic fact, and an enqueue onto it is a producer at the caller;
  each Rails migration is read for what it would do to a live table; deploy
  files (Procfile, Sidekiq and Solid Queue configs, schedules, Kamal, fly.toml
  and others) become `runtime_unit` facts with `runs` edges to the job classes
  they reach, and a root carries the runtime that runs it. Each of these counts
  what it declined to read on a coverage fact.
- Ruby and TypeScript calls to a URL with a host written out are external
  client routes, as Go, Scala, Swift and Kotlin already record them; a host
  holding an interpolation is counted and emits nothing, and an absolute URL
  on a comment line is not read.
- Ruby calls bind more cautiously than upstream's call binder: a bare name the
  walker wrote for a call on another receiver, for `super`, for
  `self.class.name` or for the bare `new`, a call a subclass overrides, and a
  constant two enclosing scopes could name all keep the name written instead
  of binding to one declaration. A call keeps its `target_id` only onto a
  symbol or an association, or in a union onto a fact of its own repository
  that is not a service node.
- The Ruby reader names the files whose tree-sitter tree held an error node on
  a `ruby:parse` fact and counts each in the receipt's `parse_errors`, where it
  used to report none. The vendored Ruby grammar carries a patch so anonymous
  block forwarding followed by whitespace parses, and those files are read
  whole.
- Rust calls and `use` leaves resolve in one crate-wide pass over per-file
  records, from the crate roots Cargo builds, by path and by written receiver;
  an item lands on its fact and carries the file that declares it, every
  refusal is counted by cause on `rust:calls` and `rust:imports` facts, and a
  dependency declared with a path carries it as a `path` prop. Rust items,
  `use` declarations and routes carry spans, and `#[test]` functions are named
  as cargo lists them.
- A Java method or class and a Kotlin function, class, object or property keep
  every calls, instantiates and injects relation their walk records, where
  upstream's walker loses the relations recorded after a nested declaration
  grows its fact slice. A Terraform block is recorded on the line of its
  keyword, not on a blank line above it.
- KiCad 6 to 10 projects are read, board first: boards and schematic sheets as
  modules, footprints and pad nets as symbols, parts placed on sheets by UUID
  path, power rails and net classes marked. A repository with no board or
  sheet runs nothing.
- `install --hooks` gives pi the two session hooks as an extension, the preview
  says per target which hooks it will write or `no hook`, and the pi extension
  no longer throws when the server exits before reading its input.
- `enola upgrade` refuses every install method but `source` and names the
  installer that owns the file, the update check is silent in a build that
  names no manifest, and the server's instance record carries the path, size
  and modification time of the binary it started from.
- `pkg/check/blame.go` spells its hex check as two ranges ORed rather than a
  double negation, with the test asserting both spellings agree.

## Prior releases

`munola-v0.4.19.1` descended from upstream v0.4.19. It carried the catalogue,
the init role binding, the member check, the snake_case recipe reach, the
module-edges keying, the cache-version double history and the blame.go
spelling, all kept above, and four things upstream has since taken: the Rubydex
provider's one dependency per qualified read, the provider seam's cache of
per-file and whole-index facts, its pairing of calls two producers read
identically (this channel's v242 through v244, upstream's v254 through v256),
and the module-edges binder dropping its own prior derivations before it
derives. `munola-v0.4.10.1` descended from upstream v0.4.10 and carried the
catalogue, the init role binding, the cache-version double history and the
blame.go spelling. `munola-v0.4.6.1` descended from upstream v0.4.6 and still
carried the exclusion filter; upstream took it in v0.4.7
(enola-labs/enola#255). The constraints reference this build once carried
inside `docs/INTENT.md` shipped upstream as `docs/CONSTRAINTS.md` and
`docs/PROVIDERS.md` over v0.4.5. What remains above is what upstream has not
taken.
