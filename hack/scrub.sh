#!/usr/bin/env bash
# scrub.sh — secret-and-mesh tripwire for this repository.
#
# Scans every tracked file in the work tree, and every blob reachable from a
# ref, for generic credential shapes: cloud access keys, forge and app tokens,
# live payment keys, private-key blocks, kubeconfig key material, long bearer
# tokens, and tailnet addresses. The CI job checks out full history for this.
#
# Matched content is never printed — only class, path and line — so the job log
# cannot become the leak it is looking for.
#
# Scope:
#   - Tracked files only; untracked scratch files are not scanned.
#   - Path-allowlisted: this script (it holds the pattern literals), testdata/,
#     docs/ and *_test.go (synthetic fixtures); or a `scrub:allow` marker on
#     the offending line.
#   - A shallow clone scans the work tree only, and says so.
#   - History scan is per commit; acceptable for this repo's size, revisit if
#     the log grows into the thousands.
#
# Usage: bash hack/scrub.sh [--no-self-test]
# Exit:  0 clean, 1 findings, 2 environment or detector defect.
set -u

usage() {
  echo "usage: bash hack/scrub.sh [--no-self-test]" >&2
  exit 2
}

self_test=1
for arg in "$@"; do
  case $arg in
    --no-self-test) self_test=0 ;;
    -h|--help) usage ;;
    *) echo "scrub: unknown argument: $arg" >&2; usage ;;
  esac
done

root=$(git rev-parse --show-toplevel 2>/dev/null) || {
  echo "scrub: not inside a git work tree" >&2
  exit 2
}
cd "$root" || exit 2

# Generic classes only: name|extended-regex. SAMPLES is index-parallel and
# exists so the detector proves itself; add both entries together.
CLASSES=(
  'aws-access-key|(AKIA|ASIA)[0-9A-Z]{16}'
  'github-token|gh[pousr]_[A-Za-z0-9]{36}'
  'github-fine-grained-pat|github_pat_[A-Za-z0-9_]{22,}'
  'slack-token|xox[baprs]-[A-Za-z0-9-]{10,}'
  'payment-live-key|(sk|rk)_live_[A-Za-z0-9]{20,}'
  'google-api-key|AIza[0-9A-Za-z_-]{35}'
  'private-key-block|-----[[:space:]]*BEGIN (RSA |EC |DSA |OPENSSH |PGP )?PRIVATE KEY-----'
  'kubeconfig-key-data|client-key-data:[[:space:]]*[A-Za-z0-9+/=]{64,}'
  'bearer-token|[Aa]uthorization:[[:space:]]*[Bb]earer[[:space:]]+[A-Za-z0-9._~+/=-]{24,}'
  'tailnet-address|100\.(6[4-9]|[7-9][0-9]|1[01][0-9]|12[0-7])\.[0-9]{1,3}\.[0-9]{1,3}'
  'tailnet-dns|([[:alnum:]-]+\.)+ts\.net'
)
SAMPLES=(
  'AKIAIOSFODNN7EXAMPLE'
  'ghp_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA'
  'github_pat_AAAAAAAAAAAAAAAAAAAAAAAAAA'
  'xoxb-1234567890-ABCDEFGHIJ'
  'sk_''live_AAAAAAAAAAAAAAAAAAAAAAAA'
  'AIzaAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA'
  '-----BEGIN RSA PRIVATE KEY-----'
  'client-key-data: AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA'
  'Authorization: Bearer AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA'
  '100.101.102.103'
  'node.example.ts.net'
)

