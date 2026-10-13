package generator

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ramsesyok/runnora-docgen/internal/model"
	"github.com/ramsesyok/runnora-docgen/internal/project"
	"github.com/ramsesyok/runnora-docgen/internal/render"
	"github.com/ramsesyok/runnora-docgen/internal/source"
)

type Options struct {
	RunbookPaths   []string
	OutputDir      string
	ConfigPath     string
	BeforeSQL      []string
	AfterSQL       []string
	ProtoPaths     []string
	BaseDir        string
	Force          bool
	ShowJSONDetail bool
	// ProjectPath / Env / Suite は新形式 (runnora.yaml) の指定。
	// ConfigPath がなければ runnora.yaml を現在のディレクトリから親へ探して使う。
	// Suite を指定すると、runbook はスイートの条件で選ぶ (RunbookPaths は指定しない)。
	ProjectPath string
	Env         string
	Suite       string
}

// input は原稿にする runbook 1 つ分と、その読み込み設定。
type input struct {
	path string
	opts source.Options
}

// planInputs は原稿にする runbook と、runbook ごとの前後処理を決める。
func planInputs(opts Options) ([]input, []string, error) {
	base := source.Options{
		ConfigPath: opts.ConfigPath,
		BeforeSQL:  opts.BeforeSQL,
		AfterSQL:   opts.AfterSQL,
		ProtoPaths: opts.ProtoPaths,
		BaseDir:    opts.BaseDir,
	}
	projectPath := opts.ProjectPath
	if opts.ConfigPath != "" {
		if projectPath != "" || opts.Env != "" || opts.Suite != "" {
			return nil, nil, fmt.Errorf("--config (旧形式) と --project / --env / --suite は同時に指定できません")
		}
	} else if projectPath == "" {
		found, err := project.Find(".")
		if err != nil {
			return nil, nil, err
		}
		projectPath = found
	}
	if projectPath == "" {
		if opts.Env != "" || opts.Suite != "" {
			return nil, nil, fmt.Errorf("--env / --suite を使うには runnora.yaml が必要です (--project で指定するか、プロジェクトのディレクトリで実行してください)")
		}
		if len(opts.RunbookPaths) == 0 {
			return nil, nil, fmt.Errorf("runbook を1つ以上指定してください")
		}
		inputs := make([]input, 0, len(opts.RunbookPaths))
		for _, path := range opts.RunbookPaths {
			inputs = append(inputs, input{path: path, opts: base})
		}
		return inputs, nil, nil
	}

	p, err := project.Load(projectPath)
	if err != nil {
		return nil, nil, err
	}
	switch {
	case opts.Suite != "" && len(opts.RunbookPaths) > 0:
		return nil, nil, fmt.Errorf("--suite と runbook の引数は同時に指定できません")
	case opts.Suite == "" && len(opts.RunbookPaths) == 0:
		return nil, nil, fmt.Errorf("runbook を指定するか --suite を指定してください")
	}
	envName, err := p.SelectEnv(opts.Env, opts.Suite)
	if err != nil {
		return nil, nil, err
	}
	hooks, err := p.Hooks(envName, opts.Suite)
	if err != nil {
		return nil, nil, err
	}
	paths := opts.RunbookPaths
	if opts.Suite != "" {
		sel, err := p.Suite(opts.Suite)
		if err != nil {
			return nil, nil, err
		}
		selected, err := p.Select(sel)
		if err != nil {
			return nil, nil, err
		}
		if len(selected) == 0 {
			return nil, nil, fmt.Errorf("スイート %q の条件に合う runbook がありません (runnora: ブロックを持つ runbook だけが対象です)", opts.Suite)
		}
		paths = nil
		for _, s := range selected {
			paths = append(paths, s.Path)
		}
	}
	if base.BaseDir == "" {
		base.BaseDir = p.Root
	}
	base.ProjectPath = p.Path

	var (
		inputs   []input
		warnings []string
	)
	for _, path := range paths {
		block, _, err := project.ReadBlock(path)
		if err != nil {
			return nil, nil, err
		}
		if !block.AllowedIn(envName) {
			warnings = append(warnings, fmt.Sprintf("%s: 環境 %s では実行しない runbook (runnora.envs) なので原稿を作りません", path, envName))
			continue
		}
		o := base
		// runnora run と同じ順: 環境 → スイート → runbook の before、runbook → スイート → 環境の after
		o.ProjectBefore = append([]string{}, hooks.Before...)
		o.ProjectAfter = nil
		if block != nil {
			for _, f := range block.Before {
				o.ProjectBefore = append(o.ProjectBefore, p.Abs(f))
			}
			for _, f := range block.After {
				o.ProjectAfter = append(o.ProjectAfter, p.Abs(f))
			}
		}
		o.ProjectAfter = append(o.ProjectAfter, hooks.After...)
		inputs = append(inputs, input{path: path, opts: o})
	}
	return inputs, warnings, nil
}

