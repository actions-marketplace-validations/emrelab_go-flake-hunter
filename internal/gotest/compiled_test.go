package gotest

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/emrelab/go-flake-hunter/internal/domain"
)

// parityPkg creates a small temporary package for parity tests.
// Module "parity", two tests (TestA passes, TestB fails when run second).
func parityPkg(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module parity\n\ngo 1.21\n")
	// TestA always passes. TestB fails on the second run (accumulates state).
	writeFile(t, filepath.Join(dir, "parity_test.go"), `package parity

import "testing"

var counter int

func TestA(t *testing.T) {
	if 2+2 != 4 {
		t.Fatal("arithmetic broken")
	}
}

func TestB(t *testing.T) {
	counter++
	if counter > 1 {
		t.Fatal("accumulated state")
	}
}
`)
	return dir
}

// assertParity runs both Exec and Compiled with the same spec and asserts
// they produce equivalent results: same (test -> Status) set, same Order as a
// SET (parallel may vary; for -parallel=1 assert exact order), same
// BuildFailed, same State, same exit-code CLASS (0/nonzero).
func assertParity(t *testing.T, dir string, spec domain.RunSpec) {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test: runs real go test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	e := &Exec{}
	c := &Compiled{Inner: &Exec{}}

	resE, errE := e.Run(ctx, domain.RunSpec{Package: dir, RunRegex: spec.RunRegex, Count: spec.Count, Parallel: spec.Parallel, CPU: spec.CPU, ShuffleSeed: spec.ShuffleSeed, Short: spec.Short, Tags: spec.Tags, Timeout: spec.Timeout})
	resC, errC := c.Run(ctx, domain.RunSpec{Package: dir, RunRegex: spec.RunRegex, Count: spec.Count, Parallel: spec.Parallel, CPU: spec.CPU, ShuffleSeed: spec.ShuffleSeed, Short: spec.Short, Tags: spec.Tags, Timeout: spec.Timeout})

	// Both must return data, not infrastructure errors.
	if errE != nil && !errors.Is(errE, domain.ErrPackageFailed) && !errors.Is(errE, domain.ErrIncomplete) {
		t.Fatalf("Exec error: %v", errE)
	}
	if errC != nil && !errors.Is(errC, domain.ErrPackageFailed) && !errors.Is(errC, domain.ErrIncomplete) {
		t.Fatalf("Compiled error: %v", errC)
	}

	// Same set of test -> Status.
	if len(resE.Outcomes) != len(resC.Outcomes) {
		t.Fatalf("outcome count mismatch: Exec=%d Compiled=%d", len(resE.Outcomes), len(resC.Outcomes))
	}
	for name, oE := range resE.Outcomes {
		oC, ok := resC.Outcomes[name]
		if !ok {
			t.Errorf("test %q missing in Compiled outcomes", name)
			continue
		}
		if oE.Status != oC.Status {
			t.Errorf("test %q status mismatch: Exec=%v Compiled=%v", name, oE.Status, oC.Status)
		}
	}

	// Order as a SET (parallel may vary). For -parallel=1 assert exact order.
	if spec.Parallel == 1 {
		if len(resE.Order) != len(resC.Order) {
			t.Errorf("order length mismatch: Exec=%v Compiled=%v", resE.Order, resC.Order)
		} else {
			for i := range resE.Order {
				if resE.Order[i] != resC.Order[i] {
					t.Errorf("order[%d] mismatch: Exec=%q Compiled=%q", i, resE.Order[i], resC.Order[i])
				}
			}
		}
	} else {
		// Check same elements regardless of order.
		orderE := make(map[string]bool)
		orderC := make(map[string]bool)
		for _, n := range resE.Order {
			orderE[n] = true
		}
		for _, n := range resC.Order {
			orderC[n] = true
		}
		if len(orderE) != len(orderC) {
			t.Errorf("order set size mismatch: Exec=%v Compiled=%v", resE.Order, resC.Order)
		}
		for n := range orderE {
			if !orderC[n] {
				t.Errorf("order element %q in Exec but not Compiled", n)
			}
		}
	}

	// Same BuildFailed.
	if resE.BuildFailed != resC.BuildFailed {
		t.Errorf("BuildFailed mismatch: Exec=%v Compiled=%v", resE.BuildFailed, resC.BuildFailed)
	}

	// Same State.
	if resE.State != resC.State {
		t.Errorf("State mismatch: Exec=%v Compiled=%v", resE.State, resC.State)
	}

	// Same exit-code CLASS (0/nonzero).
	exitE := 0
	if resE.ExitCode != 0 {
		exitE = 1
	}
	exitC := 0
	if resC.ExitCode != 0 {
		exitC = 1
	}
	if exitE != exitC {
		t.Errorf("exit code class mismatch: Exec=%d Compiled=%d", resE.ExitCode, resC.ExitCode)
	}
}