pattern=
for class in "${CLASSES[@]}"; do
  re=${class#*|}
  pattern=${pattern:+$pattern|}${re}
done

excludes=(
  ':(exclude,glob)hack/scrub.sh'
  ':(exclude,glob)testdata/**'
  ':(exclude,glob)docs/**'
  ':(exclude,glob)**/*_test.go'
)

max_shown=200
seen=
findings=0
shown=0

classify() {
  local line=$1 class name re
  for class in "${CLASSES[@]}"; do
    name=${class%%|*}
    re=${class#*|}
    if [[ $line =~ $re ]]; then
      printf '%s' "$name"
      return 0
    fi
  done
  printf 'credential-pattern'
}

# note KEY LABEL CLASS — dedupe on KEY, print at most max_shown.
note() {
  local key=$1 label=$2 class=$3
  case " $seen " in
    *" $key "*) return 0 ;;
  esac
  seen="$seen $key"
  findings=$((findings + 1))
  if [ "$shown" -lt "$max_shown" ]; then
    printf '  %s  (%s)\n' "$label" "$class" >&2
    shown=$((shown + 1))
  fi
}

scan_worktree() {
  local out line rest file line_no
  out=$(git grep -n -I -E -e "$pattern" -- . "${excludes[@]}" || true)
  [ -z "$out" ] && return 0
  while IFS= read -r line; do
    case $line in
      *scrub:allow*) continue ;;
    esac
    file=${line%%:*}
    rest=${line#*:}
    line_no=${rest%%:*}
    note "$file:$line_no" "$file:$line_no" "$(classify "$line")"
  done <<< "$out"
}

scan_history() {
  local rev out line rest sha file line_no class
  if [ "$(git rev-parse --is-shallow-repository 2>/dev/null)" = "true" ]; then
    echo "scrub: note: shallow clone — history not scanned" >&2
    return 0
  fi
  for rev in $(git rev-list --all); do
    out=$(git grep -n -I -E -e "$pattern" "$rev" -- . "${excludes[@]}" || true)
    [ -z "$out" ] && continue
    while IFS= read -r line; do
      case $line in
        *scrub:allow*) continue ;;
      esac
      sha=${line%%:*}
      rest=${line#*:}
      file=${rest%%:*}
      rest=${rest#*:}
      line_no=${rest%%:*}
      class=$(classify "$line")
      note "$file:$line_no" "history ${sha%"${sha#???????}"}:$file:$line_no" "$class"
    done <<< "$out"
  done
}

# self_test builds throwaway repositories and asserts the detector's edges:
# allowlists do not fire, every class fires, history fires after the work tree
# is clean, and no matched content is echoed.
self_test() {
  local script tmp out rc class name bad=0
  script=$(cd "$(dirname "$0")" && pwd)/$(basename "$0")
  tmp=$(mktemp -d 2>/dev/null) || {
    echo "scrub: self-test: mktemp failed" >&2
    return 2
  }
  mkdir -p "$tmp/repo/hack" "$tmp/repo/testdata" "$tmp/repo/docs" "$tmp/repo/internal/probe" || return 2
  cp "$script" "$tmp/repo/hack/scrub.sh" || return 2

  (
    cd "$tmp/repo" || exit 2
    gc() { git -c user.name=scrub -c user.email=scrub@example.invalid "$@"; }

    printf 'ordinary source\n' > README.md
    printf '%s\n' "${SAMPLES[0]}" > testdata/fixture.txt
    printf '%s\n' "${SAMPLES[0]}" > docs/example.md
    printf 'credential := "%s"\n' "${SAMPLES[0]}" > internal/probe/probe_test.go
    printf '%s  # scrub:allow\n' "${SAMPLES[0]}" > marked.txt
    git init -q . &&
      git add -A &&
      gc commit -qm 'base with allowlisted shapes' || exit 2

    # 1. Allowlists hold: clean exit, nothing reported.
    out=$(bash hack/scrub.sh --no-self-test 2>&1) && rc=0 || rc=$?
    if [ "$rc" -ne 0 ]; then
      echo "scrub: self-test: allowlisted shapes fired (exit $rc)" >&2
      printf '%s\n' "$out" | sed 's/^/    /' >&2
      exit 1
    fi

    # 2. Every class fires, and no matched content is echoed.
    : > leak.txt
    for class in "${SAMPLES[@]}"; do printf '%s\n' "$class" >> leak.txt; done
    git add leak.txt && gc commit -qm 'planted samples' || exit 2
    out=$(bash hack/scrub.sh --no-self-test 2>&1) && rc=0 || rc=$?
    if [ "$rc" -ne 1 ]; then
      echo "scrub: self-test: planted samples did not fail (exit $rc)" >&2
      printf '%s\n' "$out" | sed 's/^/    /' >&2
      exit 1
    fi
    for class in "${CLASSES[@]}"; do
      name=${class%%|*}
      case $out in
        *"($name)"*) ;;
        *)
          echo "scrub: self-test: class '$name' did not fire" >&2
          printf '%s\n' "$out" | sed 's/^/    /' >&2
          exit 1
          ;;
      esac
    done
    for class in "${SAMPLES[@]}"; do
      case $out in
        *"$class"*)
          echo "scrub: self-test: matched content was echoed to output" >&2
          exit 1
          ;;
      esac
    done

    # 3. History still fires once the work tree is clean.
    git rm -q leak.txt && gc commit -qm 'drop planted samples' || exit 2
    out=$(bash hack/scrub.sh --no-self-test 2>&1) && rc=0 || rc=$?
    if [ "$rc" -ne 1 ]; then
      echo "scrub: self-test: history-only leak did not fail (exit $rc)" >&2
      printf '%s\n' "$out" | sed 's/^/    /' >&2
      exit 1
    fi
    case $out in
      *'history '*) ;;
      *)
        echo "scrub: self-test: history line missing from report" >&2
        printf '%s\n' "$out" | sed 's/^/    /' >&2
        exit 1
        ;;
    esac
  ) || bad=1

  rm -rf "$tmp"
  [ "$bad" -eq 0 ] || return 1
  echo "scrub: self-test ok" >&2
  return 0
}

if [ "$self_test" -eq 1 ]; then
  self_test || {
    echo "scrub: detector self-test failed; refusing to report clean" >&2
    exit 2
  }
fi

scan_worktree
scan_history

if [ "$findings" -eq 0 ]; then
  echo "scrub: clean" >&2
  exit 0
fi
if [ "$findings" -gt "$shown" ]; then
  echo "scrub: $findings finding(s); first $shown shown" >&2
else
  echo "scrub: $findings finding(s)" >&2
fi
echo "scrub: FAIL — credential-shaped content found (content is not printed)" >&2
exit 1
