# Changelog

All notable changes to this project are documented here.
The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/);
this project adheres to [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Changed
- **Go baseline**: module `go` directive moved from 1.24 to 1.26. Building from
  source now needs Go 1.26+; prebuilt binaries and `go install` under the
  default `GOTOOLCHAIN=auto` are unaffected. The floor for *target* repos stays
  at 1.24 — that one lives in `doctor`, not in `go.mod`.
- **Fast runner parity**: the compiled runner now runs the test binary with
  `-test.v=test2json`, so a test that prints `--- FAIL: TestX` stays an output
  event instead of becoming a failure. `GOFLAGS` carrying test-runtime flags
  (`-skip`, `-run`, `-short`, ...) delegates to the `go test` path, because the
  direct binary run cannot honour them.
- **test2json resolution**: fixed the argument order to `go tool -n test2json`.
  The old spelling exits 2 on Go 1.26+, so every run silently used the slow
  `go tool` fallback the performance document claimed was gone.

### Fixed
- **SKIP is not PASS**: the repeat and env probes now require a measured PASS or
  FAIL on both sides of a comparison. A skip-then-fail sequence no longer
  confirms a repeat witness, and a value-dependent env flake that fails in
  isolation is no longer reported as `broken` — the registry keeps a broken
  verdict but lets the remaining hypotheses explain it first.
- **Cross-binary clean start**: every candidate round re-checks that the victim
  passes before the polluter stage, so leftover state cannot confirm an
  innocent polluter.
- **Order seeds**: a permutation whose failure cannot be shrunk no longer ends
  the shuffle sweep; the next seed is tried.
- **`os.Exit` tests**: the compiled path synthesizes the missing terminal events
  from a non-zero exit, so a test that kills the process is a failure, not an
  unfinished run.
- **`TestMain` that exits non-zero for `-list`**: `Discover` keeps the printed
  test list instead of reporting a build failure (a package that tests fine was
  skipped with exit 6).
- **Oversized log lines**: a `--from-json` line beyond the 8 MiB buffer is a
  usage error naming the limit, not an internal error.
- **Replay/compile attribution**: an unverified "victim fails in isolation"
  precondition now rejects witnesses whose victim fails on its own, instead of
  reporting an unrelated breakage as a reproduced flake.
- **`repro --from-json`**: `{}` no longer counts as evidence of a test log, and
  a stream that starts tests without a terminal event is refused as truncated
  instead of reading as "no failing tests".
- **Env replay**: `--env KEY=<value redacted>` is rejected as a usage error
  instead of producing the false "stale capsule" verdict.
- **Report sanitisation**: capsule-supplied text (note, reproduce command,
  evidence) is stripped of terminal control sequences in text and markdown
  output; a shared capsule can no longer script the reader's terminal.
- **Multi-package exit code**: a run-wide budget/cancel failure now decides the
  exit code even when an earlier package failed on its own.
- **TUI replay/preview/compile** pass the selected package with `--pkg`, so a
  capsule listed from another checkout resolves in that checkout.
- **Action**: a report the `tee` step cannot write now fails the step instead
  of publishing an empty report path with exit 0.
- **`make test-repeat`**: raised the package timeout to 45m (the cli
  subprocess-heavy packages exceed Go's 10m default at `-count=10`).
- **Docs**: `Minimize`'s claim is corrected to 1-minimal (not a global
  minimum) in the README, ARCHITECTURE and ddmin comments.

## [0.4.0] — 2026-11-20

### Added
- **CI artifact reliability**: Artifact ID-based download in quality job eliminates intermittent download failures in PR CI (`.github/workflows/ci.yml`)
- **PERFORMANCE.md documentation**: Corrected SpecKey benchmark claims and dense normalization trade-off disclosure
- **CI artifact reliability fix**: Artifact ID-based download using upload step's `artifact-id` output

### Changed
- **Nightly CI exit code**: Budget exhaustion (exit 4) now correctly preserved instead of converted to success
- **Benchmark fixture naming**: `gen_%03d_test.go` → `gen%03d_test.go` to avoid Go architecture suffix conflicts
- **CI artifact download**: Switched from name-based to ID-based download for deterministic artifact retrieval
- **Performance documentation**: Corrected SpecKey benchmark claims (baseline already ~100 ns/op on Go 1.27.4) and dense normalization trade-off (increased allocation count, similar bytes)

### Fixed
- **Nightly CI exit code 4 → 0**: Budget-exhausted self-hunts now correctly report exit 4 instead of silently succeeding
- **Benchmark fixture exclusion**: Files like `gen_386_test.go` no longer excluded on non-386 architectures
- **CI artifact download failures**: Intermittent "Artifact not found" errors eliminated via ID-based download

### Performance
- **AST scanning**: ~40-45% latency reduction and ~45% allocation reduction at 400 files (25.5 ms → 14.8 ms, 17.4 MB → 9.5 MB) through `parser.SkipObjectResolution` and AST reuse
- **Log normalization**: Clean inputs allocate 0 bytes (was 724 B/op); noisy logs allocate 50-70% fewer bytes; dense synthetic workloads trade allocation count (5041 vs 80) for similar bytes
- **Cache key generation**: Optimized with single `strings.Builder` and standard integer formatting

## [0.3.0] — 2026-11-13