func TestCompiledParityBasic(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test: runs real go test")
	}
	dir := parityPkg(t)

	specs := []domain.RunSpec{
		{Count: 1, Timeout: 60 * time.Second},                         // {count:1}
		{Count: 3, RunRegex: "^TestA$", Timeout: 60 * time.Second},    // {count:3, run:^TestA$}
		{RunRegex: "^TestA$", Parallel: 1, Timeout: 60 * time.Second}, // {run:^TestA$, parallel:1}
		{ShuffleSeed: 7, Parallel: 1, Timeout: 60 * time.Second},      // {shuffle:7, parallel:1}
		{Parallel: 2, Timeout: 60 * time.Second},                      // {parallel:2}
		{Short: true, Timeout: 60 * time.Second},                      // {short:true}
	}

	for i, spec := range specs {
		t.Run(fmt.Sprintf("spec%d", i), func(t *testing.T) {
			assertParity(t, dir, spec)
		})
	}
}

func TestCompiledParityBuildFailure(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test: runs real go test")
	}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module broken\n\ngo 1.21\n")
	writeFile(t, filepath.Join(dir, "broken_test.go"), "package broken\n\nfunc TestBroken(t *testing.T) { thisIsASyntaxError }\n")

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	e := &Exec{}
	c := &Compiled{Inner: &Exec{}}

	spec := domain.RunSpec{Package: dir, Count: 1, Timeout: 60 * time.Second}
	resE, errE := e.Run(ctx, spec)
	resC, errC := c.Run(ctx, spec)

	// Both must return data (build failure is data, not an error).
	if errE != nil {
		t.Fatalf("Exec error: %v", errE)
	}
	if errC != nil {
		t.Fatalf("Compiled error: %v", errC)
	}

	if !resE.BuildFailed {
		t.Fatalf("Exec: expected BuildFailed=true")
	}
	if !resC.BuildFailed {
		t.Fatalf("Compiled: expected BuildFailed=true")
	}
	if resE.BuildOutput == "" {
		t.Errorf("Exec: BuildOutput must be non-empty")
	}
	if resC.BuildOutput == "" {
		t.Errorf("Compiled: BuildOutput must be non-empty")
	}
}

func TestCompiledParityOsExitZero(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test: runs real go test")
	}
	// Package with a test that calls os.Exit(0). Without -test.paniconexit0
	// this would silently pass (no fail event in the stream). With it, the
	// stream matches `go test` exactly (--- FAIL + panic output + fail event).
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module exitzero\n\ngo 1.21\n")
	writeFile(t, filepath.Join(dir, "exit_test.go"), `package exitzero

import (
	"os"
	"testing"
)

func TestExitZero(t *testing.T) {
	os.Exit(0)
}
`)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	e := &Exec{}
	c := &Compiled{Inner: &Exec{}}

	spec := domain.RunSpec{Package: dir, Count: 1, Timeout: 60 * time.Second}
	resE, errE := e.Run(ctx, spec)
	resC, errC := c.Run(ctx, spec)

	// Both must return data.
	if errE != nil {
		t.Fatalf("Exec error: %v", errE)
	}
	if errC != nil {
		t.Fatalf("Compiled error: %v", errC)
	}

	// Both must report the test as FAIL (not pass, not skip).
	if !resE.Failed("TestExitZero") {
		t.Errorf("Exec: TestExitZero should have failed, got %v", resE.Outcomes["TestExitZero"].Status)
	}
	if !resC.Failed("TestExitZero") {
		t.Errorf("Compiled: TestExitZero should have failed, got %v", resC.Outcomes["TestExitZero"].Status)
	}

	// Exit code class must be non-zero for both.
	if resE.ExitCode == 0 {
		t.Errorf("Exec: expected non-zero exit code")
	}
	if resC.ExitCode == 0 {
		t.Errorf("Compiled: expected non-zero exit code")
	}
}

