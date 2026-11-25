package gotest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/emrelab/go-flake-hunter/internal/domain"
	"github.com/emrelab/go-flake-hunter/internal/policy"
)

// Compiled builds the test binary once with `go test -c` and executes it
// directly, piping stdout through `go tool test2json`. It is used ONLY when
// spec.Source == ""; witness installs (Source != "") fall back to Exec so the
// generated-witness path stays byte-identical to today.
//
// go test -c is already incremental via Go's build cache (0.05 s warm), so
// rebuilding before every run keeps Go's own invalidation and adds no staleness
// logic of our own.
type Compiled struct {
	Inner *Exec

	importPathMu sync.Mutex
	importPaths  map[string]string

	// Stable test binary path per (package dir, tags) — same output path
	// lets Go reuse the link action; changing -o forces a full relink.
	binDirOnce sync.Once
	binDir     string
	binDirErr  error

	// Memoized test2json binary path; -n tool test2json prints the binary
	// without running it. Fallback to "go tool test2json" if resolution fails.
	t2jOnce   sync.Once
	t2jPath   string
	t2jDirect bool
}

var _ Runner = (*Compiled)(nil)

// Discover passes through to the inner runner.
func (c *Compiled) Discover(ctx context.Context, pkgDir string) ([]domain.TestID, error) {
	return c.Inner.Discover(ctx, pkgDir)
}

