#!/usr/bin/env bash
# Print the CHANGELOG.md section for a tag: the text under "## [X.Y.Z]" up to
# the next "## " heading. Fails if the section is missing or empty, so a
# release can never publish with blank notes.
#
#   hack/release-notes.sh v0.1.0
set -euo pipefail

tag="${1:-}"
[[ "$tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]] || { echo "usage: hack/release-notes.sh <tag>" >&2; exit 1; }
ver="${tag#v}"
cd "$(git rev-parse --show-toplevel)"

notes="$(awk -v ver="$ver" '
  /^## / {
    if (on) exit
    h = $0
    sub(/^## \[?v?/, "", h)
    sub(/[] ].*$/, "", h)
    on = (h == ver)
    next
  }
  on { print }
' CHANGELOG.md)"
# Trim blank lines at both ends.
notes="$(printf '%s\n' "$notes" | sed -e :a -e '/^\n*$/{$d;N;ba' -e '}' | sed '/./,$!d')"
[[ -n "$notes" ]] || { echo "release-notes.sh: no CHANGELOG.md section for ${tag}" >&2; exit 1; }
printf '%s\n' "$notes"