func TestCompiledParityVetFailure(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test: runs real go test")
	}
	// go test -c also runs vet and fails on a vet error, exactly like go test.
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module vetfail\n\ngo 1.21\n")
	// fmt.Printf %d with a string arg triggers a vet error.
	writeFile(t, filepath.Join(dir, "vet_test.go"), `package vetfail

import (
	"fmt"
	"testing"
)

func TestVet(t *testing.T) {
	fmt.Printf("%d", "not a number")
}
`)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	e := &Exec{}
	c := &Compiled{Inner: &Exec{}}

	spec := domain.RunSpec{Package: dir, Count: 1, Timeout: 60 * time.Second}
	resE, errE := e.Run(ctx, spec)
	resC, errC := c.Run(ctx, spec)

	// Both must return data (vet failure is a build failure).
	if errE != nil {
		t.Fatalf("Exec error: %v", errE)
	}
	if errC != nil {
		t.Fatalf("Compiled error: %v", errC)
	}

	if !resE.BuildFailed {
		t.Errorf("Exec: expected BuildFailed=true (vet failure)")
	}
	if !resC.BuildFailed {
		t.Errorf("Compiled: expected BuildFailed=true (vet failure)")
	}
	if resE.BuildOutput == "" {
		t.Errorf("Exec: BuildOutput must be non-empty")
	}
	if resC.BuildOutput == "" {
		t.Errorf("Compiled: BuildOutput must be non-empty")
	}
}

// TestCompiledBuildPathIsStable verifies that binPath returns the same path
// for the same (dir, tags) and different paths for different inputs. The
// returned path must be inside the user cache dir.
func TestCompiledBuildPathIsStable(t *testing.T) {
	c := &Compiled{Inner: &Exec{}}

	dir1 := t.TempDir()
	writeFile(t, filepath.Join(dir1, "go.mod"), "module stable\n\ngo 1.21\n")
	dir2 := t.TempDir()
	writeFile(t, filepath.Join(dir2, "go.mod"), "module stable2\n\ngo 1.21\n")

	// Same dir, no tags -> same path
	p1, err := c.binPath(dir1, nil)
	if err != nil {
		t.Fatalf("binPath: %v", err)
	}
	p2, err := c.binPath(dir1, nil)
	if err != nil {
		t.Fatalf("binPath: %v", err)
	}
	if p1 != p2 {
		t.Errorf("same dir+tags must return same path: %q vs %q", p1, p2)
	}

	// Different dir -> different path
	p3, err := c.binPath(dir2, nil)
	if err != nil {
		t.Fatalf("binPath: %v", err)
	}
	if p1 == p3 {
		t.Errorf("different dir must return different path: both %q", p1)
	}

	// Same dir, different tags -> different path
	p4, err := c.binPath(dir1, []string{"foo"})
	if err != nil {
		t.Fatalf("binPath: %v", err)
	}
	if p1 == p4 {
		t.Errorf("different tags must return different path: both %q", p1)
	}

	// Path must be inside user cache dir
	cacheDir, _ := os.UserCacheDir()
	expectedPrefix := filepath.Join(cacheDir, "go-flake-hunter", "testbin")
	if !strings.HasPrefix(p1, expectedPrefix) {
		t.Errorf("path %q must be inside %q", p1, expectedPrefix)
	}
	if !strings.HasSuffix(p1, ".test") && !strings.HasSuffix(p1, ".test.exe") {
		t.Errorf("path %q must end with .test or .test.exe", p1)
	}
}

