#!/usr/bin/env bash
# check-vendor.sh: vendored files must be exactly what they claim to be.
#
# Every directory named "vendor" below testdata/ or schema/ holds a SOURCE file
# naming one file's upstream repository, commit, path and sha256:
#
#   repo:    https://github.com/Hedronite/NAME     (or  repository: Hedronite/NAME)
#   commit:  40 hex digits
#   path:    path of the file in that repository
#   sha256:  64 hex digits
#   file:    the local file name (optional; default: the last part of path)
#
# For each one this checks
#   1. the local file has the recorded sha256, and
#   2. the file at that commit on raw.githubusercontent.com is byte-identical,
#      so editing a file and its recorded digest together is still caught.
# A file in a vendor directory that no SOURCE names is a failure too.
#
# Usage: bash scripts/check-vendor.sh [--offline]
#   --offline  skip check 2, for a machine with no network. Refused in CI.
# Exit: 0 everything matches, 1 a mismatch, 2 the check could not be made.
set -euo pipefail

raw_base=https://raw.githubusercontent.com
offline=0
for arg in "$@"; do
  case $arg in
    --offline) offline=1 ;;
    *)
      echo "usage: bash scripts/check-vendor.sh [--offline]" >&2
      exit 2
      ;;
  esac
done

die() {
  echo "check-vendor: $*" >&2
  exit 2
}

if [ "$offline" -eq 1 ]; then
  if [ -n "${CI:-}" ] || [ -n "${GITHUB_ACTIONS:-}" ]; then
    die "--offline is not allowed in CI: the upstream comparison must run"
  fi
  echo "check-vendor: WARNING: --offline: NOT comparing with the upstream commit" >&2
fi

cd "$(dirname "$0")/.." || die "cannot find the repository root"

if command -v sha256sum >/dev/null 2>&1; then
  digest() { sha256sum <"$1" | cut -d' ' -f1; }
elif command -v shasum >/dev/null 2>&1; then
  digest() { shasum -a 256 <"$1" | cut -d' ' -f1; }
else
  die "neither sha256sum nor shasum is available"
fi

tmp=$(mktemp -d) || die "mktemp failed"
trap 'rm -rf "$tmp"' EXIT

failed=0
fail() {
  echo "check-vendor: FAIL: $*" >&2
  failed=1
}

# field SOURCE KEY: the one value of "KEY: value"; empty when absent, and an
# error when the key is repeated, so no second line can hide behind the first.
field() {
  local found
  found=$(sed -n "s/^$2:[[:space:]]*//p" "$1" | sed 's/[[:space:]]*$//')
  case $found in
    *$'\n'*) die "$1: '$2' appears more than once" ;;
  esac
  printf '%s' "$found"
}

matches() { printf '%s\n' "$1" | LC_ALL=C grep -qE "^($2)\$"; }

check_dir() {
  local dir=$1 src repo commit path want file local_file got entry
  src=$dir/SOURCE
  if [ ! -f "$src" ]; then
    fail "$dir has no SOURCE file"
    return 0
  fi

  repo=$(field "$src" repo)
  [ -n "$repo" ] || repo=$(field "$src" repository)
  repo=${repo#https://github.com/}
  commit=$(field "$src" commit)
  path=$(field "$src" path)
  want=$(field "$src" sha256)
  file=$(field "$src" file)
  [ -n "$file" ] || file=${path##*/}

  # The values become a URL and a file name: hold each to a strict shape. A
  # full commit id is required, because a branch or tag can move.
  matches "$repo" 'Hedronite/[A-Za-z0-9_-][A-Za-z0-9._-]*' || die "$src: repo must be a Hedronite repository"
  matches "$commit" '[0-9a-f]{40}' || die "$src: commit must be 40 hex digits"
  matches "$want" '[0-9a-f]{64}' || die "$src: sha256 must be 64 hex digits"
  matches "$path" '[A-Za-z0-9_][A-Za-z0-9._-]*(/[A-Za-z0-9_][A-Za-z0-9._-]*)*' || die "$src: path is not a plain relative path"
  matches "$file" '[A-Za-z0-9_][A-Za-z0-9._-]*' || die "$src: file is not a plain file name"

  # Nothing may sit in the directory unaccounted for. .DS_Store is the one file
  # a desktop adds by itself.
  while IFS= read -r entry; do
    case $entry in
      "$src" | "$dir/$file" | */.DS_Store) ;;
      *) fail "$entry is not named by $src" ;;
    esac
  done < <(find "$dir" -mindepth 1)

  local_file=$dir/$file
  if [ ! -f "$local_file" ] || [ -L "$local_file" ]; then
    fail "$local_file is missing or is not a regular file"
    return 0
  fi

  got=$(digest "$local_file")
  if [ "$got" != "$want" ]; then
    fail "$local_file has sha256 $got, SOURCE records $want"
  fi

  if [ "$offline" -eq 0 ]; then
    # -q: ignore any curlrc. No credentials, no redirects: the repository is
    # public and the answer must come from this URL.
    if ! curl -q --fail --silent --show-error --proto '=https' --max-redirs 0 \
      --retry 3 --retry-delay 2 --connect-timeout 10 --max-time 60 --max-filesize 5000000 \
      --output "$tmp/upstream" "$raw_base/$repo/$commit/$path"; then
      die "$local_file: cannot fetch $repo at $commit: $path"
    fi
    if ! cmp -s "$tmp/upstream" "$local_file"; then
      fail "$local_file differs from $repo at $commit: $path"
    fi
    rm -f "$tmp/upstream"
  fi
  if [ "$offline" -eq 0 ]; then
    echo "check-vendor: checked $local_file against its sha256 and $repo@${commit:0:12}"
  else
    echo "check-vendor: checked $local_file against its sha256 only"
  fi
}

checked=0
for top in testdata schema; do
  [ -d "$top" ] || continue
  while IFS= read -r dir; do
    check_dir "$dir"
    checked=$((checked + 1))
  done < <(find "$top" -type d -name vendor | LC_ALL=C sort)
done

# Finding nothing means the check is looking in the wrong place, not that all is well.
[ "$checked" -gt 0 ] || die "no vendor directory found under testdata/ or schema/"

if [ "$failed" -ne 0 ]; then
  echo "check-vendor: FAILED" >&2
  exit 1
fi
if [ "$offline" -eq 1 ]; then
  echo "check-vendor: ok (offline: upstream NOT compared)"
else
  echo "check-vendor: ok"
fi
