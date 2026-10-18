package generator

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ramsesyok/runnora-docgen/internal/model"
)

// executionReport は runnora の report.json (schemaVersion: 2) のうち、成績書に必要な項目。
type executionReport struct {
	SchemaVersion int    `json:"schemaVersion"`
	Runnora       string `json:"runnora"`
	StartedAt     string `json:"startedAt"`
	Suite         string `json:"suite"`
	Env           *struct {
		Name string `json:"name"`
	} `json:"env"`
	Results []executionResult `json:"results"`
	sha256  string
	path    string
}

type executionResult struct {
	ID     string          `json:"id"`
	Path   string          `json:"path"`
	Actual string          `json:"actual"`
	Passed bool            `json:"passed"`
	Steps  []executionStep `json:"steps"`
	Hooks  []struct {
		Phase string `json:"phase"`
		File  string `json:"file"`
		OK    bool   `json:"ok"`
	} `json:"hooks"`
}

type executionStep struct {
	Key    string `json:"key"`
	Result string `json:"result"`
}

var loopIteration = regexp.MustCompile(`\[\d+\]`)

func loadExecutionReport(path string) (*executionReport, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("実行レポートを読めません: %w", err)
	}
	var report executionReport
	if err := json.Unmarshal(data, &report); err != nil {
		return nil, fmt.Errorf("実行レポートの JSON が不正です: %w", err)
	}
	if report.SchemaVersion != 2 {
		return nil, fmt.Errorf("report.json の schemaVersion は 2 が必要です（取得値: %d）", report.SchemaVersion)
	}
	seenIDs := map[string]bool{}
	seenPaths := map[string]bool{}
	for _, result := range report.Results {
		if result.Path == "" {
			return nil, fmt.Errorf("report.json の results[].path が空です")
		}
		if result.ID != "" {
			if seenIDs[result.ID] {
				return nil, fmt.Errorf("report.json のシナリオ ID %q が重複しています", result.ID)
			}
			seenIDs[result.ID] = true
		}
		if seenPaths[normalizedPath(result.Path)] {
			return nil, fmt.Errorf("report.json の runbook パス %q が重複しています", result.Path)
		}
		seenPaths[normalizedPath(result.Path)] = true
	}
	sum := sha256.Sum256(data)
	report.sha256 = hex.EncodeToString(sum[:])
	report.path, err = filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	return &report, nil
}

func normalizedPath(path string) string {
	return filepath.ToSlash(filepath.Clean(filepath.FromSlash(path)))
}

func (r *executionReport) find(scenarioID, source, base string) (*executionResult, error) {
	if base == "" {
		base, _ = os.Getwd()
	}
	for i := range r.Results {
		result := &r.Results[i]
		reportedPath := result.Path
		if filepath.IsAbs(reportedPath) && base != "" {
			if relative, err := filepath.Rel(base, reportedPath); err == nil {
				reportedPath = relative
			}
		}
		if scenarioID != "" && result.ID == scenarioID {
			if normalizedPath(reportedPath) != normalizedPath(source) {
				return nil, fmt.Errorf("シナリオ %s の runbook が実行レポートと異なります: %s / %s", scenarioID, source, result.Path)
			}
			return result, nil
		}
		if scenarioID != "" && normalizedPath(reportedPath) == normalizedPath(source) && result.ID != scenarioID {
			return nil, fmt.Errorf("runbook %s のシナリオ ID が実行レポートと異なります: %s / %s", source, scenarioID, result.ID)
		}
		if scenarioID == "" && normalizedPath(reportedPath) == normalizedPath(source) {
			return result, nil
		}
	}
	return nil, nil
}

// stepGrades は表の1行ごとの良否と、空欄にした実行状態を返す。
func stepGrades(scenario *model.Scenario, run *executionResult) (map[string]string, []string, error) {
	grades := make(map[string]string, len(scenario.Steps))
	var statuses []string
	if run == nil || run.Actual == "skipped" {
		state := "結果なし"
		if run != nil {
			state = "skipped"
		}
		for _, step := range scenario.Steps {
			statuses = append(statuses, fmt.Sprintf("%s 手順 %d (%s): %s", scenario.Name, step.Number, step.Key, state))
		}
		return grades, statuses, nil
	}
	byKey := map[string][]executionStep{}
	for _, step := range run.Steps {
		byKey[step.Key] = append(byKey[step.Key], step)
	}
	for _, step := range scenario.Steps {
		found, ok := lastStep(byKey[step.Key])
		if !ok && step.Loop {
			for _, reported := range run.Steps {
				if loopIteration.ReplaceAllString(reported.Key, "") == step.Key {
					found, ok = reported, true
				}
			}
		}
		// include の loop.until は親の条件が成立しない場合、子ステップが成功していても失敗する。
		for _, loopKey := range step.LoopKeys {
			if owner, exists := lastStep(byKey[loopKey]); exists && owner.Result == "failure" && (!ok || found.Result == "success") {
				found, ok = executionStep{Key: step.Key, Result: "failure"}, true
			}
		}
		if !ok {
			statuses = append(statuses, fmt.Sprintf("%s 手順 %d (%s): 結果なし", scenario.Name, step.Number, step.Key))
			continue
		}
		switch found.Result {
		case "success":
			grades[step.Key] = "良"
		case "failure":
			grades[step.Key] = "否"
		case "skipped", "notRun":
			statuses = append(statuses, fmt.Sprintf("%s 手順 %d (%s): %s", scenario.Name, step.Number, step.Key, found.Result))
		default:
			return nil, nil, fmt.Errorf("シナリオ %s の手順 %s に不明な実行状態 %q があります", scenario.Name, step.Key, found.Result)
		}
	}
	return grades, statuses, nil
}

func lastStep(steps []executionStep) (executionStep, bool) {
	if len(steps) == 0 {
		return executionStep{}, false
	}
	return steps[len(steps)-1], true
}

func reportDisplayPath(base, path string) string {
	if base == "" {
		base, _ = os.Getwd()
	}
	rel, err := filepath.Rel(base, path)
	if err != nil {
		return filepath.ToSlash(path)
	}
	return strings.TrimPrefix(filepath.ToSlash(rel), "./")
}
