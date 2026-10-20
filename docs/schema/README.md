# Snapshot artifact format

This directory documents the files written by `enola --generate` to the
configured output directory, `.enola/` by default. These files are the contract
for external snapshot consumers.

## Artifacts

| File | Format | Contract |
|---|---|---|
| `facts.jsonl` | JSON Lines, one fact per line | [facts.md](facts.md) |
| `insights.json` | JSON array of findings | [insights.md](insights.md) |
| `receipt.json` | JSON object with provenance and extraction quality | [receipt.md](receipt.md) |
| `flows.json` | JSON object ranking the ways into the program | [flows.md](flows.md) |
| `docs.jsonl` | JSON Lines, one documented fact's whole comment per line | [docs.md](docs.md) |
| `snapshot.meta.json` | Internal superset of the receipt | Not stable |
| `llm_context.md` | Rendered text | Not stable |

## Schemas

Each artefact above that is a contract, and each JSON answer a program reads
from the binary, has a JSON Schema checked in beside this file. They are
generated from the Go types the JSON is written from, and a test regenerates
them and fails when a type changes without its schema, so they describe what
the current build writes. `contract --json` prints the same schemas together
with the closed sets (rule forms, vias, kinds, statuses and the rest) the
engine validates with.

| Schema | Describes |
|---|---|
| `facts-record.schema.json` | One line of `facts.jsonl` |
| `receipt.schema.json` | `receipt.json` |
| `insights.schema.json` | `insights.json` |
| `ledger.schema.json` | `constraints ledger --json` |
| `check.schema.json` | `check --json` |
| `lint.schema.json` | `constraints lint --json` |
| `contract.schema.json` | `contract --json` |

The MCP server's `constraints_ledger`, `constraints_lint` and `grade` tools
list the ledger, lint and check schemas as their output schemas and return
those documents as structured content, since the commands call the tools for
their answers. The other tools that answer as typed data ([CLI.md](../CLI.md#answers-a-program-reads)) list output schemas generated the same way
from the types their answers are written from; `tools/list` carries them, and
each also admits the `freshness` field a stale snapshot adds.

The schemas do not close objects with `additionalProperties: false`: a reader
accepts fields it does not know, as the versioning rule below requires. A field
is required when the writer always writes it, and a list or object that may be
empty admits `null` where the writer writes `null` for it.

## Contract scope

The contract covers:

- Documented JSON field names and presence rules.
- Fact-kind and relation-kind values.
- Props explicitly identified as contract props in [facts.md](facts.md).
- Fact identity and the resolution semantics of `target_id` and `fact_id`.

It does not cover undocumented props, JSON object field order, insight prose, or
renderer output. Consumers must accept unknown fields, kinds, relation kinds,
and prop values for a supported format version because additive vocabulary does
not change `format_version`.

The field tables describe artifacts written by the current release. Historical
artifacts with the same `format_version` can omit fields added later. In
particular, consumers that accept historical version-1 artifacts must detect
`id`, `target_id`, and `fact_id` by field presence.

## Versioning

`receipt.json` contains `format_version`. The current value is `1`.

- Additive changes, including new optional fields and vocabulary values, do not
  change `format_version`.
- Renaming or removing a documented field, changing identity semantics, or
  changing the meaning of an existing value requires a new `format_version`.

Reject unsupported versions. Treat `format_version: 0` as unknown.

## Determinism

`facts.jsonl`, `insights.json` and `flows.json` are byte-stable when the source tree,
effective configuration, enola build, and repository labels are unchanged.
`snapshot_id` hashes the fact serialization, enola version, and effective
configuration hash.

Repository labels normally come from Git remote repository names and otherwise
fall back to checkout directory names. Different fallback labels produce
different fact IDs and snapshot IDs for the same source tree.

`receipt.json` is not byte-stable. It contains run-specific and machine-specific
values such as `generated_at`, `duration`, and `repo_path`.
