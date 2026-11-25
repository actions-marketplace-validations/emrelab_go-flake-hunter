package probe_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emrelab/go-flake-hunter/internal/domain"
	"github.com/emrelab/go-flake-hunter/internal/facts"
	"github.com/emrelab/go-flake-hunter/internal/gotest"
	"github.com/emrelab/go-flake-hunter/internal/probe"
)

// Cross-clause tests share one binary and are replayed by top-level selection.
func TestOrderReproducesCrossClauseSelection(t *testing.T) {
	tgt := probe.Target{
		Test: domain.TestID{Package: "p", Name: "TestVictim"},
		Dir:  t.TempDir(),
		Facts: facts.PkgFacts{Clause: map[string]string{
			"TestVictim":        "p_test",
			"TestInnerPolluter": "p",
		}},
	}

	fake := &gotest.Fake{RunFn: func(s domain.RunSpec) (*domain.RunResult, error) {
		if s.RunRegex == "^TestVictim$" {
			return gotest.Result(s, map[string]domain.Status{"TestVictim": domain.StatusPass}), nil
		}
		// Whole package: the inner-package test runs first, the victim fails.
		res := gotest.Result(s, map[string]domain.Status{
			"TestInnerPolluter": domain.StatusPass,
			"TestVictim":        domain.StatusFail,
		})
		res.Order = []string{"TestInnerPolluter", "TestVictim"}
		return res, nil
	}}

	w, err := probe.Order{Seeds: []int64{1}}.Probe(context.Background(), tgt, fake)
	if err != nil || w == nil || !w.CrossClause || w.CrossBinary || w.Func != "" {
		t.Fatalf("cross-clause witness = %+v, %v", w, err)
	}
	for _, run := range fake.Runs() {
		if strings.Contains(run.RunRegex, "TestFlakeWitness") {
			t.Fatal("cross-clause witness used a generated chain")
		}
	}

}

// Same-clause candidates exist but none reproduces the failure, and the
// package does contain tests from the other clause: the missing ingredient is
// across the visibility boundary. Top-level selection still reproduces it.
func TestOrderFallsBackToCrossClauseSelection(t *testing.T) {
	tgt := probe.Target{
		Test: domain.TestID{Package: "p", Name: "TestVictim"},
		Dir:  t.TempDir(),
		Facts: facts.PkgFacts{Clause: map[string]string{
			"TestVictim":        "p_test",
			"TestSibling":       "p_test",
			"TestInnerPolluter": "p",
		}},
	}

	fake := &gotest.Fake{RunFn: func(s domain.RunSpec) (*domain.RunResult, error) {
		switch {
		case s.RunRegex == "^TestVictim$":
			return gotest.Result(s, map[string]domain.Status{"TestVictim": domain.StatusPass}), nil
		case strings.HasPrefix(s.RunRegex, "^TestFlakeWitness"):
			// The victim inside the witness passes: same-clause candidates do not explain it.
			return gotest.Result(s, nil), nil
		default:
			res := gotest.Result(s, map[string]domain.Status{
				"TestSibling":       domain.StatusPass,
				"TestInnerPolluter": domain.StatusPass,
				"TestVictim":        domain.StatusFail,
			})
			res.Order = []string{"TestSibling", "TestInnerPolluter", "TestVictim"}
			return res, nil
		}
	}}

	w, err := probe.Order{Seeds: []int64{1}}.Probe(context.Background(), tgt, fake)
	if err != nil || w == nil || !w.CrossClause {
		t.Fatalf("cross-clause fallback = %+v, %v", w, err)
	}

}

