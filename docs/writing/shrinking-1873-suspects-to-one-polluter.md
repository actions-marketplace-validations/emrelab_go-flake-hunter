---
layout: default
title: "Shrinking a flaky Go test from 1873 suspects to one polluter"
description: How an order-dependent test in a 1736-test package was reduced to a single polluter and a permanent regression test.
---

# Shrinking a flaky Go test from 1873 suspects to one polluter

*A real case: `Jamf-Concepts/jamf-cli`, November 2026. Fix merged in
[#474](https://github.com/Jamf-Concepts/jamf-cli/pull/474), approved by a
maintainer.*

## The job nobody volunteers for

On 2026-09-07 a maintainer wrote down a problem in
[jamf-cli#370](https://github.com/Jamf-Concepts/jamf-cli/issues/370):

> `go test ./internal/commands/ -shuffle=N` fails on seeds 2, 7 and 10 […] CI
> does not shuffle, so this is invisible there.

Two months later it was still open. Not because nobody cared, but because the
package has **1736 tests** and one full run costs 60–200 seconds. Finding which
test leaves global state behind means trying candidate orderings, and each
attempt burns a full run. By hand that is a few dozen runs — an afternoon of
staring at a terminal for a bug that CI will never report.

That is the gap [go-flake-hunter](https://github.com/emrelab/go-flake-hunter)
closes. Not detection — `-count=100`, `-shuffle=on` and hosted dashboards
already tell you a test is flaky. The gap is **reproduction**: turning "somewhere
in these 1872 tests" into "this test, deterministically", plus a test file that
outlives the fix.

## What the log actually gives you

A failing `-shuffle=10` run produces a `go test -json` stream. Reading it:

```
go test ./internal/commands/ -count=1 -shuffle=10   # FAIL, ~90s
```

The victim is
`TestCheckAPIMatchRefusesAPlatformCommandOnAnInstanceProfile`, and it failed at
index 1873 of 3146 test events. Solitary, it passes. So the failure needs a
predecessor — and any of 1872 tests could be it.

Notice what the log does *not* give you. It reports one victim and no cause. The
seed tells you the order was reproducible (a shuffled run is deterministic in
order), which is the one fact the whole approach rests on.

## Shrinking: 1873 → 1

Feed the failing log to `repro --from-json`:

```sh
go test ./internal/commands/ -count=1 -shuffle=10 -json > fail.json
go-flake-hunter repro --from-json fail.json
```

It takes the prefix that ran before the victim and delta-debugs it. Each probe
is one `go test` invocation over a shrinking subset: does the victim still fail
when half of these predecessors are gone? Keep the half that preserves the
failure, drop the other, repeat. `--budget 400` caps the number of probes, so a
search that cannot finish says so (status `partial`, exit 4) instead of running
all night.

The result on this package:

```
flake found: TestCheckAPIMatchRefusesAPlatformCommandOnAnInstanceProfile
  package      : github.com/Jamf-Concepts/jamf-cli/internal/commands
  class        : order
  polluter     : TestResolveAuth_URLNormalization
  run          : 3/3 failed
  Wilson 95%   : 43.9%–100.0%
  deterministic: yes
```

One polluter out of 1872 candidates. Three out of three reproduction attempts.

Two details in that block are deliberate. `run: 3/3` with a Wilson interval means
the tool states how sure it is instead of claiming certainty from a single
observation; a 3/3 result still carries a lower bound of 43.9%, and the tool
prints that rather than hiding it. `class: order` names the failure shape,
because "order" and "repeat" and "parallel" need different witnesses.

## The mechanism, once it is one test

`credentialSource` (`internal/commands/gateway_coverage.go:171`) builds the
sentence the victim asserts on out of four package-level variables: `token`,
`tokenFile`, `clientID`, `clientSecret`.

`resolveAuth` (`internal/commands/root.go:703-734`) **writes** them — it caches
`JAMF_TOKEN`, `JAMF_CLIENT_ID`, `JAMF_CLIENT_SECRET` and token-file contents into
those globals. Every test that calls `resolveAuth` therefore leaves them set for
whatever runs next.

The polluter resolves a profile whose token is `env:TEST_URL_NORM_TOKEN`, which
sets `token`. The victim then expects:

```
profile "pro-instance"
```

and gets:

```
the JAMF_TOKEN environment variable
```

In source order the victim runs before the polluters — `gateway_coverage_test.go`
sorts before `pro_*` and `root_test.go` — so the default suite is green, and CI,
which never shuffles, had no reason to notice.

This is the part worth staring at: **the test was not wrong.** The sentence it
asserts on is a product requirement — an operator with working credentials must
not read a scope refusal as a credential problem. What was wrong is that the test
never established the state its assertion depends on.

## The fix is eleven lines

The repository already had the right helper.
[#410](https://github.com/Jamf-Concepts/jamf-cli/pull/410) added
`isolateAuthGlobals(t)` for exactly this failure mode, after a different pair of
tests leaked credentials into a later one. The fix adds one call to the two tests
that assert on `credentialSource` output:

```go
func TestCheckAPIMatchRefusesAPlatformCommandOnAnInstanceProfile(t *testing.T) {
	isolateAuthGlobals(t)
	...
```

No production change. The global-flag design is the real reason the package is
order-sensitive, and replacing it is a larger refactor than a bug report
deserves.

| Run | before (`977b704`) | with the patch |
|---|---|---|
| `-count=1` | ok | ok |
| `-shuffle=2` | ok | ok |
| `-shuffle=7` | ok | ok |
| `-shuffle=10` | **FAIL** | ok |
| `-shuffle=1234` | **FAIL** | ok |

The witness built from the capsule — polluter → victim, in a build-tagged file —
is RED before the patch and GREEN after it.

## Two measurements that did not fit the story

Kept because they matter more than the tidy version:

- **The issue's seeds are partly stale.** `-shuffle=2` and `-shuffle=7` pass on
  the base commit; the failure reproduces on `10` and `1234`. A two-month-old bug
  report is a hypothesis, not a specification. Re-check it before believing it.
- **An early "seed 7 fails" reading was wrong.** It failed while a second full
  suite ran on the same machine, and did not reproduce alone. A shuffled run is
  deterministic in *order*, not in *wall clock*, and this package has tests
  sensitive to the latter. The A/B is in the evidence folder.

If a tool had reported the first reading as fact, the PR would have carried a
seed number that proves nothing.

## What this did not do

It did not find the bug. A maintainer wrote the diagnosis in September; the
contribution here is the minimal polluter, the deterministic witness and the
patch.

It also did not fix the class. Two other `-shuffle` failures in the same package
were recorded at the base commit and left alone — because each needs its own
polluter before a fix can be trusted:

| Seed | Failing test at `977b704` |
|---|---|
| `-shuffle=99991` | `TestProGroupAction_UniqueGroupNameSendsOneWriteThroughTheRoot` |
| `-shuffle=on` (random) | `TestDryRun_ProUploadIsNeverSent` |

Re-measured at the merged commit `62feabf`: seed `99991` no longer fails in that
same test — it fails in `TestLogflushPolicy_SendsOneDeleteAfterLookup` instead.
The patch isolated two leaking tests and the shuffle found the next one.
That is what "not fixed, only reduced" looks like: the failures move instead of
disappearing, and each move needs its own hunt.

That is the honest shape of this kind of tool: it is very good at turning
"somewhere in these 1872 tests" into "this test, deterministically", and it
cannot tell you why the code was written the way it was.

## The same week, elsewhere

Two other flake fixes landed in `tailscale/tailscale`, both approved by Brad
Fitzpatrick:

- [#22408](https://github.com/tailscale/tailscale/pull/22408) — `TestHTTPBandwidth`
  failed 11 times in two weeks on Windows CI. Every probe measured a transfer
  time of exactly `0`, because a 1 MiB loopback transfer finishes inside one
  monotonic clock tick: the retry loop added in #20214 re-measured zero. The fix
  makes the transfer span more than one tick (half the payload, flush, 50 ms
  sleep, rest) and deletes the retries.
- [#22427](https://github.com/tailscale/tailscale/pull/22427) — `TestTCPRoundRobinHandler`
  hung until the 10-minute package timeout. `memnet.connHalf` did not implement
  `CloseWrite`, so a proxy propagating the backend's close silently did nothing
  and the test waited forever. Before: 50000 runs hung; after: the same 50000
  runs pass in 1.3 s.

Different failure classes — clock granularity, a missing interface method, an
order dependence hidden in package-level state — same method: reproduce
deterministically first, then fix the cause rather than the symptom.

## Try it

```sh
go install github.com/emrelab/go-flake-hunter/cmd/go-flake-hunter@v0.3.0
```

```sh
go-flake-hunter hunt ./...                        # find one
go-flake-hunter compile                           # make it a permanent, build-tagged test
go-flake-hunter explain .go-flake-hunter/gfh_ID.json   # what it found, and what it refused to claim
```

## Evidence

| What | Where |
|---|---|
| This case, verbatim | [`docs/real-world/jamf-cli/`](https://github.com/emrelab/go-flake-hunter/tree/main/docs/real-world/jamf-cli) — capsule, the 38 MB log's repro run, the seed sweep before and after, the A/B that ruled out a load-induced failure |
| Eight upstream flake cases | [`docs/real-world/README.md`](https://github.com/emrelab/go-flake-hunter/blob/main/docs/real-world/README.md) — Kubernetes, istio, Tailscale, jamf-cli, slug, CLIProxyAPI |
| Install, commands, exit codes | [repository README](https://github.com/emrelab/go-flake-hunter#readme) |
| Turning a red CI log into a PR comment | [CI reproduction recipe](https://github.com/emrelab/go-flake-hunter/blob/main/docs/CI-REPRO.md) |
