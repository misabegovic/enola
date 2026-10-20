# Providers — measured facts enola did not extract

enola's own extractors parse source. A **provider** adds measured facts from somewhere
else: a tool enola does not ship, a booted application or a signature file. Each fact
states how it was resolved, and every run records which providers contributed. Declared
constraints can evaluate provider facts; see [CONSTRAINTS.md](CONSTRAINTS.md).

## The provider seam

A tool enola does not ship can contribute measured facts through the
engine config's `providers:` block: an executable run once with
`--version` and once with the repository path, emitting facts as JSONL
in the store's own schema. The contract is fail-closed end to end —
one invalid line rejects the provider's whole output, a provider fact
may not collide with an extractor fact's kind+name identity, and every
fact must carry a **`resolution_level`** prop: the basis on which the
provider resolved what it emitted (the same
vocabulary the Stimulus pass uses for its `markup-declared` binding
facts). The seam stamps provenance (`provider`, `provider_version`)
onto every accepted fact, and each run lands in the receipt's provider
**census** — including providers that contributed nothing and why. The
census is comparability: a delta whose two snapshots ran different
provider sets (`provider_set`) is never graded as a full verdict,
exactly as a differing extractor set is. `enola check` grades the
**intersection** — only facts from producers that ran on both sides,
the disputed provider's facts excluded by their stamped `provider`
prop and named in a partial verdict that says what went ungraded —
and still declines outright when fact identity itself is in doubt (a
different enola version or build, repository, or ignore set).

The `resolution_level` vocabulary is closed, for the same reason the
kind and relation vocabularies are — a level nothing knows how to
weigh is a claim nothing can act on: `constant-receiver`,
`lexical-self`, `name-only`, `literal-declared`, `markup-declared`,
`convention-derived`, `runtime-observed`, `declared`, and `resolved`.
`resolved` states that the provider resolved a name through the
language's own lookup rules (nesting, inheritance, the locked gems) to
one declaration: neither a receiver typing, nor a signature-file claim,
nor a path convention, which is why it is its own word.
Two producers reading one Ruby tree read many of the same call sites,
and they do not spell a receiver the same way: one engine writes a
singleton class as `<Owner>`, the other writes the plain constant, and
only one of them knows a class method from an instance one. The seam
owns the one spelling table and applies it after validation and before
merge, so no script learns another's notation. It then pairs call
relations across producers by file, line, receiver and method: a
relation two producers emitted identically is kept once, under the
first producer in name order, in the spelling that carries the scope
(`Board.find`, as the extractor names a class method), with a separate
prop **`resolution_agreement: agreement`** beside the producer's own
`resolution_level`, which is never rewritten. A site where the producers
resolved different receivers is left exactly as each emitted it and
counted in the receipt's provider block by shape: `differing`,
`alias-resolved` when the producer carried `resolution_cause: alias` for
that line, and `singleton-spelling`, a cause expected to read zero so a
notation the table does not cover shows up as a number. Agreed,
differing and one-sided counts per provider sit beside the census; a
difference is a count, never a vote and never a refusal.

`runtime-observed` is its own level, not a stronger static one: it
states that a **booted application** reported the fact, and a fact
carrying it must also carry an **`observed_via`** prop naming the
observation channel (`rails-boot`, `query-counter`) — runtime
provenance without a channel is a claim that cannot be re-derived.
`declared` is the mirror obligation on the static side: it states
that a **signature file claims** the fact — a type annotation, not
code observed or run — and a fact carrying it must also carry a
**`declared_in`** prop naming that signature file, because a
declaration is not source: the claim can drift from the
implementation, so a consumer must always be able to weigh it apart
from extracted and runtime truth, and find the file that made it.

A provider may additionally report its own coverage accounting over
one stderr line prefixed `enola-provider-census: ` — files seen,
declarations parsed, constructs skipped with named causes — which the
seam validates as strictly as the facts and carries into the
receipt's provider census, the same coverage discipline the engine's
file census applies to its own walk.

A provider with nothing to read in a repository (a lint reader with
no results file, a runtime collector with no capture) says so on one
stderr line prefixed `enola-provider-not-applicable: `, followed by
what was missing, emits no facts, and exits 0. The seam records it
as **not applicable** rather than as a run that found nothing: the
receipt carries `applicable: false` beside `skipped: true` with the
missing input as the `reason`, and a lint reader with no results file
no longer reads like one whose results are clean. A provider that
says it does not apply and emits facts is invalid output, skipped
like any other. The built-in Rubydex provider is not applicable where
there is no `Gemfile`, and says so before looking for its library.

