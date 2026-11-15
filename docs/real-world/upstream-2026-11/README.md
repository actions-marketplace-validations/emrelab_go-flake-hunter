---
layout: default
title: "Pinned upstream batch — November 2026"
---

# Five upstream defects, pinned and re-proved

Seven capsules from two upstream Go repositories — four Kubernetes packages and
one istio package — each found at the base commit of the pull request that fixed
(or proposes to fix) it. Every capsule is content-addressed, `repo.dirty:
false`, and `verifyStatus: verified` — the generated regression was run and was
**red** on the unfixed tree.

Tool: `v0.3.0-39-g142b237`, `go1.26.2`, macOS 27.2 arm64.

**Why these four Kubernetes cases exist upstream at all:** `pull-kubernetes-unit`
runs `go test -count=1`. Every one of them passes at `-count=1` and fails only on
a second run in the same binary, so the default CI order never sees them. That is
the class this tool was built for, and it is the class its fixtures could
previously only demonstrate on invented code — the three earlier real-world cases
(slug, CLIProxyAPI, jamf-cli) are all `order`.

## Method, identical for every case

```sh
git checkout <base commit of the fix PR>     # never the fixed tree
go test <plain command>                       # reproduce the report by hand first
go-flake-hunter repro . <Victim> --budget … --timeout … --capsule-dir … --format json
go-flake-hunter compile <capsule> --pkg . --verify   # must print: RED (verified)
git apply <upstream pull request>.patch
go test -tags goflakehunter -run '^TestFlakeRegression_<Victim>$' .   # must go green
go-flake-hunter replay <capsule> --pkg . -n 3      # must exit 3: never reproduced
```

