# Feeding Vigil from Scry

Scry gives [Vigil](https://github.com/Hedronite/vigil) one fact per cluster: is it ready, and how many nodes and readiness checks pass. The transport is a file and a timer. Vigil never runs Scry, never holds a kubeconfig and never calls a cluster; a timer runs `scry status --out PATH`, and Vigil reads `PATH`.

**One cluster per feed.** Vigil's `vigil.summary/v0` carries one `scry{}` object and one `scry` source, so a Scry file feeds Vigil one cluster today. Scry writes one file per cluster (`status` resolves exactly one cluster entry, and `--out` takes one path). More clusters wait for an additive per-cluster field on the Vigil side; nothing changes in Scry when that lands.

## Run it

```text
scry status --json --config /etc/scry/scry.toml --out /var/lib/scry/status.json
```

Timer examples with placeholder paths, every 60 seconds, well inside Vigil's default `stale_after` of 120 seconds:

- [`examples/scry-status.service`](examples/scry-status.service) and [`examples/scry-status.timer`](examples/scry-status.timer) for systemd. Exit 1 (YELLOW) and 2 (RED) are verdicts, so the service lists them in `SuccessExitStatus`; exits 3 to 8 fail the unit.
- [`examples/com.example.scry-status.plist`](examples/com.example.scry-status.plist) for launchd.

Point the file source for Scry in Vigil's configuration at the same path. Vigil's reader is the other half; it lives in the Vigil repository.

## The file

`--out PATH` belongs to `status` only; `wait --out` is a usage error (exit 8).

| Rule | Behaviour |
| --- | --- |
| Content | Always the full `scry.status/v0` JSON document, byte for byte what `--json` prints, whatever stdout shows (table, `--json` or `--quiet`). |
| Write path | Temp file in the same directory as `PATH`, write, `fsync`, rename over `PATH`, `fsync` the directory. Mode `0644` whatever the umask. The rename is the only change `PATH` ever sees, so a reader sees the previous complete file or the new complete file, never a partial or empty one. |
| When | Exits 0, 1, 2 (observed) and 4, 5, 6 (unobserved: auth, TLS, transport). |
| When not | Exits 3, 7 and 8 write nothing and leave the last file in place, so Vigil ages it into `stale`. |
| Failure | If the write fails, the temp file is removed, the old file stays byte for byte, and Scry exits 3 with reason `out_write_failed`. Stdout then shows an unobserved document carrying that reason, and stderr one line: `scry: cannot write the --out file` (silent with `--quiet`). The path and the operating-system error are never printed. If only the final directory fsync fails after the rename succeeded, the new file is in place: Scry prints one warning line on stderr (`scry: warning: the --out file was written but the directory sync failed`, silent with `--quiet`) and the exit code follows the status result, not 3. |
| Refused | `--out ""`, `--out -`, and a path that is the kubeconfig or the `--config` file (an atomic rewrite would destroy it) are usage errors (exit 8). |
| Links | A symbolic link at `PATH` is replaced, not followed. |

The directory must already exist and be writable by the job's user; Scry does not create it. A process killed mid-write can leave a `.scry-out-*.tmp` file beside `PATH`; Vigil ignores it.

`generated_at` is the freshness Vigil reads. A document whose `observed` is `false` is still a fresh file; Vigil shows the source as `unavailable` and it never caps `overall`.

## Mapping `scry.status/v0` to Vigil `scry{}`

Vigil's `scry{}` is frozen at `Hedronite/vigil` `44dfb0b` (`vigil.summary/v0`, `$defs/scry`): `additionalProperties: false`, counts and reasons only, no node names, no server details, no status. Scry is not an authority and never changes `overall`.

| Vigil `scry{}` | From the Scry document |
| --- | --- |
| `observed` | `observed` |
| `generated_at` | `generated_at` (RFC 3339 UTC, accepted by Vigil's timestamp pattern) |
| `cluster_alias` | `cluster.alias` (Scry's alias pattern and Vigil's slug pattern are the same) |
| `severity` | `severity` when observed; `null` when `observed` is `false` (the Scry document has no `severity` then) |
| `reasons` | `reasons`, copied as they are, in order (all Scry reason codes fit Vigil's `^[a-z][a-z0-9_]{0,47}$`) |
| `counts.nodes_total` | `nodes.total` (0 when `nodes` is absent) |
| `counts.nodes_ready` | `nodes.ready` (a `Ready=Unknown` node is not ready and is not counted) |
| `counts.readyz_pass` | 1 when `readyz.ok` is true, else 0 |
| `counts.readyz_fail` | 1 when `readyz.ok` is false, else 0 |

Two cases to know:

- **Unobserved** (`observed: false`, exits 4, 5, 6): `observed: false`, `generated_at`, `cluster_alias`, `severity: null` and `reasons` (the failure reason, for example `auth_rejected`), and no `counts`. Vigil's own `scry-unobserved` golden is the minimal form, `{"observed": false}`, and is also valid; a reader may emit either. This feed carries the extra fields so Vigil can age the file and show why.
- **No `/readyz` answer** (`apiserver_down`, `expected_dark`): the document has no `readyz` block, so `readyz_pass` and `readyz_fail` are both 0 and `nodes_*` are 0. The counts describe what was seen, not the verdict; `severity` and `reasons` carry the verdict. A cluster declared `--expected dark` that is unreachable is GREEN with reason `expected_dark`.

Not exported: node names and reasons, server version and flavor, API latency, readyz check names and messages, `apiserver.result` and `livez`, `cluster.expected`, and any `wait` block (`status` never writes one). That is the Vigil contract, and it keeps the file free of addresses and names.

Vigil treats an unknown reason as plain text, so a reason Scry adds later (additive within `v0`) needs no Vigil change.

### Example

A YELLOW cluster with one node not ready (`testdata/status/yellow-unknown.json`) becomes:

```json
{
  "observed": true,
  "generated_at": "2026-11-13T20:00:00Z",
  "cluster_alias": "lab-one",
  "severity": "YELLOW",
  "reasons": ["node_not_ready"],
  "counts": { "nodes_total": 2, "nodes_ready": 1, "readyz_pass": 1, "readyz_fail": 0 }
}
```

## Golden pairs

`testdata/vigil/<case>.scry.json` is the expected `scry{}` for `testdata/status/<case>.json`, for every status fixture that can reach a file: `green`, `yellow`, `yellow-unknown`, `red`, `red-all-unknown`, `expected-dark`, `unobserved-auth` and `unobserved-tls`.

`go test ./internal/vigil` checks that:

- every expected block validates against the vendored Vigil `$defs/scry` (`testdata/vigil/vendor/vigil.summary.v0.schema.json`, unmodified from Vigil `44dfb0bb8d5c0598a44175cbe4478953f5c95d91`; `SOURCE` beside it records the commit and a SHA-256, and a test fails if the file changes);
- a reference implementation of the mapping above, written independently of Scry's types, produces exactly each golden block from its input;
- every exportable status fixture has a golden and no golden is orphaned;
- every Scry reason code fits Vigil's reason pattern, Scry's alias rule agrees with Vigil's slug on a probe set, and Scry's timestamps fit Vigil's pattern;
- the vendored schema still rejects node names, server detail, a status field, a bad severity, a bad alias, a bad reason and extra counts, so the check above proves something.

To follow a Vigil schema change, re-vendor the file from the new commit, update `SOURCE`, and let those tests say what moved.
