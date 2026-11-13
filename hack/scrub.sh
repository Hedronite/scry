#!/usr/bin/env bash
# scrub.sh — credential-and-location tripwire for this repository.
#
# Two tiers, because a secret and an address leak in different places:
#
#   Tier A (credentials) scans every tracked file and every commit reachable
#   from a ref, with testdata/, docs/, *_test.go and this script path-allowlisted
#   because those legitimately hold synthetic samples.
#
#   Tier B (locations) scans ALL tracked files and history except this script:
#   fixtures and docs are exactly where a real address, port, home path or
#   private hostname would land. There is no path allowlist in Tier B.
#
#   A `scrub:allow` marker on the line escapes either tier. Matched content is
#   never printed — only class, path and line — so the job log cannot become
#   the leak it is looking for.
#
# Scope notes:
#   - Tracked files only; untracked scratch files are not scanned.
#   - A shallow clone scans the work tree only, and says so.
#   - History scan is per commit; acceptable for this repository's size.
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

# Tier A: name|extended-regex. SAMPLES_CRED is index-parallel and exists so the
# detector proves itself; add both entries together.
CRED_CLASSES=(
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
SAMPLES_CRED=(
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

# Tier B: name|extended-regex. IPv4 and IPv6 are matched broadly, then filtered
# by the allowance sets below (loopback, unspecified, documentation ranges).
LOC_CLASSES=(
  'ipv4-literal|([0-9]{1,3}\.){3}[0-9]{1,3}'
  'ipv6-literal|([0-9A-Fa-f]{0,4}:){3,7}[0-9A-Fa-f]{0,4}|::[0-9A-Fa-f]{1,4}'
  'kube-apiserver-port|:6443([^0-9]|$)'
  'home-path|/Users/|/home/[[:alnum:]_.-]+/'
  'private-hostname|\.(local|lan|internal|home\.arpa|ts\.net)([^[:alnum:].-]|$)'
  'jwt|eyJ[A-Za-z0-9_-]{10,}\.'
)
SAMPLES_LOC=(
  '10.1.2.3'
  'fd00::12'
  'fd00:0:0:0:0:0:0:1'
  ':6443'
  '/Users/example/projects/cluster'
  'api.internal'
  'eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJ4In0'
)

join_patterns() { # name|regex... -> alternation
  local class pattern=
  for class in "$@"; do
    pattern=${pattern:+$pattern|}${class#*|}
  done
  printf '%s' "$pattern"
}

cred_pattern=$(join_patterns "${CRED_CLASSES[@]}")
loc_pattern=$(join_patterns "${LOC_CLASSES[@]}")

script_path=':(exclude,glob)hack/scrub.sh'
cred_excludes=(
  "$script_path"
  ':(exclude,glob)testdata/**'
  ':(exclude,glob)docs/**'
  ':(exclude,glob)**/*_test.go'
)
loc_excludes=("$script_path")

max_shown=200
seen=
findings=0
shown=0

# note KEY LABEL CLASS — dedupe on KEY (one row per location), print at most
# max_shown rows.
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

classify_cred() { # content -> class, empty if none
  local content=$1 class name re
  for class in "${CRED_CLASSES[@]}"; do
    name=${class%%|*}
    re=${class#*|}
    if [[ $content =~ $re ]]; then
      printf '%s' "$name"
      return 0
    fi
  done
  return 0
}

ipv4_disallowed() { # content -> 0 when a non-allowlisted IPv4 is present
  local ip a b c d
  while IFS= read -r ip; do
    [ -z "$ip" ] && continue
    case $ip in
      127.0.0.1|0.0.0.0) continue ;;
    esac
    IFS=. read -r a b c d <<< "$ip"
    if [ "$a" = 192 ] && [ "$b" = 0 ] && [ "$c" = 2 ]; then continue; fi
    if [ "$a" = 198 ] && [ "$b" = 51 ] && [ "$c" = 100 ]; then continue; fi
    if [ "$a" = 203 ] && [ "$b" = 0 ] && [ "$c" = 113 ]; then continue; fi
    return 0
  done < <(printf '%s\n' "$1" | grep -oE '([0-9]{1,3}\.){3}[0-9]{1,3}')
  return 1
}

ipv6_disallowed() { # content -> 0 when a non-allowlisted IPv6 literal is present
  local ip low
  while IFS= read -r ip; do
    [ -z "$ip" ] && continue
    case $ip in
      *[0-9A-Fa-f]*) ;;
      *) continue ;; # bare '::' carries no address; it is allowed and unreported
    esac
    low=$(printf '%s' "$ip" | tr 'A-F' 'a-f')
    case $low in
      '::1'|'::') continue ;;
      2001:db8:*|2001:0db8:*) continue ;;
    esac
    return 0
  done < <(printf '%s\n' "$1" | grep -oE '([0-9A-Fa-f]{0,4}:){3,7}[0-9A-Fa-f]{0,4}|::[0-9A-Fa-f]{1,4}')
  return 1
}