## Which providers run

A **providers mode** decides, one of three:

- `declared` runs the config's `providers:` list and nothing else. It
  is what a list has always meant, and the mode of a build that says
  nothing.
- `every` runs each **catalogue** provider that applies to the
  repository and can run on the machine, with the config's list beside
  it: a listed entry replaces the catalogue entry of its name, so a
  config can pin or relocate one provider and still take the rest.
- `none` runs nothing, list or not.

The mode is resolved once, first match winning: the config's
`providers_mode` key; `declared` when the config lists its own
`providers:`, since an explicit list is the config saying what to run;
the `ENOLA_PROVIDERS` variable; the build's default, a package
variable a linker flag may set
(`-X github.com/enola-labs/enola/internal/providers.DefaultMode=every`),
which is empty, meaning `declared`, in the source. So a build that
sets nothing generates exactly what it generated before, and a config
that names no provider hashes exactly as before.

The **catalogue** is what the binary can offer without a config: the
built-in Rubydex provider under its two names, and the example scripts found in one
**providers directory**, laid out as `examples/providers` is. The
directory is the one `ENOLA_PROVIDERS_DIR` names, else the share
directory a build names by the `ShareDir` linker variable (a relative
value is taken under `$XDG_DATA_HOME`, else `~/.local/share`), else
none. Each entry says what makes it apply:

| Entry | Applies where | Runs |
|---|---|---|
| `rubydex` | a `Gemfile` | in-process, from its fetched library |
| `prism` | a `Gemfile` | in-process, from the same index as `rubydex` |
| `rbs` | Ruby, RBS or RBI files | the script |
| `zeitwerk` | Ruby files | the script |
| `runtime` | a `.enola-runtime/*.json` capture | the script |
| `eslint` | an ESLint results file (`ENOLA_ESLINT_RESULTS`, else `tmp/eslint-results.json`) | the script, under Node |

The Prism reference script is not in the catalogue: alone it spells a
constant receiver's call `Const#m`, not knowing a class from a value
constant, and binds a call inside `def self.x` to the instance method,
where the built-in `prism` writes the same facts with the receiver
Ruby resolves. A config that names `prism` with a command still runs
the script, and beside Rubydex the seam respells what both read. The
Rubydex reference script is not in the catalogue: beside the built-in it binds nothing the built-in does not,
at two to three times the time. ESLint is offered only with a results
file, and the engine never sets `ENOLA_ESLINT_RUN`: running `npx` may
download ESLint, and a snapshot installs nothing.

A script entry runs under an **interpreter named once**: `ENOLA_RUBY`
or `ENOLA_NODE`, else the config's `interpreters:` block
(`interpreters: {ruby: /path/to/ruby}`), else the first `ruby` or
`node` on PATH. A Ruby named by either setting runs without `GEM_HOME`
and `GEM_PATH`, which a version manager exports for its own Ruby and
which would point another Ruby at gems built for that one. The seam's
version probe is the test: an interpreter that cannot load a script is
a skip whose reason names the interpreter and the first line it
printed. A listed provider's command is never rewritten; it names its
own interpreter.

A directory packed beside a binary carries `provider-versions.json`,
the version each script stated when it was packed, keyed by its path
under the directory. Those are the catalogue's pins, so a mismatch
means the script beside the binary is not the one it shipped with. A
directory without the record runs its scripts unpinned; one whose
record does not parse runs none of them.

Every `--generate`, `baseline pin` and `check` ends, after everything
else on standard error, with one summary line per provider the
catalogue or the list names: `ran` with its version and fact count,
`skipped` with the reason, or `not applicable` with what was missing.
A version mismatch is a skip there. With no provider in play the
summary is empty and the output is what it always was. The exit code
never changes: a provider does not fail a snapshot, and the summary is
where a skip is seen.

```
Providers (every):
  eslint    not applicable: no ESLint results file (ENOLA_ESLINT_RESULTS, or tmp/eslint-results.json)
  prism     ran 0.2.0, 64185 facts
  rbs       ran 0.1.0, 0 facts
  rubydex   ran 0.4.0, 24754 facts
  runtime   not applicable: no .enola-runtime/*.json capture
  zeitwerk  ran 0.1.0, 1149 facts
```

