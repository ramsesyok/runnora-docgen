package generator

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/ramsesyok/runnora-docgen/internal/model"
)

func TestGenerateResultsKeepsScenarioTableAndReportsUnexecutedSteps(t *testing.T) {
	dir := t.TempDir()
	writeProjectFile(t, filepath.Join(dir, "runnora.yaml"), `version: 2
project: {name: result-test}
defaults: {env: unit}
environments:
  unit: {}
suites:
  scenarios:
    env: unit
    select: {paths: [runbooks/*.yml]}
`)
	writeProjectFile(t, filepath.Join(dir, "runbooks", "sample.yml"), `desc: 試験 A
runnora: {id: TEST-A, expect: fail}
steps:
  passed: {test: true}
  failed: {test: false}
  skipped: {if: "false", test: true}
  later: {test: true}
`)
	reportPath := filepath.Join(dir, "reports", "run", "report.json")
	writeTestReport(t, reportPath, executionReport{
		SchemaVersion: 2, Runnora: "v1", StartedAt: "2026-10-18T12:00:00Z", Suite: "scenarios",
		Env: &struct {
			Name string `json:"name"`
		}{Name: "unit"},
		Results: []executionResult{{
			ID: "TEST-A", Path: "runbooks/sample.yml", Actual: "fail", Passed: true,
			Steps: []executionStep{
				{Key: "passed", Result: "success"}, {Key: "failed", Result: "failure"},
				{Key: "skipped", Result: "skipped"}, {Key: "later", Result: "notRun"},
			},
		}},
	})
	procedureDir := filepath.Join(dir, "procedure")
	resultDir := filepath.Join(dir, "results")
	project := filepath.Join(dir, "runnora.yaml")
	if _, err := Generate(context.Background(), Options{ProjectPath: project, Suite: "scenarios", OutputDir: procedureDir}); err != nil {
		t.Fatal(err)
	}
	generated, err := Generate(context.Background(), Options{ProjectPath: project, ReportPath: reportPath, OutputDir: resultDir})
	if err != nil {
		t.Fatal(err)
	}
	procedure := readGenerated(t, procedureDir, "test-a", "scenario.qmd")
	result := readGenerated(t, resultDir, "test-a", "scenario.qmd")
	goodCell := regexp.MustCompile(`\| 良 +\|`)
	badCell := regexp.MustCompile(`\| 否 +\|`)
	if len(goodCell.FindAllString(result, -1)) != 1 || len(badCell.FindAllString(result, -1)) != 1 {
		t.Fatalf("結果が表の良否欄に入っていません:\n%s", result)
	}
	if goodCell.MatchString(procedure) || badCell.MatchString(procedure) {
		t.Fatalf("手順書の良否欄が埋まっています:\n%s", procedure)
	}
	for _, want := range []string{"手順番号", "試験手順", "期待結果", "結果", "ステータス・期待値", "良否"} {
		if !strings.Contains(result, want) || !strings.Contains(procedure, want) {
			t.Fatalf("表の構造が一致しません: %s", want)
		}
	}
	if len(generated.Statuses) != 2 || !strings.Contains(generated.Statuses[0], "skipped") || !strings.Contains(generated.Statuses[1], "notRun") {
		t.Fatalf("実行されなかった手順の CLI 状態 = %v", generated.Statuses)
	}
	var manifest struct {
		Execution *manifestExecution `json:"execution"`
	}
	if err := json.Unmarshal([]byte(readGenerated(t, resultDir, "test-a", "manifest.json")), &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Execution == nil || len(manifest.Execution.SHA256) != 64 || manifest.Execution.Actual != "fail" || !manifest.Execution.Passed {
		t.Fatalf("実行情報を保存していません: %+v", manifest.Execution)
	}
}

func TestStepGradesUseCurrentLoopOutcome(t *testing.T) {
	for _, tt := range []struct {
		name  string
		steps []executionStep
		want  string
	}{
		{
			name: "count loop uses last reported iteration",
			steps: []executionStep{
				{Key: "run", Result: "success"}, {Key: "run[0].call", Result: "failure"}, {Key: "run[1].call", Result: "success"},
			},
			want: "良",
		},
		{
			name: "until failure overrides a successful child",
			steps: []executionStep{
				{Key: "run", Result: "failure"}, {Key: "run.call", Result: "success"},
			},
			want: "否",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			scenario := &model.Scenario{Name: "loop", Steps: []model.Step{{Number: 1, Key: "run.call", Loop: true, LoopKeys: []string{"run"}}}}
			grades, statuses, err := stepGrades(scenario, &executionResult{Steps: tt.steps})
			if err != nil || len(statuses) != 0 || grades["run.call"] != tt.want {
				t.Fatalf("grades=%v statuses=%v err=%v", grades, statuses, err)
			}
		})
	}
}