// Single-clause packages must behave unchanged: the same-clause polluter is found.
func TestOrderSingleClauseStillFindsPolluter(t *testing.T) {
	tgt := probe.Target{
		Test: domain.TestID{Package: "p", Name: "TestVictim"},
		Dir:  t.TempDir(),
		Facts: facts.PkgFacts{Clause: map[string]string{
			"TestVictim":   "p",
			"TestPolluter": "p",
		}},
	}

	fake := &gotest.Fake{RunFn: func(s domain.RunSpec) (*domain.RunResult, error) {
		switch {
		case s.RunRegex == "^TestVictim$":
			return gotest.Result(s, map[string]domain.Status{"TestVictim": domain.StatusPass}), nil
		case strings.HasPrefix(s.RunRegex, "^TestFlakeWitness"):
			// The witness brings the victim down.
			name := strings.Trim(s.RunRegex, "^$")
			res := gotest.Result(s, nil)
			res.Outcomes[domain.WitnessSubtest(name, "TestVictim")] = domain.Outcome{Status: domain.StatusFail}
			return res, nil
		default:
			res := gotest.Result(s, map[string]domain.Status{
				"TestPolluter": domain.StatusPass,
				"TestVictim":   domain.StatusFail,
			})
			res.Order = []string{"TestPolluter", "TestVictim"}
			return res, nil
		}
	}}

	w, err := probe.Order{Seeds: []int64{1}}.Probe(context.Background(), tgt, fake)
	if err != nil {
		t.Fatalf("a witness was expected: %v", err)
	}
	if len(w.Polluters) != 1 || w.Polluters[0].Name != "TestPolluter" {
		t.Fatalf("polluter = %+v", w.Polluters)
	}
}

// Ctrl-C mid-search must leave no witness file: leftover debris breaks every
// later build in the package and the user has to delete it by hand.
func TestOrderCancelLeavesNoWitnessFile(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	tgt := probe.Target{
		Test: domain.TestID{Package: "p", Name: "TestVictim"},
		Dir:  dir,
		Facts: facts.PkgFacts{Clause: map[string]string{
			"TestVictim":   "p",
			"TestPolluter": "p",
		}},
	}

	fake := &gotest.Fake{RunFn: func(s domain.RunSpec) (*domain.RunResult, error) {
		switch {
		case s.RunRegex == "^TestVictim$":
			return gotest.Result(s, map[string]domain.Status{"TestVictim": domain.StatusPass}), nil
		case strings.HasPrefix(s.RunRegex, "^TestFlakeWitness"):
			cancel() // the user pressed Ctrl-C
			return nil, context.Canceled
		default:
			res := gotest.Result(s, map[string]domain.Status{
				"TestPolluter": domain.StatusPass,
				"TestVictim":   domain.StatusFail,
			})
			res.Order = []string{"TestPolluter", "TestVictim"}
			return res, nil
		}
	}}

	_, err := probe.Order{Seeds: []int64{1}}.Probe(ctx, tgt, fake)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected a cancellation error, got %v", err)
	}
	leftovers, _ := filepath.Glob(filepath.Join(dir, "zz_goflakehunter_witness_*"))
	if len(leftovers) > 0 {
		t.Errorf("witness files left behind: %v", leftovers)
	}
}

func TestOrderRetainsPartialVerificationAsSuspected(t *testing.T) {
	for _, failures := range []int{0, 2, 3} {
		t.Run(fmt.Sprint(failures), func(t *testing.T) {
			rounds := 0
			tgt := probe.Target{Dir: t.TempDir(), Test: domain.TestID{Package: "p", Name: "TestVictim"}, Facts: facts.PkgFacts{Clause: map[string]string{"TestVictim": "p", "TestPolluter": "p"}}}
			fake := &gotest.Fake{RunFn: func(s domain.RunSpec) (*domain.RunResult, error) {
				if s.RunRegex == "^TestVictim$" {
					return gotest.Result(s, map[string]domain.Status{"TestVictim": domain.StatusPass}), nil
				}
				if strings.Contains(s.RunRegex, "TestFlakeWitness") {
					rounds++
					name := strings.Trim(s.RunRegex, "^$")
					status := domain.StatusPass
					if rounds <= failures {
						status = domain.StatusFail
					}
					return gotest.Result(s, map[string]domain.Status{domain.WitnessSubtest(name, "TestVictim"): status}), nil
				}
				res := gotest.Result(s, map[string]domain.Status{"TestVictim": domain.StatusFail})
				res.Order = []string{"TestPolluter", "TestVictim"}
				return res, nil
			}}
			w, err := probe.Order{Verify: 3}.Probe(context.Background(), tgt, fake)
			if rounds != 3 {
				t.Fatalf("verification stopped early: %d", rounds)
			}
			switch failures {
			case 0:
				if w != nil || !errors.Is(err, domain.ErrNotReproducible) {
					t.Fatalf("zero reproduction retained: %+v %v", w, err)
				}
			case 2:
				if w == nil || w.Verification == nil || w.Verification.Failures != 2 || !errors.Is(err, domain.ErrNotReproducible) {
					t.Fatalf("partial=%+v %v", w, err)
				}
			case 3:
				if w == nil || w.Verification != nil || err != nil {
					t.Fatalf("confirmed=%+v %v", w, err)
				}
			}
		})
	}
}

