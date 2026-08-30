# munola release notes

This file is read by the munola release workflow. A release is refused unless
it carries a "Differs from upstream" section naming what this build adds to
or changes in the upstream version it descends from.

## Differs from upstream

guides: v0.3.1

- Built from the carved `current` channel at the tagged commit; the upstream
  base version is the first three segments of the tag.
- The recipe catalogue from the enola-guides gem is built in: api-boundaries,
  background-work, data-ownership and ember-conventions bind through
  `constraints init` when their roles resolve; held identical to the gem's
  files at the tag the `guides:` line names.
- `constraints init` binds a recipe role by the role's own match default, so a
  carried recipe can bind at all. A default of one segment is not a place and
  is left unbound.
- Cache versions v242 through v244 are this channel's own, so upstream's
  releases of the same numbers are carried here as v245 and v246; the
  changelog comments in `internal/engine/cache.go` record both histories.
  `cacheVersion` itself matches upstream's v257, so caches interoperate.
- `pkg/check/blame.go` spells its hex check as two ranges ORed rather than a
  double negation, with the test asserting both spellings agree.

## Prior releases

`munola-v0.4.6.1` descended from upstream v0.4.6 and still carried the
exclusion filter; upstream took it in v0.4.7 (enola-labs/enola#255), so it
leaves the list the way the walk fix left it at 0.4.6: by being contributed.
The constraints reference this build once carried inside `docs/INTENT.md`
shipped upstream as `docs/CONSTRAINTS.md` and `docs/PROVIDERS.md` over
v0.4.5. What remains above is what upstream has not taken.
