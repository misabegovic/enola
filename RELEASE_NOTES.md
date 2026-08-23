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
- Every `constraints` subcommand exits as itself; upstream v0.4.4 exits 1
  after `init` has written its file.
- `constraints init` binds a recipe role by the role's own match default, so a
  carried recipe can bind at all.
- The Rubydex provider indexes a Rails engine's `app/` directory beside its
  `lib/`, so engine constants resolve.
