package source

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ramsesyok/runnora-docgen/internal/model"
)

func TestLoadExecCommandAndTest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "exec.yml")
	content := "steps:\n  compare:\n    exec:\n      command: runnora-diff expected.json -\n    test: current.exit_code == 0\n"
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	scenario, warnings, err := Load(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 || len(scenario.Steps) != 1 {
		t.Fatalf("warnings=%v steps=%v", warnings, scenario.Steps)
	}
	step := scenario.Steps[0]
	if step.Kind != model.StepExec || step.ExecCommand != "runnora-diff expected.json -" || step.Test != "current.exit_code == 0" {
		t.Fatalf("exec step = %#v", step)
	}
}
