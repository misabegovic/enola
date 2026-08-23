# munola release notes

This file is read by the munola release workflow. A release is refused unless
it carries a "Differs from upstream" section naming what this build adds to
or changes in the upstream version it descends from.

## Differs from upstream

- Built from the `upstream-current-sync` lineage at the tagged commit; the
  upstream base version is the first three segments of the tag.
- List every change here that is not in that upstream release, one line each,
  with the upstream pull request number where one exists.