The upstream patch is applied as published (`gh`'s `.patch` URL), never adapted.
Where a patch touched a non-test file the tool did not need to know: the witness
is built from the test bodies the repository already has.

## The batch

| Case | Upstream | Class | Minimal witness | Before | After the patch |
|---|---|---|---|---|---|
| [k8s client-go/rest](#kubernetes-client-gorest) | [k8s#143133](https://github.com/kubernetes/kubernetes/pull/143133) (open) | repeat (`-count`) | `TestAuthPluginWrapTransport` | package red at `-count=2`, 4 tests | green |
| [k8s apiserver](#kubernetes-apiserver) | [k8s#144048](https://github.com/kubernetes/kubernetes/pull/144048) (open) | repeat | `TestObserveCompilation`, `TestRecordEtcdBookmark` | red at `-count=2`; a third package panics | green |
| [k8s scheduler/queue](#kubernetes-schedulerqueue) | [k8s#143132](https://github.com/kubernetes/kubernetes/pull/143132) (merged) | repeat | `TestPriorityQueue_UpdateNominatedPodForNode` | red at `-count=5` | green |
| [k8s scheduler/metrics](#kubernetes-schedulermetrics) | [k8s#142680](https://github.com/kubernetes/kubernetes/pull/142680) (merged) | order | `TestQueuedEntitiesRecorder` ← `TestInFlightEventAsync` + `TestRecordGeneratedPlacements` | red at `-shuffle=2` | green — but one repeat witness survives it, see below |
| [istio accesslog](#istio-accesslog) | [istio#39619](https://github.com/istio/istio/issues/39619), fix [istio#39624](https://github.com/istio/istio/pull/39624) (merged 2022) | order | `TestListenerAccessLog` ← `TestListenerAccessLogs` | red at `-shuffle=2`, `-shuffle=3` | green |

Each case directory holds the same file set:

| File | What it is |
|---|---|
| `capsules/gfh_*.json` | the capsule |
| `*-repro.json` / `repro.json` | the `repro` run that produced it |
| `*-compile.txt` / `compile.txt` | `compile --verify`, verbatim — the RED verdict |
| `*regression_test.go.txt` | the generated test, as written |
| `baseline-*.txt` | the failing `go test` on the unfixed tree, filtered to the assertions |
| `upstream-fix.patch` | the upstream patch, as published |
| `after-fix.txt` | the generated regression and the package, after the patch |

Path substitution, and it is the only edit: the local checkout path became
`<repo>` (so `relDir` reads `<repo>/pkg/scheduler/metrics`) and the capsule
directory became `<capsule-dir>`. No field value, verdict or byte of output was
otherwise touched — the verdicts are the ones the binary printed, which is why
they are published as captured rather than re-rendered.

## kubernetes client-go/rest

Base `7898e97f1724f72869caa6b354eda92845a6b7f8`. Capsule `gfh_f0726736eb90`.

```sh
go test -count=2 ./rest
# FAIL TestAuthPluginWrapTransport / TestAuthPluginPersist
#      Test_WhenNilPersister_NoOpPersisterIsAssigned / TestThrottledLogger
#   "auth Provider Plugin \"pluginA\" was registered twice"
#   "expected 100 log messages, but got 2"
```

Two independent leaks in one package: auth plugins registered into a
process-global map without cleanup, and `lastLogTime` left at fake-clock
timestamps. One capsule names the first; the same `-count=2` run names all four.
The witness needs no polluter — repeat class, `-count=10` in one binary, 2/2.

## kubernetes apiserver

Base `7898e97f17`. Capsules `gfh_3874aa831864` (cel/metrics) and
`gfh_54a975bd9f97` (etcd3/metrics). One upstream PR, three packages, three
different root causes.

| Package | `-count=2` before | Capsule |
|---|---|---|
| `pkg/cel/metrics` | `TestObserveCompilation`, `TestObserveEvaluation`: `unexpected count: 2` | yes |
| `pkg/storage/etcd3/metrics` | `TestRecordEtcdBookmark`: `etcd_bookmark_total 2` vs `1` | yes |
| `pkg/storage/value` | `TestLogTransformErr`: `panic: flag redefined: add_dir_header` | **no — see below** |

The third package is a documented limit, not an omission. The defect is real and
reproduces by hand (`baseline-value-panic.txt`), but `go test -json` never emits a
terminal event for a test that panics the binary: the tool reports
`missing results for TestLogTransformErr: test run incomplete` and exits 7. A
repeat witness is a claim about a *test outcome*, and a dead process has none.
`after-fix.txt` shows the same package green once the upstream patch is applied.

## kubernetes scheduler/queue

Base `9b3380ed0f0882068abd7fb9bfbc816c595ae79e`. Capsule `gfh_6c64368b7e5a`.

`TestPriorityQueue_UpdateNominatedPodForNode` deep-copies a package-level fixture,
then clears `NodeName` on the **original** instead of the copy. Round 1 passes;
every later round in the same binary sees an already-scheduled pod. Found by the
repeat probe in two runs, no minimization needed, `-count=10`.

## kubernetes scheduler/metrics

Base `3c690c189e5d367715dbd056a5c2009a476c8d77`. Two capsules, and the second is
the interesting one.

**Order** — `gfh_7e7a0c4a10dc`. Shuffled, `TestQueuedEntitiesRecorder` fails
against a registry another test has already written. ddmin over the package
reduced the preceding set to two tests:

```
TestInFlightEventAsync + TestRecordGeneratedPlacements → TestQueuedEntitiesRecorder
```

**Repeat, and a leftover the merged fix does not close** — `gfh_b0ab94e0e871`.
Before the patch, `TestNewMetricsAsyncRecorder` also fails at `-count=2` alone:
it asserts on package-level counters it never resets. The upstream patch
(`metrics.go` + `profile_metrics_test.go`) removes `InitMetrics()` from two other
tests and fixes the shuffle symptom — the order witness goes green, `-shuffle`
seeds 1/2/3 pass — but the repeat witness **stays red after the patch**:

```sh
git apply …/142680.patch
go test -count=2 -run '^TestNewMetricsAsyncRecorder$' ./pkg/scheduler/metrics
# --- FAIL: expected metric to have count 0, got 3
```

`repeat-regression-after-fix` is in `after-fix.txt`. This is the permanence claim
working as intended: the committed regression outlives the patch and names what
the patch left behind. Reported here as an observation about the pinned commit,
not as a claim that upstream still carries it.

## istio accesslog

Base `ac641d08093c14ee53a71b7193a7e6a519db8159` (2022). Capsule `gfh_1adc5e54f33f`.

The upstream issue names `-shuffle=on` as the only trigger; on this hardware
seed 1 passes and seeds 2 and 3 fail, so the report's own reproducer was seed
dependent. The witness is a sibling test whose name differs by one letter:

```
TestListenerAccessLogs → TestListenerAccessLog
```

`TestListenerAccessLog` resets the shared `accessLogBuilder` *after* it builds,
so the first case in the table inherits whatever the previous test left; the fix
moves the reset before the build. 2022 code needs one linker flag on a modern
toolchain — the vendored `golang.org/x/net` reaches `syscall.recvmsg` by
`//go:linkname`, which Go 1.23+ refuses:

```sh
GOFLAGS="-ldflags=-checklinkname=0" go test -shuffle=2 ./pilot/pkg/networking/core/v1alpha3
```

The flag is why the same package fails to build without it; it is not part of the
flake. `repro.json` was produced with it set, and the capsule inherits nothing
from it.

## Blocked: cockroachdb/cockroach #160685

The sixth candidate is not in this batch. A plain checkout of
`4c350c2bbe81c16574fb1beb8dd3171c84ad4afc` does not compile `./pkg/cli`:

```
pkg/server/diagnostics/diagnostics.go:17:2: no required module provides package
  github.com/cockroachdb/cockroach/pkg/server/diagnostics/diagnosticspb
```

Cockroach generates its protobuf packages at build time; they are not in the
tree or in the module zip. Proving it needs `./dev generate go`, which bootstraps
its own toolchain. Recorded as blocked rather than claimed.

## What this batch does not claim

- **No detection rate.** Five repositories chosen because an upstream report
  already existed. Recall and false-positive rate over arbitrary code are not
  measured here and are not measured anywhere in this repository.
- **Four of the five were already reported upstream.** The contribution is the
  minimal witness, the deterministic replay and the permanent regression — never
  the diagnosis. The k8s scheduler/metrics repeat witness is the one finding this
  batch adds to what upstream already knew, and it is stated as a leftover on a
  pinned commit, not as a bug report.
- **The istio case is four years old.** It re-validates the lifecycle on old code
  with a current toolchain; it is not a new discovery.
- **Only the generated regression is proven green after each fix.** No claim is
  made that the entire upstream suite is green, except where `after-fix.txt`
  shows the package itself passing.