// Run executes the compiled test binary directly.
func (c *Compiled) Run(ctx context.Context, spec domain.RunSpec) (*domain.RunResult, error) {
	// Two cases need the go tool itself:
	//   - witness installs (Source != ""): the generated file must be built by
	//     the same path as today, byte for byte.
	//   - -race: it needs its own build (`go test -c -race`), and the search
	//     never runs under race — this is one witness confirmation, not a hot
	//     path, so paying the tool's planning cost here is the right trade.
	if spec.Source != "" || spec.Race {
		return c.Inner.Run(ctx, spec)
	}

	// Fill defaults exactly like Exec.Run does.
	if spec.Timeout <= 0 && c.Inner.RunTimeout > 0 {
		spec.Timeout = c.Inner.RunTimeout
	}
	spec = spec.WithDefaults()

	// Lock the package directory.
	ctx, release, err := policy.Locked(ctx, spec.Package)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, domain.Wrap(domain.ErrInternal, "could not acquire lock: %v", err)
	}
	defer release()

	// Race is handled above by delegating to Exec: it needs its own build.
	// Stable binary path in user cache dir. The path must be stable across runs
	// or Go relinks (link cache is keyed by output path); the file is overwritten
	// in place, so nothing accumulates beyond one entry per package.
	bin, err := c.binPath(spec.Package, spec.Tags)
	if err != nil {
		return nil, err
	}

	// Build the test binary with `go test -c`. Use the inner's GoBin so a
	// configured toolchain is honoured.
	goBin := c.Inner.goBin()
	buildArgs := []string{"test", "-c", "-o", bin, "."}
	if len(spec.Tags) > 0 {
		buildArgs = append(buildArgs, "-tags", strings.Join(spec.Tags, ","))
	}
	build := commandContext(ctx, goBin, buildArgs...)
	build.Dir = spec.Package

	var buildBuf bytes.Buffer
	build.Stdout = &buildBuf
	build.Stderr = &buildBuf

	buildErr := build.Run()

	// Context cancellation takes precedence.
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	if buildErr != nil {
		var exitErr *exec.ExitError
		if !asExitError(buildErr, &exitErr) {
			// Could not start go at all (missing, bad dir).
			return nil, domain.Wrap(domain.ErrInternal, "could not start go test -c (%s)", spec.Package)
		}
		// Build failed (non-zero exit from go test -c). vet failures also appear here.
		return &domain.RunResult{
			Spec:        spec,
			BuildFailed: true,
			BuildOutput: Normalize(buildBuf.String()),
			State:       domain.RunIncomplete,
		}, nil
	}

	// Prepare test binary arguments. -test.paniconexit0 is MANDATORY: without it
	// a test calling os.Exit(0) just ends the process, so the last round has no
	// terminal event at all. With it the call panics and the stack lands in the
	// output. test2json still emits no fail event for that panic (cmd/go
	// synthesizes it), which is why Run upgrades any started-but-unfinished
	// test below instead of trusting the stream.
	args := []string{
		"-test.v",
		"-test.paniconexit0",
		"-test.count", strconv.Itoa(spec.Count),
		"-test.timeout", spec.Timeout.String(),
	}
	if spec.RunRegex != "" {
		args = append(args, "-test.run", spec.RunRegex)
	}
	if spec.Parallel > 0 {
		args = append(args, "-test.parallel", strconv.Itoa(spec.Parallel))
	}
	if spec.CPU > 0 {
		args = append(args, "-test.cpu", strconv.Itoa(spec.CPU))
	}
	if spec.ShuffleSeed != 0 {
		args = append(args, "-test.shuffle", strconv.FormatInt(spec.ShuffleSeed, 10))
	}
	if c.Inner.Short || spec.Short {
		args = append(args, "-test.short")
	}

	// Run the test binary with a context that has slack so Go's own -timeout fires first.
	runCtx, cancel := context.WithTimeout(ctx, spec.Timeout+ctxSlack)
	defer cancel()

	run := commandContext(runCtx, bin, args...)
	run.Dir = spec.Package
	run.Env = append(os.Environ(), spec.Env...)

	var stdout, stderr bytes.Buffer
	run.Stdout = &stdout
	run.Stderr = &stderr

	start := time.Now()
	runErr := run.Run()
	elapsed := time.Since(start)

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if errors.Is(runErr, exec.ErrWaitDelay) {
		return nil, domain.Wrap(domain.ErrIncomplete, "%s left output pipes open after exit", spec.Package)
	}

	exitCode := 0
	if runErr != nil {
		var exitErr *exec.ExitError
		if !asExitError(runErr, &exitErr) {
			return nil, domain.Wrap(domain.ErrInternal, "could not start go test binary (%s)", spec.Package)
		}
		exitCode = exitErr.ExitCode()
	}

	timedOut := runCtx.Err() == context.DeadlineExceeded

	// Get import path for test2json -p flag (memoized via inner).
	ip, err := c.importPath(ctx, spec.Package)
	if err != nil {
		ip = ""
	}

	// Pipe ONLY stdout through test2json. go test pipes stdout to test2json and
	// passes stderr through untouched; combining them turns a test's stderr
	// writes into JSON frames. We keep stderr for BuildOutput/tail only.
	var parsedOut []byte
	if ip != "" {
		t2jPath, direct := c.test2jsonPath()
		t2jCmd, t2jArgs := t2jPath, []string{"-p", ip}
		if !direct {
			t2jCmd, t2jArgs = goBin, []string{"tool", "test2json", "-p", ip}
		}
		// commandContext returns a platform-specific type (the Windows variant
		// carries a Job Object), so it is used inline rather than assigned to
		// *exec.Cmd.
		t2j := commandContext(ctx, t2jCmd, t2jArgs...)
		t2j.Dir = spec.Package
		t2j.Stdin = bytes.NewReader(stdout.Bytes())
		var t2jOut bytes.Buffer
		t2j.Stdout = &t2jOut
		if err := t2j.Run(); err == nil {
			parsedOut = t2jOut.Bytes()
		}
	}

	// If test2json failed or import path unavailable, parse raw stdout.
	// A parse loss must not become a false verdict.
	if parsedOut == nil {
		parsedOut = stdout.Bytes()
	}

	res, perr := parseEvents(parsedOut, spec, exitCode, elapsed, timedOut)
	if perr != nil {
		return nil, domain.Wrap(domain.ErrInternal, "could not read test2json output (%s): %s", spec.Package, tail(stderr.String(), 2))
	}

	// test2json limitation: it does not emit a terminal event for a test the
	// binary never finished (panic, os.Exit, runtime.Goexit). cmd/go covers
	// those by synthesizing a failure; this is that rule: a test that STARTED
	// (in res.Order) and has fewer terminal events than spec.Count in a
	// non-zero-exit run did not complete its round.
	if exitCode != 0 {
		for _, name := range res.Order {
			if res.Terminal[name] < spec.Count {
				o := res.Outcomes[name]
				o.Status = domain.StatusFail
				res.Outcomes[name] = o
			}
		}
	}
	// Recompute state if any test was upgraded to fail.
	for _, outcome := range res.Outcomes {
		if outcome.Status == domain.StatusFail {
			if res.State != domain.RunTestFailed && res.State != domain.RunPackageFailed {
				res.State = domain.RunTestFailed
			}
			break
		}
	}

	// test2json never emits a package-level event; cmd/go synthesizes it from
	// the process exit status. parseEvents therefore sees "incomplete" for every
	// Compiled run, and a TestMain that exits non-zero while every test passes
	// would read as "scan cut short" (exit 7, package skipped) instead of
	// "package failed" (exit 7, but with the verdict). Derive it the way cmd/go
	// does, and only when the run was not actually cut short.
	if res.State == domain.RunIncomplete && !timedOut && exitCode >= 0 {
		if exitCode == 0 {
			res.State = domain.RunPassed
		} else {
			res.State = domain.RunPackageFailed
			for _, outcome := range res.Outcomes {
				if outcome.Status == domain.StatusFail {
					res.State = domain.RunTestFailed
					break
				}
			}
		}
	}

	// Build failure post-check (copied verbatim from Exec.Run).
	if res.BuildFailed || !timedOut && len(res.Outcomes) == 0 && exitCode != 0 &&
		(strings.Contains(res.BuildOutput, "[setup failed]") || strings.Contains(res.BuildOutput, "[build failed]")) {
		res.BuildFailed = true
		if strings.TrimSpace(res.BuildOutput) == "" {
			res.BuildOutput = firstLines(stderr.String(), 5)
		}
	}
	if !res.BuildFailed {
		switch res.State {
		case domain.RunPackageFailed:
			return res, domain.Wrap(domain.ErrPackageFailed, "%s", spec.Package)
		case domain.RunIncomplete:
			return res, domain.Wrap(domain.ErrIncomplete, "%s", spec.Package)
		}
	}
	return res, nil
}