`providers list` prints the catalogue on this machine: the mode a
snapshot here would run in and what decided it, the providers
directory and where it came from, and each entry with its version,
what it applies to, and whether it can run and why not. `--json`
gives the same; a config path, if given, supplies its `interpreters:`
and its mode.

## Reusing provider facts

Provider facts enter the engine's extractor cache, under the same
version and build stamps, so a provider whose inputs did not move is
not run again. A provider whose output partitions by file declares it
in its entry, `files: per-file`, with the `extensions:` it reads; the
seam then keeps one entry per file, keyed by the provider's name, its
reported version and the file's content digest, hands the provider only
the files with no entry (the repository path, then `--files` and the
path of a listing, one repo-relative path per line), and merges the
cached facts for the rest. A fact the provider emits about a file it was
not handed is dropped and counted, so a script cannot widen its own
scope. A provider that declares nothing runs whole-tree on every
snapshot as before; a provider that reads across files must not declare
per-file, because the cache would serve facts computed against a tree
that has since changed. The built-in Rubydex provider keeps one
whole-index entry instead, keyed by the engine library version, the
Ruby file set with content digests and `Gemfile.lock`; a hit reuses the
facts and the census as recorded, a miss rebuilds the index, never
patches it.

The receipt's provider record carries a `reuse` block whenever the
cache was consulted for a provider that ran: `reused` and `computed`
fact counts, the files behind them for a per-file provider, facts
dropped as `outside_scope`, and for the whole-index case `cache: hit`
or `miss` with what the key did not match (`files`, `lockfile`,
`version`, or `cold` when no index had been recorded). A skipped
provider carries no reuse block, and a run without a cache carries
none either: absent means not asked, never zero. A cold and a warm
snapshot of one tree produce byte-identical facts, which the suite
asserts.

## The Rubydex provider