func TestCrossClauseKeepsObservedShuffleSeed(t *testing.T) {
	tgt := probe.Target{Dir: t.TempDir(), Test: domain.TestID{Name: "TestVictim"}, Facts: facts.PkgFacts{Clause: map[string]string{"TestVictim": "p_test", "TestPolluter": "p"}}}
	fake := &gotest.Fake{RunFn: func(s domain.RunSpec) (*domain.RunResult, error) {
		status := domain.StatusPass
		if s.ShuffleSeed == 21 {
			status = domain.StatusFail
		}
		res := gotest.Result(s, map[string]domain.Status{"TestVictim": status})
		res.Order = []string{"TestPolluter", "TestVictim"}
		return res, nil
	}}
	w, err := probe.Order{Seeds: []int64{21}}.Probe(context.Background(), tgt, fake)
	if err != nil || w == nil || !w.CrossClause || w.Seed != 21 {
		t.Fatalf("seeded witness=%+v %v", w, err)
	}
	for _, s := range fake.Runs() {
		if s.RunRegex != "" && s.ShuffleSeed != 21 {
			t.Fatalf("selected replay lost seed: %+v", s)
		}
	}
}

// TestOrderStopsWhenPermutationSpaceIsExhausted verifies the early-stop when
// all n! permutations have been observed. With 2 tests, n! = 2. The fake
// returns the baseline order for seed=0 (baseline run) and for the first
// seed, then the other permutation for the second seed. The victim never
// fails. The loop must stop after seeing both distinct permutations:
// 1 baseline + 2 seed runs = 3 total.
func TestOrderStopsWhenPermutationSpaceIsExhausted(t *testing.T) {
	tgt := probe.Target{
		Test: domain.TestID{Package: "p", Name: "TestA"},
		Dir:  t.TempDir(),
		Facts: facts.PkgFacts{Clause: map[string]string{
			"TestA": "p",
			"TestB": "p",
		}},
	}

	seedCount := 0
	fake := &gotest.Fake{RunFn: func(s domain.RunSpec) (*domain.RunResult, error) {
		if s.RunRegex == "^TestA$" {
			return gotest.Result(s, map[string]domain.Status{"TestA": domain.StatusPass}), nil
		}
		var order []string
		if s.ShuffleSeed == 0 {
			// Baseline run (no shuffle)
			order = []string{"TestA", "TestB"}
		} else {
			seedCount++
			if seedCount%2 == 1 {
				// First seed returns same as baseline
				order = []string{"TestA", "TestB"}
			} else {
				// Second seed returns the other permutation
				order = []string{"TestB", "TestA"}
			}
		}
		res := gotest.Result(s, map[string]domain.Status{"TestA": domain.StatusPass, "TestB": domain.StatusPass})
		res.Order = order
		return res, nil
	}}

	_, err := probe.Order{Seeds: probe.DefaultSeeds}.Probe(context.Background(), tgt, fake)
	if !errors.Is(err, domain.ErrNotReproduced) {
		t.Fatalf("expected ErrNotReproduced, got %v", err)
	}
	if fake.CallCount() != 3 {
		t.Fatalf("expected 3 runs (1 baseline + 2 seeds), got %d", fake.CallCount())
	}
}

// TestOrderSkipsShuffleForSingleTest verifies that with n=1 (only one test
// in the package), the seed loop is skipped entirely because 1! = 1 and the
// baseline run already covers the only permutation.
func TestOrderSkipsShuffleForSingleTest(t *testing.T) {
	tgt := probe.Target{
		Test: domain.TestID{Package: "p", Name: "TestA"},
		Dir:  t.TempDir(),
		Facts: facts.PkgFacts{Clause: map[string]string{
			"TestA": "p",
		}},
	}

	fake := &gotest.Fake{RunFn: func(s domain.RunSpec) (*domain.RunResult, error) {
		if s.RunRegex == "^TestA$" {
			return gotest.Result(s, map[string]domain.Status{"TestA": domain.StatusPass}), nil
		}
		res := gotest.Result(s, map[string]domain.Status{"TestA": domain.StatusPass})
		res.Order = []string{"TestA"}
		return res, nil
	}}

	_, err := probe.Order{Seeds: probe.DefaultSeeds}.Probe(context.Background(), tgt, fake)
	if !errors.Is(err, domain.ErrNotReproduced) {
		t.Fatalf("expected ErrNotReproduced, got %v", err)
	}
	if fake.CallCount() != 1 {
		t.Fatalf("expected 1 run (baseline only, no shuffle), got %d", fake.CallCount())
	}
	runs := fake.Runs()
	if len(runs) != 1 || runs[0].ShuffleSeed != 0 {
		t.Fatalf("expected single baseline run with ShuffleSeed=0, got %+v", runs)
	}
}