// binPath returns a stable binary path in the user cache directory for the
// given package and tags. Same (dir, tags) -> same path, so Go reuses the
// link action. Different (dir, tags) -> different paths.
func (c *Compiled) binPath(pkgDir string, tags []string) (string, error) {
	c.binDirOnce.Do(func() {
		base, err := os.UserCacheDir()
		if err != nil {
			c.binDirErr = err
			return
		}
		root := filepath.Join(base, "go-flake-hunter", "testbin")
		if err := os.MkdirAll(root, 0o700); err != nil {
			c.binDirErr = err
			return
		}
		c.binDir = root
	})
	if c.binDirErr != nil {
		return "", c.binDirErr
	}
	h := fnv.New64a()
	h.Write([]byte(filepath.Clean(pkgDir)))
	h.Write([]byte{0})
	h.Write([]byte(strings.Join(tags, ",")))
	name := fmt.Sprintf("%x.test", h.Sum64())
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(c.binDir, name), nil
}

// test2jsonPath resolves the test2json binary path once. `goBin -n tool test2json`
// prints the binary path without running it. If resolution fails, it falls back
// to running test2json through the go binary (direct=false).
func (c *Compiled) test2jsonPath() (path string, direct bool) {
	c.t2jOnce.Do(func() {
		goBin := c.Inner.goBin()
		cmd := commandContext(context.Background(), goBin, "-n", "tool", "test2json")
		var out bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &out
		if cmd.Run() == nil {
			p := strings.TrimSpace(out.String())
			if p != "" {
				if _, err := os.Stat(p); err == nil {
					c.t2jPath = p
					c.t2jDirect = true
					return
				}
			}
		}
		c.t2jDirect = false
	})
	return c.t2jPath, c.t2jDirect
}

// importPath resolves and memoizes the module import path for a directory,
// delegating to the inner Exec's exported helper.
func (c *Compiled) importPath(ctx context.Context, pkgDir string) (string, error) {
	c.importPathMu.Lock()
	if c.importPaths == nil {
		c.importPaths = make(map[string]string)
	}
	if ip, ok := c.importPaths[pkgDir]; ok {
		c.importPathMu.Unlock()
		return ip, nil
	}
	c.importPathMu.Unlock()

	ip, err := c.Inner.importPath(ctx, pkgDir)
	if err != nil {
		return "", err
	}

	c.importPathMu.Lock()
	c.importPaths[pkgDir] = ip
	c.importPathMu.Unlock()
	return ip, nil
}
