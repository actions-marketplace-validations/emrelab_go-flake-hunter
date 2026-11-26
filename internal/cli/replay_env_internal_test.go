package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emrelab/go-flake-hunter/internal/domain"
	"github.com/emrelab/go-flake-hunter/internal/policy"
)

// The printed env reproduce command carries a redaction placeholder because the
// value never enters a capsule. Running with the placeholder is not a run: it
// used to report "never reproduced — stale capsule", a false diagnosis.
func TestReplayRejectsRedactedEnvPlaceholder(t *testing.T) {
	dir := t.TempDir()
	id := "gfh_abcdef123456"
	c := domain.Capsule{
		Schema: domain.SchemaVersion,
		ID:     id,
		Kind:   domain.KindEnv,
		Status: domain.StatusConfirmed,
		Target: domain.Target{Package: "example.com/p", Victim: "TestEnv"},
		Witness: domain.Witness{
			Kind:    domain.KindEnv,
			Victim:  domain.TestID{Package: "example.com/p", Name: "TestEnv"},
			EnvDiff: []string{"GFH_MODE"},
			Runs:    3,
		},
	}
	data, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, id+".json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer
	err = replayCmd(context.Background(),
		[]string{path, "--env", "GFH_MODE=" + policy.RedactionPlaceholder},
		IO{Stdout: &out, Stderr: &errOut})
	if !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("err = %v, want ErrUsage", err)
	}
	if !strings.Contains(err.Error(), "placeholder") {
		t.Errorf("error should name the placeholder problem: %v", err)
	}
}