Rubydex, the shared Ruby analysis engine, is a provider the binary
carries itself. A `providers:` entry named `rubydex` with no `command`
runs it in-process: the engine's C-ABI library, which every platform gem
ships prebuilt, is loaded at run time (no cgo, no Ruby interpreter, no
gem in the measured repository's bundle) from enola's cache, where
`enola providers fetch rubydex` puts it after downloading the pinned
gem version from rubygems.org and verifying its published digest. A
configured provider whose library is absent is a named skip in the
census that says which command installs it; `doctor` reports the same.
Fetching is the only network access a provider makes, never at snapshot
time. Dependency gem paths come from the repository's own bundle
(`bundle list --paths`) when `bundle` is on PATH; without it the
workspace alone is indexed and the census says so. A reference
implementation in Ruby stays at `examples/providers/ruby/rubydex/` for
an installation that prefers an external process; both emit the same
facts.

The provider indexes the workspace, resolves it, and emits the three
things enola's own Ruby extractor and the Prism provider do not: constant
references resolved through Ruby's nesting and inheritance rules
(`rubydex-ref:`, a `depends_on` edge at `resolved`), method calls whose
receiver resolves to a constant other than the lexical enclosing class
(`rubydex-call:`, a `calls` edge at `constant-receiver`; the enclosing
class is the extractor's to say), and each class's linearised ancestor
chain with mixins in resolution order (`rubydex-ancestor:`, one
`implements` edge per ancestor at `resolved`, carrying the ancestor's
distance and whether the workspace declares it). Only facts located in
the workspace are emitted; built-in ancestors are omitted because every
class reaches them; unresolved references and Rubydex's own diagnostics
are counted in the census rather than guessed around. The ancestry edges
are what the `ancestor:` component key reads.

A `providers:` entry named `prism` with no `command` reads the same index
in the same run and writes what the Prism reference script writes, under
its names: a call whose receiver is the lexical enclosing class
(`prism-call:`, a `calls` edge, at `lexical-self` when the source writes
`self` or no receiver and `constant-receiver` when it writes the
constant), and `new` on a written constant (`prism-new:`, an
`instantiates` edge at `constant-receiver`, with `one_shot_call` naming
the message sent to the new object in the same expression when Rubydex
reads a call of that name exactly there). The callee is the receiver
Rubydex resolved, so `Board.find` inside `Board` is the singleton the
script spells `Board#find`. Each fact carries `engine: rubydex` beside
the `provider: prism` the seam stamps, and the seam never pairs two
names of one engine as an agreement. A call whose receiver Rubydex types
as no constant, which the script writes name-only, is counted in the
census and not written; a call on another class is the `rubydex-call:`
fact. An implicit or `self` receiver that Rubydex types as the class
inside an instance method is not written either and is counted as such:
Rubydex 0.4.0 reads a default parameter's value before it enters the
method, so `def route!(base = settings)` types `settings` on the class,
where Ruby evaluates it on the instance. Its version is the library's.

## The runtime provider

`examples/providers/ruby/runtime/enola_runtime_provider.rb` is the reference
collector for runtime-observed facts. It reads capture files from
`.enola-runtime/*.json` in the target repository — captures an
operator produced by running the app, never something the snapshot
produces — and emits them through the seam. Two capture schemas are
recognized: the booted-Rails capture (`source: "enola runtime"`,
the final route table plus reflected associations and table bindings,
which only exist after boot) and the query-counter capture
(`source: "activesupport-notifications"`, database queries per
application frame measured under a spec run). Facts are namespaced
(`runtime-route:`, `runtime-association:`, `runtime-storage:`,
`runtime-queries:`) so they add observations without colliding with
the identities the extractors own, and every fact carries
`resolution_level: runtime-observed` plus its `observed_via` channel.

The contract is fail-closed end to end: a boot capture reporting any
`unreachable` subject is refused whole (an incomplete boot must not
become partial truth), an unrecognized capture source is refused by
name, and a repository with no captures reads not applicable, with the
capture directory named: never an error, and never a run that observed
nothing. After the merge, the engine
cross-links observations to measurements: an extracted route fact
whose method and path a `runtime-route:` observation reports gains
**`runtime_observed: true`** and the merged, sorted `observed_via`
set, so runtime truth is queryable on the measured graph
(`query_facts(kind=route, prop=runtime_observed, prop_value=true)`)
and constraint rules can evaluate it (a `require` on
`observed_via`, a `forbid_fact` over a component selecting
observations). Runtime truth informs the graph; it never gates
anything by itself — observations carry no linker verdicts
(`unmatched_by_clients` never lands on one) and stay out of the
unused-routes censuses, because an observation of the booted
application is not a static route the linker could assess.

## The RBS/Sorbet provider

`examples/providers/ruby/rbs/enola_rbs_provider.rb` brings declared Ruby types into
the graph as facts. One provider covers both signature dialects: RBS
files (`**/*.rbs`), Sorbet interface files (`**/*.rbi`), and inline
Sorbet `sig { }` blocks in `**/*.rb` — pure-Ruby stdlib parsing (a
conservative hand parser, `json` only), deliberately not the `rbs`
gem: the gem's parser is a native extension whose rendering drifts
across gem versions, and a fact stream that depends on which rbs a
machine has installed is not deterministic. The hand parser reads the
common declaration forms and **fails closed by name** on everything
else — an attr declaration, a mixin, a type alias, an unrecognized
sig chain link each land in the census as a counted skip cause, never
as a guessed fact, and a structurally broken signature file is
discarded whole with its already-parsed declarations retracted from
the parsed count.

Two fact shapes, both `symbol` facts at level `declared` with
`declared_in` pointing at the signature file: **method contracts**
(`rbs-signature: Billing::Ledger#record`, carrying receiver, method,
singleton, the rendered signature, per-parameter declarations —
`untyped` and `T.untyped` recorded, never omitted — the return type,
overload counts where RBS declares overloads, and one `has_method`
relation targeting the method identity) and **type declarations**
(`rbs-decl: Billing::Ledger`, carrying `decl_kind`
class/module/interface, type parameters where generic, and the
declared superclass). The namespaced names add declarations without
colliding with the identities the extractors own.

After the merge the engine cross-links claims to measurements,
mirroring the runtime cross-link: an extracted symbol whose exact
class+method identity a declared contract names gains **`typed:
true`**, the merged sorted `declared_signature` summary, and the
merged sorted `declared_in` file set — never touching the extractor's
account of the symbol itself. Declared truth is then queryable
(`query_facts(kind=symbol, prop=typed, prop_value=true)`) and
constraint rules can evaluate it — a `require` on `declared_in`
over an API component, a `forbid_fact` over a component selecting
retired contracts — with every finding citing the signature file that
made the claim. A declaration is a claim about the implementation,
not proof of it: the provider records what the signature file says,
the level says who said it, and nothing presents the claim as
inferred or verified.