type Result struct {
	Files    []string
	Warnings []string
}

type plannedFile struct {
	path    string
	content []byte
}

type manifest struct {
	Generator string `json:"generator"`
	Scenario  string `json:"scenario"`
	// ScenarioID は runbook の runnora: ブロックの id (runnora の report.json と同じ)。ブロックがなければ省く。
	ScenarioID string `json:"scenarioId,omitempty"`
	Source     string `json:"source"`
	SHA256     string `json:"sha256"`
	// Steps は手順書の手順番号と、runnora のステップのキーの対応。
	Steps  []manifestStep `json:"steps"`
	Files  []string       `json:"files"`
	Inputs any            `json:"inputs"`
}

// manifestStep は手順 1 行分。loop のステップは手順書では 1 行なので、キーに [n] を付けず loop: true にする
// (runnora の report.json では key[0]、key[1] … と回ごとに分かれる)。
type manifestStep struct {
	Number int    `json:"number"`
	Key    string `json:"key"`
	Loop   bool   `json:"loop,omitempty"`
}

func Generate(ctx context.Context, opts Options) (*Result, error) {
	inputs, planWarnings, err := planInputs(opts)
	if err != nil {
		return nil, err
	}
	if opts.OutputDir == "" {
		opts.OutputDir = "generated"
	}
	outputDir, err := filepath.Abs(opts.OutputDir)
	if err != nil {
		return nil, fmt.Errorf("output path: %w", err)
	}

	var plan []plannedFile
	result := &Result{Warnings: planWarnings}
	usedIDs := map[string]string{}
	usedFolders := map[string]string{}
	for _, in := range inputs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		scenario, warnings, err := source.Load(in.path, in.opts)
		if err != nil {
			return nil, err
		}
		block, _, err := project.ReadBlock(in.path)
		if err != nil {
			return nil, err
		}
		scenarioID := ""
		// Use the explicit runnora ID for labels and a filesystem-safe folder name.
		folderID := scenario.ID
		if block != nil {
			scenarioID = block.ID
			if strings.TrimSpace(scenarioID) == "" {
				return nil, fmt.Errorf("%s: runnora.id が空です", in.path)
			}
			scenario.ID = scenarioID
			folderID = source.OutputID(scenarioID)
		}
		makePathsPortable(scenario, in.opts.BaseDir)
		if previous, exists := usedIDs[scenario.ID]; exists {
			return nil, fmt.Errorf("出力 ID %q が重複します: %s, %s", scenario.ID, previous, scenario.SourcePath)
		}
		usedIDs[scenario.ID] = scenario.SourcePath
		if previous, exists := usedFolders[folderID]; exists {
			return nil, fmt.Errorf("同じ生成コマンド内で出力フォルダ名 %q が重複します: %s, %s", folderID, previous, scenario.SourcePath)
		}
		usedFolders[folderID] = scenario.SourcePath
		result.Warnings = append(result.Warnings, warnings...)

		documents := render.ScenarioDocuments(scenario, opts.ShowJSONDetail)
		scenarioDir := filepath.Join(outputDir, folderID)
		manifestFiles := make([]string, 0, len(documents))
		for _, document := range documents {
			path := filepath.Join(scenarioDir, document.Name)
			plan = append(plan, plannedFile{path: path, content: document.Content})
			manifestFiles = append(manifestFiles, document.Name)
		}
		sort.Strings(manifestFiles)
		steps := make([]manifestStep, 0, len(scenario.Steps))
		for _, s := range scenario.Steps {
			steps = append(steps, manifestStep{Number: s.Number, Key: s.Key, Loop: s.Loop})
		}
		manifestData, err := json.MarshalIndent(manifest{
			Generator:  "runnora-docgen",
			Scenario:   scenario.Name,
			ScenarioID: scenarioID,
			Source:     scenario.SourcePath,
			SHA256:     scenario.SourceHash,
			Steps:      steps,
			Files:      manifestFiles,
			Inputs:     scenario.Sources,
		}, "", "  ")
		if err != nil {
			return nil, err
		}
		manifestData = append(manifestData, '\n')
		plan = append(plan, plannedFile{path: filepath.Join(scenarioDir, "manifest.json"), content: manifestData})
	}

	for _, file := range plan {
		if _, err := os.Stat(file.path); err == nil && !opts.Force {
			return nil, fmt.Errorf("生成先が既に存在します（上書きするには --force）: %s", file.path)
		} else if err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("生成先を確認できません: %s: %w", file.path, err)
		}
	}
	for _, file := range plan {
		if err := os.MkdirAll(filepath.Dir(file.path), 0o755); err != nil {
			return nil, fmt.Errorf("出力ディレクトリを作れません: %w", err)
		}
		if err := os.WriteFile(file.path, file.content, 0o644); err != nil {
			return nil, fmt.Errorf("原稿を書き込めません: %s: %w", file.path, err)
		}
		result.Files = append(result.Files, file.path)
	}
	return result, nil
}