// TestOrderDoesNotStopWhilePermutationsRemain verifies that the early-stop
// does NOT trigger when distinct permutations observed < n!. With 3 tests,
// n! = 6. The fake always returns the same order. All 12 seeds must be tried:
// 1 baseline + 12 seeds = 13 total runs.
func TestOrderDoesNotStopWhilePermutationsRemain(t *testing.T) {
	tgt := probe.Target{
		Test: domain.TestID{Package: "p", Name: "TestA"},
		Dir:  t.TempDir(),
		Facts: facts.PkgFacts{Clause: map[string]string{
			"TestA": "p",
			"TestB": "p",
			"TestC": "p",
		}},
	}

	fake := &gotest.Fake{RunFn: func(s domain.RunSpec) (*domain.RunResult, error) {
		if s.RunRegex == "^TestA$" {
			return gotest.Result(s, map[string]domain.Status{"TestA": domain.StatusPass}), nil
		}
		res := gotest.Result(s, map[string]domain.Status{"TestA": domain.StatusPass, "TestB": domain.StatusPass, "TestC": domain.StatusPass})
		res.Order = []string{"TestA", "TestB", "TestC"}
		return res, nil
	}}

	_, err := probe.Order{Seeds: probe.DefaultSeeds}.Probe(context.Background(), tgt, fake)
	if !errors.Is(err, domain.ErrNotReproduced) {
		t.Fatalf("expected ErrNotReproduced, got %v", err)
	}
	if fake.CallCount() != 13 {
		t.Fatalf("expected 13 runs (1 baseline + 12 seeds), got %d", fake.CallCount())
	}
}

// TestOrderIgnoresShortPermutation proves a seed run that reports FEWER tests
// than the baseline (an aborted/partial run) does not count toward exhaustion.
// 2 tests => space 2. Baseline order [TestA TestB]. Seed 1 returns only
// [TestA] (truncated). Seed 2 returns [TestB TestA]. With the guard: 3 runs
// total (baseline + 2 seeds). Without the guard the truncated order would be
// counted as a distinct permutation, seen would hit 2 and the loop would stop
// after 2 runs.
func TestOrderIgnoresShortPermutation(t *testing.T) {
	tgt := probe.Target{
		Test: domain.TestID{Package: "p", Name: "TestA"},
		Dir:  t.TempDir(),
		Facts: facts.PkgFacts{Clause: map[string]string{
			"TestA": "p",
			"TestB": "p",
		}},
	}

	fake := &gotest.Fake{RunFn: func(s domain.RunSpec) (*domain.RunResult, error) {
		if s.RunRegex == "^TestA$" {
			return gotest.Result(s, map[string]domain.Status{"TestA": domain.StatusPass}), nil
		}
		var order []string
		if s.ShuffleSeed == 0 {
			// Baseline run
			order = []string{"TestA", "TestB"}
		} else if s.ShuffleSeed <= 1 {
			// Seed 1: truncated run (only TestA ran)
			order = []string{"TestA"}
		} else {
			// Seed 2+: the other full permutation
			order = []string{"TestB", "TestA"}
		}
		res := gotest.Result(s, map[string]domain.Status{"TestA": domain.StatusPass, "TestB": domain.StatusPass})
		res.Order = order
		return res, nil
	}}

	_, err := probe.Order{Seeds: probe.DefaultSeeds}.Probe(context.Background(), tgt, fake)
	if !errors.Is(err, domain.ErrNotReproduced) {
		t.Fatalf("expected ErrNotReproduced, got %v", err)
	}
	if fake.CallCount() != 3 {
		t.Fatalf("expected 3 runs (baseline + 2 seeds), got %d", fake.CallCount())
	}
}
