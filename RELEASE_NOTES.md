# munola release notes

This file is read by the munola release workflow. A release is refused unless
it carries a "Differs from upstream" section naming what this build adds to
or changes in the upstream version it descends from.

## Differs from upstream

guides: v0.3.1

- Built from the carved `current` channel of misabegovic/mu-enola at the
  tagged commit; the upstream base version is the first three segments of the
  tag.
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
  than by name alone, breaks a within-repository collision by the smallest
  module rather than the last read, counts a target placed only in another
  repository as a cross-repository landing rather than unresolved, and drops
  its own prior derivations before deriving, so a second bind or a restored
  baseline replay adds no phantom edges. Upstream's unresolved-reason
  breakdown from 0.4.13 runs over the residual unresolved bucket.
- Cache versions v242 through v244 are this channel's own, so upstream's
  releases of the same numbers are carried here as v245 through v247; the
  changelog comments in `internal/engine/cache.go` record both histories.
  `cacheVersion` itself matches upstream's v265, so caches interoperate.
- `pkg/check/blame.go` spells its hex check as two ranges ORed rather than a
  double negation, with the test asserting both spellings agree.

## Prior releases

`munola-v0.4.10.1` descended from upstream v0.4.10 and carried the catalogue,
the init role binding, the cache-version double history and the blame.go
spelling; the member check, the snake_case recipe reach and the module-edges
keying were promoted into `current` after it was cut and ship here for the
first time. `munola-v0.4.6.1` descended from upstream v0.4.6 and still carried
the exclusion filter; upstream took it in v0.4.7 (enola-labs/enola#255). The
constraints reference this build once carried inside `docs/INTENT.md` shipped
upstream as `docs/CONSTRAINTS.md` and `docs/PROVIDERS.md` over v0.4.5. What
remains above is what upstream has not taken.