func makePathsPortable(scenario *model.Scenario, baseDir string) {
	base := baseDir
	if strings.TrimSpace(base) == "" {
		base, _ = os.Getwd()
	}
	if absolute, err := filepath.Abs(base); err == nil {
		base = absolute
	}

	scenario.SourcePath = portablePath(base, scenario.SourcePath)
	for index := range scenario.Steps {
		scenario.Steps[index].SourcePath = portablePath(base, scenario.Steps[index].SourcePath)
		for i := range scenario.Steps[index].RequestJSONRefs {
			scenario.Steps[index].RequestJSONRefs[i].Path = portablePath(base, scenario.Steps[index].RequestJSONRefs[i].Path)
		}
		for i := range scenario.Steps[index].ResponseBodyJSONRefs {
			scenario.Steps[index].ResponseBodyJSONRefs[i].Path = portablePath(base, scenario.Steps[index].ResponseBodyJSONRefs[i].Path)
		}
	}
	for index := range scenario.Cases {
		scenario.Cases[index].SourcePath = portablePath(base, scenario.Cases[index].SourcePath)
	}
	for index := range scenario.BeforeHooks {
		scenario.BeforeHooks[index].Path = portablePath(base, scenario.BeforeHooks[index].Path)
	}
	for index := range scenario.AfterHooks {
		scenario.AfterHooks[index].Path = portablePath(base, scenario.AfterHooks[index].Path)
	}
	for index := range scenario.Sources {
		scenario.Sources[index].Path = portablePath(base, scenario.Sources[index].Path)
	}
}

func portablePath(base, path string) string {
	if strings.TrimSpace(path) == "" {
		return path
	}
	relative, err := filepath.Rel(base, path)
	if err != nil {
		return filepath.ToSlash(filepath.Base(path))
	}
	return filepath.ToSlash(relative)
}
