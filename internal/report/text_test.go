package report_test

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/emrelab/go-flake-hunter/internal/domain"
	"github.com/emrelab/go-flake-hunter/internal/report"
)

func sample() domain.Capsule {
	return domain.Capsule{
		ID:   "gfh_abc123456789",
		Kind: domain.KindOrder,
		Witness: domain.Witness{
			Kind:      domain.KindOrder,
			Victim:    domain.TestID{Package: "p", Name: "TestVictim"},
			Polluters: []domain.TestID{{Package: "p", Name: "TestPolluter"}},
			Runs:      5,
		},
		Observation: domain.Observation{Runs: 5, Failures: 5, Deterministic: true},
		Evidence: domain.Evidence{
			GlobalsWritten: map[string][]string{"TestPolluter": {"seeded"}},
			GlobalsRead:    map[string][]string{"TestVictim": {"seeded"}},
		},
		Repro:  domain.Repro{Command: "go test . -run '^TestFlakeWitness$'"},
		Status: domain.StatusConfirmed,
	}
}

func TestTextReportsTheEssentials(t *testing.T) {
	var b bytes.Buffer
	if err := report.Text(&b, sample()); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{"flake found", "TestVictim", "order", "TestPolluter", "5/5", "gfh_abc123456789"} {
		if !strings.Contains(out, want) {
			t.Errorf("%q missing from output: %s", want, out)
		}
	}
}

// The single most valuable line: the writer/reader intersection. But it is never called a "root cause".
func TestTextShowsSharedGlobalAsObservation(t *testing.T) {
	var b bytes.Buffer
	if err := report.Text(&b, sample()); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	if !strings.Contains(out, "TestPolluter writes, TestVictim reads: seeded") {
		t.Errorf("intersection line missing: %s", out)
	}
	if strings.Contains(strings.ToLower(out), "root cause") {
		t.Error("must not claim a root cause")
	}
}

func TestTextWithoutEvidence(t *testing.T) {
	c := sample()
	c.Evidence = domain.Evidence{}
	var b bytes.Buffer
	if err := report.Text(&b, c); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(b.String(), "evidence") {
		t.Error("the line must not print without evidence")
	}
}

// An empty result must be honest: what was tried, why nothing came of it.
func TestNoFlakeListsAttempts(t *testing.T) {
	var b bytes.Buffer
	err := report.NoFlake(&b, []report.AttemptInfo{
		{Probe: "repeat", Err: domain.ErrNotReproduced, NotReproduced: true},
		{Probe: "order", Err: errors.New("time ran out")},
	})
	if err != nil {
		t.Fatal(err)
	}
	out := b.String()
	if !strings.Contains(out, "flake not found") {
		t.Errorf("title missing: %s", out)
	}
	if !strings.Contains(out, "repeat") || !strings.Contains(out, "order") {
		t.Errorf("tried probes must be listed: %s", out)
	}
	if !strings.Contains(out, "time ran out") {
		t.Errorf("the error reason must be shown: %s", out)
	}
}

func TestJSONIsMachineReadable(t *testing.T) {
	var b bytes.Buffer
	if err := report.JSON(&b, sample()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), `"schema"`) {
		t.Errorf("JSON fields missing: %s", b.String())
	}
}

// A capsule is shareable data; a control sequence in a note must not reach the
// reader's terminal through the text or markdown renderer.
func TestReportsStripTerminalControls(t *testing.T) {
	c := sample()
	c.Note = "look\x1b]52;c;cGF3bmVk\x07here"
	c.Repro.Command = "echo \x1b[31mred\x1b[0m"
	c.Witness.Victim.Package = "p\x1b[2J"
	for name, render := range map[string]func(*bytes.Buffer) error{
		"text":     func(b *bytes.Buffer) error { return report.Text(b, c) },
		"markdown": func(b *bytes.Buffer) error { return report.Markdown(b, c) },
	} {
		var b bytes.Buffer
		if err := render(&b); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if strings.ContainsRune(b.String(), 0x1b) {
			t.Errorf("%s output carries a terminal escape: %q", name, b.String())
		}
	}
}