// TestCompiledParityTestMainNonZeroExit is the regression that the missing
// package-level synthesis caused: a TestMain that exits non-zero while every
// test passes. `go test` derives RunPackageFailed from the exit status;
// test2json emits no package event at all, so Compiled used to report
// RunIncomplete, and hunt reported the package as skipped instead of failed.
func TestCompiledParityTestMainNonZeroExit(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test: runs real go test")
	}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module sickmain\n\ngo 1.21\n")
	writeFile(t, filepath.Join(dir, "main_test.go"), `package sickmain

import (
	"flag"
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	flag.Parse()
	code := m.Run()
	if flag.Lookup("test.list").Value.String() == "" && code == 0 {
		code = 7
	}
	os.Exit(code)
}

func TestPasses(t *testing.T) {}
`)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	spec := domain.RunSpec{Package: dir, Count: 1, Timeout: 60 * time.Second}
	resE, errE := (&Exec{}).Run(ctx, spec)
	resC, errC := (&Compiled{Inner: &Exec{}}).Run(ctx, spec)

	// A package-level failure is data for both runners, not an error value.
	if errE != nil && !errors.Is(errE, domain.ErrPackageFailed) {
		t.Fatalf("Exec error: %v", errE)
	}
	if errC != nil && !errors.Is(errC, domain.ErrPackageFailed) {
		t.Fatalf("Compiled error: %v", errC)
	}
	if resE.State != domain.RunPackageFailed {
		t.Fatalf("Exec state = %q, want %q", resE.State, domain.RunPackageFailed)
	}
	if resC.State != domain.RunPackageFailed {
		t.Fatalf("Compiled state = %q, want %q (exit 7 with every test passing)", resC.State, domain.RunPackageFailed)
	}
	if !resC.Passed("TestPasses") {
		t.Errorf("Compiled: TestPasses should pass, got %v", resC.Outcomes["TestPasses"].Status)
	}
	if resC.ExitCode != 7 {
		t.Errorf("Compiled exit code = %d, want 7", resC.ExitCode)
	}
}

// TestCompiledFallsBackForWitnessSource proves that when spec.Source != "",
// Compiled.Run delegates to Inner (Exec), AND that the Inner's GoBin is honoured
// for both paths. We use a logging shim that appends the invoked argv to a
// log file and then execs the REAL go binary (passed via GOFLakeHunter_REAL_GO
// env var). Then:
// - Source=="" run must log a line containing "test -c" and must NOT log "test -json"
// - Source!="" run must log "test -json"
// Skip on Windows.
func TestCompiledFallsBackForWitnessSource(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test: runs real go test")
	}
	if runtime.GOOS == "windows" {
		t.Skip("shell script fallback not portable to Windows")
	}

	// Find real go binary
	realGo, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("go not found: %v", err)
	}

	// Create a temp package that passes normally.
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module witnessfallback\n\ngo 1.21\n")
	writeFile(t, filepath.Join(dir, "pass_test.go"), `package witnessfallback

import "testing"

func TestPass(t *testing.T) {}
`)

	// Create a logging shim that records argv and execs real go.
	logFile := filepath.Join(t.TempDir(), "go-shim.log")
	shimDir := t.TempDir()
	shimPath := filepath.Join(shimDir, "go")
	shimScript := fmt.Sprintf(`#!/bin/sh
# Log the full command line (argv)
echo "ARGS:$*" >> %s
# Exec the real go binary
exec %s "$@"
`, logFile, realGo)
	writeFile(t, shimPath, shimScript)
	if err := os.Chmod(shimPath, 0o755); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Inner that uses the logging shim.
	inner := &Exec{GoBin: shimPath}
	c := &Compiled{Inner: inner}

	// Source=="" should SUCCEED (uses compiled path, which calls shim for -c).
	resOk, errOk := c.Run(ctx, domain.RunSpec{Package: dir, Count: 1, Timeout: 60 * time.Second})
	if errOk != nil {
		t.Fatalf("Source==\"\" must succeed: %v", errOk)
	}
	if !resOk.Passed("TestPass") {
		t.Errorf("Source==\"\": TestPass should pass, got %v", resOk.Outcomes["TestPass"].Status)
	}

	// Read log and verify: must contain "test -c", must NOT contain "test -json"
	logBytes, _ := os.ReadFile(logFile)
	log := string(logBytes)
	if !strings.Contains(log, "test -c") {
		t.Errorf("Source==\"\" log must contain 'test -c', got: %s", log)
	}
	if strings.Contains(log, "test -json") {
		t.Errorf("Source==\"\" log must NOT contain 'test -json', got: %s", log)
	}

	// Clear log for next run
	_ = os.WriteFile(logFile, []byte(""), 0o644)

	// Source!="": delegates to inner, which uses the shim for -json.
	resFail, errFail := c.Run(ctx, domain.RunSpec{Package: dir, Count: 1, Timeout: 60 * time.Second, Source: "witness-xyz"})
	// The inner exec will use the shim for "go test -json ...".
	// Any error is acceptable; we just check the log.
	logBytes, _ = os.ReadFile(logFile)
	log = string(logBytes)
	if !strings.Contains(log, "test -json") {
		t.Errorf("Source!=\"\" log must contain 'test -json', got: %s", log)
	}
	_ = resFail
	_ = errFail

	// Race: needs its own `go test -c -race` build, so it must also go through
	// the tool. This is how ConfirmRace's cross-clause path runs, and it passes
	// no Source — refusing it here turned `--race` into an internal error.
	_ = os.WriteFile(logFile, []byte(""), 0o644)
	_, _ = c.Run(ctx, domain.RunSpec{Package: dir, Count: 1, Timeout: 120 * time.Second, Race: true})
	logBytes, _ = os.ReadFile(logFile)
	log = string(logBytes)
	if !strings.Contains(log, "test -json") {
		t.Errorf("Race log must contain 'test -json' (delegate to Exec), got: %s", log)
	}
	if strings.Contains(log, "test -c") {
		t.Errorf("Race log must NOT contain 'test -c' (a race build is not the compiled path), got: %s", log)
	}
}

