#!/usr/bin/env bash
# Copy the catalogue recipes from an enola-guides checkout into the embedded
# tree this binary ships. Usage: scripts/munola-recipes-sync.sh <guides-checkout>
set -euo pipefail
guides="${1:?path to an enola-guides checkout}"
dest="$(cd "$(dirname "$0")/.." && pwd)/internal/intent/recipes/munola"
for name in api-boundaries background-work data-ownership ember-conventions; do
  cp "$guides/recipes/$name.yaml" "$dest/$name.yaml"
done
ls "$dest"
