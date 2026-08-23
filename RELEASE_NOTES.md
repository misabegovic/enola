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
- SARIF and host annotations beside text and JSON, so a verdict shows on the
  diff where the change was made.
- `constraints explain` answers who reaches a file and which verdicts change
  if it leaves its part.
- A Rubydex dependency lands on the file that defines the leaf it names,
  rather than on a reopening of the same constant.
- A corpus run records the identity of every finding it held and judges it, and
  a promotion refuses to drop a finding judged a true positive.
- Provider facts are cached inside the engine, and the receipt says what the
  run reused and what it recomputed.
- A breach is dated by its witness line where the history store cannot reach
  back far enough to date it directly.
- Receivers are spelled once across providers: what two producers emitted
  identically is kept once and stamped as agreement, and differing receivers
  stay as emitted and are counted by shape in the receipt.
- Every verdict carries one line naming what the run could not see.
- Every `constraints` subcommand exits as itself; upstream v0.4.4 exits 1
  after `init` has written its file.
- Recipe expansion rebinds every rule form, so a role bound at expansion holds
  for the forms written after it.
- The Rubydex provider indexes a Rails engine's `app/` directory beside its
  `lib/`, so engine constants resolve.
