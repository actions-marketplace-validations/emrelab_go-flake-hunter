package clihunt

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emrelab/go-flake-hunter/internal/clitest"
)

func TestAuditC2_PackageFailureSurvivesPassingTests(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	dir := clitest.FixtureDir(t, "clean")
	src := `package clean
import ("flag"; "os"; "testing")
func TestMain(m *testing.M) {
	flag.Parse()
	code := m.Run()
	if flag.Lookup("test.list").Value.String() == "" && code == 0 { code = 7 }
	os.Exit(code)
}
`
	if err := os.WriteFile(filepath.Join(dir, "main_test.go"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, stderr := clitest.Run("hunt", dir, "--format", "json")
	if code != 7 || !strings.Contains(stderr, "package failed") || !strings.Contains(out, "package-failed") {
		t.Fatalf("hunt exit=%d stdout=%s stderr=%s", code, out, stderr)
	}
	// Nothing was judged, so the text summary must not claim a clean scan.
	code, out, stderr = clitest.Run("hunt", dir)
	if code != 7 || !strings.Contains(out, "no package could be scanned") || !strings.Contains(out, "1 skipped") {
		t.Errorf("text hunt exit=%d stdout=%s stderr=%s", code, out, stderr)
	}
}

func TestAuditC4_AlwaysFailingTestIsBroken(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	dir := clitest.FixtureDir(t, "clean")
	src := "package clean\nimport \"testing\"\nfunc TestBroken(t *testing.T) { t.Fatal(\"broken\") }\n"
	if err := os.WriteFile(filepath.Join(dir, "broken_test.go"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, stderr := clitest.Run("hunt", dir)
	if code != 0 || !strings.Contains(out, "broken") {
		t.Errorf("hunt exit=%d stdout=%s stderr=%s", code, out, stderr)
	}
	code, out, stderr = clitest.Run("hunt", dir, "--fail-on-flake", "--format", "json")
	var report struct {
		Flakes   []json.RawMessage
		Packages []struct{ Status string }
	}
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatal(err)
	}
	if code != 1 || len(report.Flakes) != 0 || len(report.Packages) != 1 || report.Packages[0].Status != "broken" {
		t.Errorf("gated hunt exit=%d stdout=%s stderr=%s", code, out, stderr)
	}
}

func TestAuditC5_PartialIsInconclusiveOnEverySurface(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	dir := clitest.FixtureDir(t, "order-basic")
	for _, format := range []string{"text", "markdown", "sarif", "json"} {
		t.Run(format, func(t *testing.T) {
			store := t.TempDir()
			code, out, stderr := clitest.Run("hunt", dir, "--budget", "1", "--format", format, "--capsule-dir", store)
			if code != 4 || !strings.Contains(out, "inconclusive") || strings.Contains(out, "is flaky") || strings.Contains(out, "flake found") {
				t.Errorf("hunt exit=%d stdout=%s stderr=%s", code, out, stderr)
			}
			if format == "sarif" && (!strings.Contains(out, `"ruleId": "go-flake-hunter/inconclusive"`) || !strings.Contains(out, `"level": "note"`)) {
				t.Errorf("incorrect SARIF result: %s", out)
			}
			if format == "text" {
				code, out, stderr = clitest.Run("explain", "--capsule-dir", store)
				if code != 0 || !strings.Contains(out, "inconclusive") || !strings.Contains(out, "fixture/orderbasic") {
					t.Errorf("explain exit=%d stdout=%s stderr=%s", code, out, stderr)
				}
			}
		})
	}
}

func TestAuditC11_SickPackageDoesNotHideTheRest(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	dir := clitest.FixtureDir(t, "clean")
	sub := filepath.Join(dir, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	// A package that exits non-zero without any test reporting the failure.
	// The scan used to stop here, so the user saw the failure and nothing about
	// the package that was fine.
	src := `package sub
import ("flag"; "os"; "testing")
func TestMain(m *testing.M) {
	flag.Parse()
	code := m.Run()
	if flag.Lookup("test.list").Value.String() == "" && code == 0 { code = 7 }
	os.Exit(code)
}
func TestSub(t *testing.T) {}
`
	if err := os.WriteFile(filepath.Join(sub, "sub_test.go"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, stderr := clitest.Run("hunt", dir, sub)
	if code != 7 {
		t.Fatalf("exit=%d stdout=%s stderr=%s", code, out, stderr)
	}
	if !strings.Contains(out, "1 of 2 packages (1 skipped)") || !strings.Contains(out, "sub: package failed") {
		t.Fatalf("skipped package not reported: %s", out)
	}
	if !strings.Contains(out, "flake not found:") {
		t.Fatalf("the healthy package's verdict was lost: %s", out)
	}
}

func TestAuditC9_LogsToStderrAndRejectsFormatBeforeRuns(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	dir := clitest.FixtureDir(t, "repeat-accumulate")
	store := t.TempDir()
	code, out, stderr := clitest.Run("hunt", dir, "--format", "invalid", "--capsule-dir", store)
	entries, err := os.ReadDir(store)
	if code != 2 || len(entries) != 0 || err != nil || out != "" {
		t.Errorf("invalid format exit=%d artifacts=%v err=%v stdout=%s stderr=%s", code, entries, err, out, stderr)
	}
	code, out, stderr = clitest.Run("hunt", dir, "--format", "json", "-v", "--capsule-dir", store)
	if code != 0 || !json.Valid([]byte(out)) || !strings.Contains(stderr, "written:") {
		t.Errorf("verbose JSON exit=%d stdout=%s stderr=%s", code, out, stderr)
	}
}
