# munola release notes

This file is read by the munola release workflow. A release is refused unless
it carries a "Differs from upstream" section naming what this build adds to
or changes in the upstream version it descends from.

## Differs from upstream

guides: v0.3.1

- Built from the `upstream-current-sync` lineage at the tagged commit; the
  upstream base version is the first three segments of the tag.
- The recipe catalogue from the enola-guides gem is built in: api-boundaries,
  background-work, data-ownership and ember-conventions bind through
  `constraints init` when their roles resolve; held identical to the gem's
  files at the tag the `guides:` line names.
- `constraints init` binds a recipe role by the role's own match default, so a
  carried recipe can bind at all. A default of one segment is not a place and
  is left unbound.
- The built-in Rubydex provider is told what the repository configuration
  excludes, so an excluded document is skipped before its definitions are read
  and an excluded reference before its fact is built (enola-labs/enola#255,
  merged upstream 08:48Z on 2026-08-24, an hour after v0.4.6 was published, so
  the release this descends from does not carry it). On a Rails monolith the
  snapshot goes from 440 seconds to 193 with the fact set byte for byte
  identical.
- Provider facts are cached inside the engine, and the receipt says what the
  run reused and what it recomputed.
- Receivers are spelled once across providers: what two producers emitted
  identically is kept once and stamped as agreement, and differing receivers
  stay as emitted and are counted by shape in the receipt.
- A Rubydex dependency lands on the file that defines the leaf it names,
  rather than on a reopening of the same constant.
- The constraints reference carries the constraints directory, the component
  vocabulary and the rule forms built on it; upstream v0.4.6 documents the
  three carriers and stops there.
- Cache versions v242 through v244 are this channel's own, so upstream's v242
  and v243 are carried here as v245 and v246. The numbers differ from a stock
  build of the same code; the facts do not.

## Prior releases

`munola-v0.4.4.1` and `munola-v0.4.4.2` descend from upstream v0.4.4. Their
notes named a longer list, most of which upstream has since taken: the prefix
walk fix shipped in v0.4.6 as enola-labs/enola#254, and the constraints
program, SARIF output and `constraints explain` arrived over v0.4.5 and
v0.4.6. What remains above is what upstream has not taken yet.