func TestGenerateResultsRejectsMismatchedRunbook(t *testing.T) {
	dir := t.TempDir()
	writeProjectFile(t, filepath.Join(dir, "runnora.yaml"), "version: 2\nproject: {name: mismatch}\n")
	writeProjectFile(t, filepath.Join(dir, "runbooks", "sample.yml"), "runnora: {id: TEST-A}\nsteps:\n  check: {test: true}\n")
	reportPath := filepath.Join(dir, "report.json")
	writeTestReport(t, reportPath, executionReport{SchemaVersion: 2, Results: []executionResult{{ID: "TEST-A", Path: "runbooks/other.yml", Steps: []executionStep{{Key: "check", Result: "success"}}}}})
	_, err := Generate(context.Background(), Options{
		ProjectPath: filepath.Join(dir, "runnora.yaml"), RunbookPaths: []string{filepath.Join(dir, "runbooks", "sample.yml")},
		ReportPath: reportPath, OutputDir: filepath.Join(dir, "results"),
	})
	if err == nil || !strings.Contains(err.Error(), "runbook が実行レポートと異なります") {
		t.Fatalf("不一致を検出できません: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "results")); !os.IsNotExist(err) {
		t.Fatal("不一致の成績書を出力しました")
	}
}

func TestGenerateResultsKeepsSkippedRunbookBlank(t *testing.T) {
	dir := t.TempDir()
	writeProjectFile(t, filepath.Join(dir, "runnora.yaml"), `version: 2
project: {name: skipped-test}
environments: {unit: {}, other: {}}
suites:
  scenarios:
    env: unit
    select: {paths: [runbooks/*.yml]}
`)
	writeProjectFile(t, filepath.Join(dir, "runbooks", "sample.yml"), `desc: 対象外
runnora: {id: SKIP-A, envs: [other]}
steps: {check: {test: true}}
`)
	reportPath := filepath.Join(dir, "report.json")
	writeTestReport(t, reportPath, executionReport{
		SchemaVersion: 2, Suite: "scenarios", Env: &struct {
			Name string `json:"name"`
		}{Name: "unit"},
		Results: []executionResult{{ID: "SKIP-A", Path: "runbooks/sample.yml", Actual: "skipped", Passed: true}},
	})
	out := filepath.Join(dir, "results")
	got, err := Generate(context.Background(), Options{ProjectPath: filepath.Join(dir, "runnora.yaml"), ReportPath: reportPath, OutputDir: out})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Statuses) != 1 || !strings.Contains(got.Statuses[0], "skipped") {
		t.Fatalf("対象外シナリオの状態 = %v", got.Statuses)
	}
	content := readGenerated(t, out, "skip-a", "scenario.qmd")
	if regexp.MustCompile(`\| [良否] +\|`).MatchString(content) {
		t.Fatalf("対象外シナリオに良否があります:\n%s", content)
	}
}

func writeTestReport(t *testing.T, path string, report executionReport) {
	t.Helper()
	data, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	writeProjectFile(t, path, string(data))
}

func readGenerated(t *testing.T, dir, folder, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, folder, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