classify_loc() { # content -> class, empty if none
  local content=$1 class name re
  for class in "${LOC_CLASSES[@]}"; do
    name=${class%%|*}
    re=${class#*|}
    case $name in
      ipv4-literal)
        ipv4_disallowed "$content" && { printf '%s' "$name"; return 0; }
        ;;
      ipv6-literal)
        ipv6_disallowed "$content" && { printf '%s' "$name"; return 0; }
        ;;
      *)
        if [[ $content =~ $re ]]; then
          printf '%s' "$name"
          return 0
        fi
        ;;
    esac
  done
  return 0
}

scan_worktree() {
  local out line file line_no content class
  out=$(git grep -n -I -E -e "$cred_pattern" -- . "${cred_excludes[@]}" || true)
  while IFS= read -r line; do
    case $line in
      *scrub:allow*) continue ;;
    esac
    IFS=: read -r file line_no content <<< "$line"
    class=$(classify_cred "$content")
    [ -n "$class" ] && note "$file:$line_no" "$file:$line_no" "$class"
  done <<< "$out"

  out=$(git grep -n -I -E -e "$loc_pattern" -- . "${loc_excludes[@]}" || true)
  while IFS= read -r line; do
    case $line in
      *scrub:allow*) continue ;;
    esac
    IFS=: read -r file line_no content <<< "$line"
    class=$(classify_loc "$content")
    [ -n "$class" ] && note "$file:$line_no" "$file:$line_no" "$class"
  done <<< "$out"
}

scan_history() {
  local revs rev out line sha file line_no content class
  if [ "$(git rev-parse --is-shallow-repository 2>/dev/null)" = "true" ]; then
    echo "scrub: note: shallow clone — history not scanned" >&2
    return 0
  fi
  revs=$(git rev-list --all)
  # A detached HEAD (the shape of a PR checkout) is not under refs/.
  case " $revs " in
    *" $(git rev-parse --verify HEAD 2>/dev/null) "*) ;;
    *) revs="$revs $(git rev-parse --verify HEAD 2>/dev/null)" ;;
  esac
  for rev in $revs; do
    out=$(git grep -n -I -E -e "$cred_pattern" "$rev" -- . "${cred_excludes[@]}" || true)
    while IFS= read -r line; do
      case $line in
        *scrub:allow*) continue ;;
      esac
      IFS=: read -r sha file line_no content <<< "$line"
      class=$(classify_cred "$content")
      [ -n "$class" ] && note "$file:$line_no" "history ${sha:0:7}:$file:$line_no" "$class"
    done <<< "$out"

    out=$(git grep -n -I -E -e "$loc_pattern" "$rev" -- . "${loc_excludes[@]}" || true)
    while IFS= read -r line; do
      case $line in
        *scrub:allow*) continue ;;
      esac
      IFS=: read -r sha file line_no content <<< "$line"
      class=$(classify_loc "$content")
      [ -n "$class" ] && note "$file:$line_no" "history ${sha:0:7}:$file:$line_no" "$class"
    done <<< "$out"
  done
}