// A passing test that PRINTS a result-shaped line must stay a pass. Plain
// -test.v let the framework's converter turn the line into a fail event, and
// the fast path (the default) then reported the test as broken.
func TestCompiledIgnoresResultShapedTestOutput(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module phantom\n\ngo 1.21\n")
	writeFile(t, filepath.Join(dir, "phantom_test.go"), `package phantom

import (
	"fmt"
	"testing"
)

func TestPass(t *testing.T) {
	fmt.Println("--- FAIL: TestPass (0.00s)")
}
`)
	spec := domain.RunSpec{Package: dir, RunRegex: "^TestPass$", Count: 1}
	for name, r := range map[string]Runner{"exec": &Exec{}, "compiled": &Compiled{Inner: &Exec{}}} {
		res, err := r.Run(context.Background(), spec)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if res.Failed("TestPass") || res.State != domain.RunPassed {
			t.Errorf("%s: printed failure line became a verdict: state=%s status=%v", name, res.State, res.Status("TestPass"))
		}
	}
}

// GOFLAGS test-runtime flags select which tests run. The compiled path runs the
// built binary directly, so it cannot honour them; parity with the go test path
// comes first, measured on the same package.
func TestCompiledHonoursRuntimeTestFlagsFromGOFLAGS(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module flags\n\ngo 1.21\n")
	writeFile(t, filepath.Join(dir, "flags_test.go"), "package flags\n\nimport \"testing\"\n\nfunc TestSelected(t *testing.T) {}\n")
	t.Setenv("GOFLAGS", "-skip=TestSelected")

	spec := domain.RunSpec{Package: dir, RunRegex: "^TestSelected$", Count: 1}
	slow, errSlow := (&Exec{}).Run(context.Background(), spec)
	fast, errFast := (&Compiled{Inner: &Exec{}}).Run(context.Background(), spec)
	if (errSlow == nil) != (errFast == nil) {
		t.Fatalf("error class diverged: slow=%v fast=%v", errSlow, errFast)
	}
	if slow.State != fast.State || slow.Status("TestSelected") != fast.Status("TestSelected") {
		t.Errorf("engines disagree under GOFLAGS: slow=%s/%v fast=%s/%v",
			slow.State, slow.Status("TestSelected"), fast.State, fast.Status("TestSelected"))
	}
}

// os.Exit inside a test kills the process; cmd/go synthesizes the failure from
// the exit status. The compiled path must do the same, including the terminal
// count, or the probe reads the run as unfinished and skips the package.
func TestCompiledSynthesizesFailureForOsExit(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module exiter\n\ngo 1.21\n")
	writeFile(t, filepath.Join(dir, "exit_test.go"), `package exiter

import (
	"os"
	"testing"
)

func TestExitZero(t *testing.T) { os.Exit(0) }
`)
	res, err := (&Compiled{Inner: &Exec{}}).Run(context.Background(), domain.RunSpec{Package: dir, RunRegex: "^TestExitZero$", Count: 1})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Failed("TestExitZero") || !res.Completed("TestExitZero", 1) {
		t.Fatalf("state=%s status=%v terminal=%d; want a completed failure", res.State, res.Status("TestExitZero"), res.Terminal["TestExitZero"])
	}
}