# self_test builds throwaway repositories and asserts the detector's edges:
# allowlists do not fire, every class fires, documentation ranges and marked
# lines do not fire, Tier B reaches fixtures and tests, a history-only leak
# still fails after the work tree is clean, and no sample is echoed.
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
    run() { bash hack/scrub.sh --no-self-test 2>&1; }

    printf 'ordinary source\n' > README.md
    printf '%s\n' "${SAMPLES_CRED[0]}" > testdata/fixture.txt
    printf '%s\n' "${SAMPLES_CRED[0]}" > docs/example.md
    printf 'credential := "%s"\n' "${SAMPLES_CRED[0]}" > internal/probe/probe_test.go
    printf '%s  # scrub:allow\n' "${SAMPLES_CRED[0]}" > marked.txt
    printf '%s  # scrub:allow\n' "${SAMPLES_LOC[2]}" > marked-location.txt
    printf '192.0.2.10 198.51.100.20 203.0.113.30 127.0.0.1 0.0.0.0\n' > testdata/doc-ranges.txt
    printf '::1\n::\n2001:db8::1\n2001:db8:ffff:1::9\n' > testdata/doc-ranges6.txt
    printf '2006-01-02T15:04:05Z\n2026-11-13T15:00:00Z\n15:04:05\n' > testdata/timestamps.txt
    git init -q . &&
      git add -A &&
      gc commit -qm 'base with allowlisted shapes' || exit 2

    # 1. Allowlists and documentation ranges hold: clean exit, nothing reported.
    out=$(run) && rc=0 || rc=$?
    if [ "$rc" -ne 0 ]; then
      echo "scrub: self-test: allowlisted shapes fired (exit $rc)" >&2
      printf '%s\n' "$out" | sed 's/^/    /' >&2
      exit 1
    fi

    # 2. Every class fires, including Tier B inside testdata/ and a _test.go.
    : > leak-cred.txt
    for class in "${SAMPLES_CRED[@]}"; do printf '%s\n' "$class" >> leak-cred.txt; done
    : > testdata/location.txt
    for class in "${SAMPLES_LOC[@]}"; do printf '%s\n' "$class" >> testdata/location.txt; done
    : > internal/probe/location_test.go
    for class in "${SAMPLES_LOC[@]}"; do printf '// %s\n' "$class" >> internal/probe/location_test.go; done
    git add -A && gc commit -qm 'planted samples' || exit 2
    out=$(run) && rc=0 || rc=$?
    if [ "$rc" -ne 1 ]; then
      echo "scrub: self-test: planted samples did not fail (exit $rc)" >&2
      printf '%s\n' "$out" | sed 's/^/    /' >&2
      exit 1
    fi
    for class in "${CRED_CLASSES[@]}" "${LOC_CLASSES[@]}"; do
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
    case $out in
      *'testdata/location.txt:'*) ;;
      *)
        echo "scrub: self-test: Tier B did not reach testdata/" >&2
        exit 1
        ;;
    esac
    case $out in
      *'location_test.go:'*) ;;
      *)
        echo "scrub: self-test: Tier B did not reach a _test.go file" >&2
        exit 1
        ;;
    esac
    case $out in
      *'testdata/doc-ranges'*)
        echo "scrub: self-test: a documented range fired" >&2
        printf '%s\n' "$out" | sed 's/^/    /' >&2
        exit 1
        ;;
      *'testdata/timestamps'*)
        echo "scrub: self-test: a clock time fired as an address" >&2
        printf '%s\n' "$out" | sed 's/^/    /' >&2
        exit 1
        ;;
    esac
    for class in "${SAMPLES_CRED[@]}" "${SAMPLES_LOC[@]}"; do
      case $out in
        *"$class"*)
          echo "scrub: self-test: matched content was echoed to output" >&2
          exit 1
          ;;
      esac
    done

    # 3. History still fires once the work tree is clean.
    git rm -q leak-cred.txt testdata/location.txt internal/probe/location_test.go &&
      gc commit -qm 'drop planted samples' || exit 2
    out=$(run) && rc=0 || rc=$?
    if [ "$rc" -ne 1 ]; then
      echo "scrub: self-test: history-only leak did not fail (exit $rc)" >&2
      printf '%s\n' "$out" | sed 's/^/    /' >&2
      exit 1
    fi
    case $out in
      *'history '*':'*) ;;
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
echo "scrub: FAIL — credential-shaped or location content found (content is not printed)" >&2
exit 1
