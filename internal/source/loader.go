package source

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/ramsesyok/runnora-docgen/internal/model"
	"gopkg.in/yaml.v3"
)

type Options struct {
	ConfigPath string
	BeforeSQL  []string
	AfterSQL   []string
	ProtoPaths []string
	BaseDir    string
	// TraceHTTPStep reports only variable paths (never values) for one HTTP step.
	TraceHTTPStep int
	// ProjectPath は新形式の runnora.yaml (出典として記録する)。
	// ProjectBefore / ProjectAfter は runnora.yaml の環境・スイートと runbook の runnora: ブロックから
	// 決めた前後処理 (絶対パス、runnora run の実行順)。BeforeSQL / AfterSQL はその内側に加える。
	ProjectPath   string
	ProjectBefore []string
	ProjectAfter  []string
}

type loader struct {
	opts     Options
	stack    map[string]bool
	warnings []string
	httpRefs map[*model.HTTPRequest]httpVariableRefs
	// includeSources は include したファイル (template と vars の json://) の出典。
	// 取り込んだ runbook の Sources に加える。
	includeSources []model.SourceRef
}

type runnerInfo struct {
	name     string
	kind     model.StepKind
	endpoint string
	protos   []string
}

type httpVariableRefs struct {
	path    []string
	query   []string
	headers []string
}

var statusPattern = regexp.MustCompile(`(?:current|steps\.[A-Za-z0-9_-]+)\.res\.status\s*==\s*([^\s&|)]+)`)

func Load(path string, opts Options) (*model.Scenario, []string, error) {
	l := &loader{opts: opts, stack: map[string]bool{}}
	if opts.TraceHTTPStep > 0 {
		l.httpRefs = make(map[*model.HTTPRequest]httpVariableRefs)
	}
	scenario, err := l.loadRunbook(path)
	if err != nil {
		return nil, l.warnings, err
	}
	if opts.TraceHTTPStep > 0 {
		l.traceHTTPStep(scenario, opts.TraceHTTPStep)
	}
	before, after, hookWarnings, err := loadHooks(opts)
	l.warnings = append(l.warnings, hookWarnings...)
	if err != nil {
		return nil, l.warnings, err
	}
	scenario.BeforeHooks = before
	scenario.AfterHooks = after
	if opts.ConfigPath != "" {
		if ref, refErr := sourceRef("config", resolveWithBase(opts.BaseDir, opts.ConfigPath)); refErr == nil {
			addSource(scenario, ref)
		}
	}
	if opts.ProjectPath != "" {
		if ref, refErr := sourceRef("project", opts.ProjectPath); refErr == nil {
			addSource(scenario, ref)
		}
	}
	for _, asset := range append(append([]model.Asset{}, before...), after...) {
		addSource(scenario, model.SourceRef{Kind: asset.Origin + "-sql", Path: asset.Path, SHA256: asset.SHA256})
	}
	return scenario, l.warnings, nil
}

func (l *loader) traceHTTPStep(scenario *model.Scenario, number int) {
	for _, step := range scenario.Steps {
		if step.Number != number {
			continue
		}
		if step.HTTP == nil {
			l.warnings = append(l.warnings, fmt.Sprintf("HTTP 手順 %d は HTTP 呼び出しではありません (%s)", number, step.Key))
			return
		}
		raw := l.httpRefs[step.HTTP]
		l.warnings = append(l.warnings, fmt.Sprintf("HTTP 手順 %d (%s、%s:%d) の変数参照。値は表示しません", number, step.Key, step.SourcePath, step.SourceLine))
		l.warnings = append(l.warnings, fmt.Sprintf("  URL: 読込時=%v、生成時に残る参照=%v", raw.path, varPaths(step.HTTP.Path)))
		l.warnings = append(l.warnings, fmt.Sprintf("  Query: 読込時=%v、生成時に残る参照=%v", raw.query, varPaths(step.HTTP.Query)))
		l.warnings = append(l.warnings, fmt.Sprintf("  Headers: 読込時=%v、生成時に残る参照=%v", raw.headers, varPaths(step.HTTP.Headers)))
		return
	}
	l.warnings = append(l.warnings, fmt.Sprintf("HTTP 手順 %d はありません", number))
}

func (l *loader) loadRunbook(path string) (*model.Scenario, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("runbook path: %w", err)
	}
	if l.stack[abs] {
		return nil, fmt.Errorf("include の循環参照を検出しました: %s", abs)
	}
	l.stack[abs] = true
	defer delete(l.stack, abs)

	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, fmt.Errorf("runbook を読めません: %s: %w", abs, err)
	}
	root, err := parseYAML(data)
	if err != nil {
		return nil, fmt.Errorf("runbook YAML が不正です: %s: %w", abs, err)
	}

	runners := parseRunners(root, filepath.Dir(abs))
	protoPaths := make([]string, 0, len(l.opts.ProtoPaths))
	for _, path := range l.opts.ProtoPaths {
		protoPaths = append(protoPaths, resolveWithBase(l.opts.BaseDir, path))
	}
	for _, runner := range runners {
		protoPaths = append(protoPaths, runner.protos...)
	}
	protoTypes, protoWarnings := loadProtoIndex(protoPaths)
	l.warnings = append(l.warnings, protoWarnings...)

	scenario := &model.Scenario{
		ID:         slug(strings.TrimSuffix(filepath.Base(abs), filepath.Ext(abs))),
		Name:       firstNonEmpty(scalar(mappingValue(root, "desc")), filepath.Base(abs)),
		SourcePath: abs,
		SourceHash: fileHash(data),
		Sources:    []model.SourceRef{{Kind: "runbook", Path: abs, SHA256: fileHash(data)}},
	}
	for _, protoPath := range protoPaths {
		if ref, refErr := sourceRef("proto", protoPath); refErr == nil {
			addSource(scenario, ref)
		}
	}

	caseRefs := caseReferences(root)
	includeKey, includePath, includeLoop := suiteInclude(root)
	if len(caseRefs) > 0 && includePath != "" {
		childPath := resolveReference(filepath.Dir(abs), includePath)
		child, childErr := l.loadRunbook(childPath)
		if childErr != nil {
			return nil, childErr
		}
		scenario.Steps = child.Steps
		for _, ref := range child.Sources {
			addSource(scenario, ref)
		}
		for i := range scenario.Steps {
			scenario.Steps[i].Number = i + 1
			// runnora では suite のステップ (include) を通して呼ぶので、キーはそのステップとつなぐ
			scenario.Steps[i].Key = includeKey + "." + scenario.Steps[i].Key
			scenario.Steps[i].Loop = scenario.Steps[i].Loop || includeLoop
			for j := range scenario.Steps[i].LoopKeys {
				scenario.Steps[i].LoopKeys[j] = includeKey + "." + scenario.Steps[i].LoopKeys[j]
			}
			if includeLoop {
				scenario.Steps[i].LoopKeys = append([]string{includeKey}, scenario.Steps[i].LoopKeys...)
			}
		}
		for _, ref := range caseRefs {
			caseValue, caseErr := loadCase(filepath.Dir(abs), ref)
			if caseErr != nil {
				return nil, caseErr
			}
			scenario.Cases = append(scenario.Cases, caseValue)
			addSource(scenario, model.SourceRef{Kind: "case", Path: caseValue.SourcePath, SHA256: caseValue.SourceHash})
		}
		resolveStatuses(scenario)
		resolveJSONFiles(scenario, nil)
		return scenario, nil
	}

	sourceStart := len(l.includeSources)
	steps, err := l.parseSteps(root, abs, runners, protoTypes)
	if err != nil {
		return nil, err
	}
	scenario.Steps = steps
	for _, ref := range l.includeSources[sourceStart:] {
		addSource(scenario, ref)
	}
	l.includeSources = l.includeSources[:sourceStart]
	if len(scenario.Cases) == 0 {
		scenario.Cases = []model.Case{{ID: "default", Name: "default", SourcePath: abs, Data: map[string]any{}}}
	}
	resolveStatuses(scenario)
	resolveJSONFiles(scenario, decodeAny(mappingValue(root, "vars")))
	if err := loadJSONDetails(scenario, decodeAny(mappingValue(root, "vars"))); err != nil {
		return nil, err
	}
	return scenario, nil
}

func parseYAML(data []byte) (*yaml.Node, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("ルートは mapping である必要があります")
	}
	return doc.Content[0], nil
}

func parseRunners(root *yaml.Node, baseDir string) []runnerInfo {
	var result []runnerInfo
	for _, entry := range mappingEntries(mappingValue(root, "runners")) {
		name, config := entry[0].Value, entry[1]
		runner := runnerInfo{name: name, kind: model.StepUnknown}
		switch {
		case mappingValue(config, "endpoint") != nil:
			runner.kind = model.StepHTTP
			runner.endpoint = scalar(mappingValue(config, "endpoint"))
		case mappingValue(config, "addr") != nil:
			runner.kind = model.StepGRPC
			runner.endpoint = scalar(mappingValue(config, "addr"))
			if protos := mappingValue(config, "protos"); protos != nil && protos.Kind == yaml.SequenceNode {
				for _, p := range protos.Content {
					runner.protos = append(runner.protos, resolveReference(baseDir, p.Value))
				}
			}
		case mappingValue(config, "dsn") != nil:
			runner.kind = model.StepDB
		}
		result = append(result, runner)
	}
	return result
}

func (l *loader) parseSteps(root *yaml.Node, sourcePath string, runners []runnerInfo, protoTypes protoIndex) ([]model.Step, error) {
	stepsNode := mappingValue(root, "steps")
	if stepsNode == nil {
		return nil, fmt.Errorf("steps がありません: %s", sourcePath)
	}
	type rawStep struct {
		id   string
		key  string
		node *yaml.Node
	}
	var raw []rawStep
	switch stepsNode.Kind {
	case yaml.MappingNode:
		for _, entry := range mappingEntries(stepsNode) {
			raw = append(raw, rawStep{id: entry[0].Value, key: entry[0].Value, node: entry[1]})
		}
	case yaml.SequenceNode:
		for i, n := range stepsNode.Content {
			raw = append(raw, rawStep{id: fmt.Sprintf("step-%d", i+1), key: strconv.Itoa(i), node: n})
		}
	default:
		return nil, fmt.Errorf("steps は mapping または sequence である必要があります: %s", sourcePath)
	}

	var result []model.Step
	for _, item := range raw {
		if include := mappingValue(item.node, "include"); include != nil {
			path := scalar(mappingValue(include, "path"))
			if path == "" {
				l.warnings = append(l.warnings, fmt.Sprintf("include.path がありません: %s:%d", sourcePath, item.node.Line))
				continue
			}
			childPath := resolveReference(filepath.Dir(sourcePath), path)
			child, err := l.loadRunbook(childPath)
			if err != nil {
				return nil, err
			}
			vars, varSources, err := loadIncludeVars(mappingValue(include, "vars"), filepath.Dir(childPath))
			if err != nil {
				return nil, err
			}
			l.includeSources = append(l.includeSources, append(child.Sources, varSources...)...)
			includeDesc := scalar(mappingValue(item.node, "desc"))
			includeLoop := mappingValue(item.node, "loop") != nil
			for _, childStep := range child.Steps {
				childStep.ID = item.id + "." + childStep.ID
				childStep.Key = item.key + "." + childStep.Key
				childStep.Loop = childStep.Loop || includeLoop
				for j := range childStep.LoopKeys {
					childStep.LoopKeys[j] = item.key + "." + childStep.LoopKeys[j]
				}
				if includeLoop {
					childStep.LoopKeys = append([]string{item.key}, childStep.LoopKeys...)
				}
				// 1 ステップだけの template はケースの呼び出しそのものなので、include 側の説明を手順名にする
				if includeDesc != "" && len(child.Steps) == 1 {
					childStep.Description = includeDesc
				}
				vars.apply(&childStep)
				result = append(result, childStep)
			}
			continue
		}
		step := parseStep(item.id, item.node, sourcePath, runners, protoTypes)
		if l.httpRefs != nil && step.HTTP != nil {
			l.httpRefs[step.HTTP] = httpVariableRefs{
				path:    varPaths(step.HTTP.Path),
				query:   varPaths(step.HTTP.Query),
				headers: varPaths(step.HTTP.Headers),
			}
		}
		step.Key = item.key
		step.Loop = mappingValue(item.node, "loop") != nil
		if step.Loop {
			step.LoopKeys = []string{step.Key}
		}
		result = append(result, step)
	}
	for i := range result {
		result[i].Number = i + 1
		if result[i].Kind == model.StepUnknown {
			l.warnings = append(l.warnings, fmt.Sprintf("未対応のステップを要約だけ出力します: %s:%d (%s)", result[i].SourcePath, result[i].SourceLine, result[i].ID))
		}
		if result[i].GRPC != nil && result[i].GRPC.RPCType == model.RPCUnsupported {
			l.warnings = append(l.warnings, fmt.Sprintf("Client/Bidirectional streaming は未対応です: %s", result[i].GRPC.Method))
		}
		if result[i].GRPC != nil && result[i].GRPC.RPCType == model.RPCUnknown {
			l.warnings = append(l.warnings, fmt.Sprintf("RPC 種別を判定できません。.proto を指定してください: %s", result[i].GRPC.Method))
		}
	}
	return result, nil
}

func parseStep(id string, n *yaml.Node, sourcePath string, runners []runnerInfo, protoTypes protoIndex) model.Step {
	step := model.Step{
		ID:          id,
		Description: firstNonEmpty(scalar(mappingValue(n, "desc")), id),
		Kind:        model.StepUnknown,
		Test:        scalar(mappingValue(n, "test")),
		SourcePath:  sourcePath,
		SourceLine:  n.Line,
	}
	for _, runner := range runners {
		operation := mappingValue(n, runner.name)
		if operation == nil {
			continue
		}
		switch runner.kind {
		case model.StepHTTP:
			step.Kind = model.StepHTTP
			step.HTTP = parseHTTP(runner, operation)
		case model.StepGRPC:
			step.Kind = model.StepGRPC
			step.GRPC = parseGRPC(runner, operation, protoTypes)
		case model.StepDB:
			step.Kind = model.StepDB
			step.DBQuery = scalar(mappingValue(operation, "query"))
		}
		break
	}
	if exec := mappingValue(n, "exec"); exec != nil {
		step.Kind = model.StepExec
		step.ExecCommand = scalar(mappingValue(exec, "command"))
	}
	if bind := mappingValue(n, "bind"); bind != nil {
		step.Kind = model.StepBind
		step.Bind = stringMap(bind)
	}
	if step.Kind == model.StepUnknown && step.Test != "" {
		step.Kind = model.StepTest
	}
	return step
}

func parseHTTP(runner runnerInfo, operation *yaml.Node) *model.HTTPRequest {
	req := &model.HTTPRequest{Runner: runner.name, Endpoint: runner.endpoint}
	entries := mappingEntries(operation)
	if len(entries) == 0 {
		return req
	}
	req.Path = entries[0][0].Value
	// runn の HTTP ステップには query 欄がなく、クエリは URL に直接書く。
	// 手順書では URL と分けて示すため、? 以降をクエリ文字列として取り出す。
	if path, query, found := strings.Cut(req.Path, "?"); found {
		req.Path = path
		req.Query = query
	}
	methods := mappingEntries(entries[0][1])
	if len(methods) == 0 {
		return req
	}
	req.Method = strings.ToUpper(methods[0][0].Value)
	config := methods[0][1]
	req.Headers = decodeAny(mappingValue(config, "headers"))
	if query := mappingValue(config, "query"); query != nil {
		req.Query = decodeAny(query)
	}
	if body := mappingValue(config, "body"); body != nil {
		bodyEntries := mappingEntries(body)
		if len(bodyEntries) > 0 && strings.Contains(bodyEntries[0][0].Value, "/") {
			req.ContentType = bodyEntries[0][0].Value
			req.Body = decodeAny(bodyEntries[0][1])
		} else {
			req.Body = decodeAny(body)
		}
	}
	return req
}

func parseGRPC(runner runnerInfo, operation *yaml.Node, protoTypes protoIndex) *model.GRPCRequest {
	req := &model.GRPCRequest{Runner: runner.name, Address: runner.endpoint, RPCType: model.RPCUnknown}
	entries := mappingEntries(operation)
	if len(entries) == 0 {
		return req
	}
	req.Method = entries[0][0].Value
	config := entries[0][1]
	req.Headers = decodeAny(mappingValue(config, "headers"))
	req.Message = decodeAny(mappingValue(config, "message"))
	req.Timeout = scalar(mappingValue(config, "timeout"))
	if rpcType, ok := protoTypes[req.Method]; ok {
		req.RPCType = rpcType
	} else {
		parts := strings.Split(req.Method, ".")
		short := parts[len(parts)-1]
		if rpcType, ok := protoTypes[short]; ok {
			req.RPCType = rpcType
		}
	}
	return req
}

func caseReferences(root *yaml.Node) []string {
	cases := mappingValue(mappingValue(root, "vars"), "cases")
	if cases == nil || cases.Kind != yaml.SequenceNode {
		return nil
	}
	var refs []string
	for _, item := range cases.Content {
		if strings.HasPrefix(item.Value, "json://") || strings.HasPrefix(item.Value, "yaml://") {
			refs = append(refs, item.Value)
		}
	}
	return refs
}

// suiteInclude は suite が template を呼ぶステップのキー、include のパス、loop があるかを返す。
func suiteInclude(root *yaml.Node) (key, path string, loop bool) {
	steps := mappingValue(root, "steps")
	for _, entry := range mappingEntries(steps) {
		if include := mappingValue(entry[1], "include"); include != nil {
			return entry[0].Value, scalar(mappingValue(include, "path")), mappingValue(entry[1], "loop") != nil
		}
	}
	return "", "", false
}

func loadCase(baseDir, ref string) (model.Case, error) {
	path := strings.TrimPrefix(strings.TrimPrefix(ref, "json://"), "yaml://")
	abs := resolveReference(baseDir, path)
	data, err := os.ReadFile(abs)
	if err != nil {
		return model.Case{}, fmt.Errorf("case を読めません: %s: %w", abs, err)
	}
	var raw map[string]any
	if strings.HasSuffix(strings.ToLower(abs), ".json") {
		err = json.Unmarshal(data, &raw)
	} else {
		err = yaml.Unmarshal(data, &raw)
		raw = normalize(raw).(map[string]any)
	}
	if err != nil {
		return model.Case{}, fmt.Errorf("case が不正です: %s: %w", abs, err)
	}
	expectation := map[string]any{}
	if value, ok := raw["expect"].(map[string]any); ok {
		expectation = value
		delete(raw, "expect")
	}
	name := fmt.Sprint(raw["name"])
	if name == "<nil>" || name == "" {
		name = strings.TrimSuffix(filepath.Base(abs), filepath.Ext(abs))
	}
	return model.Case{
		ID:          slug(name),
		Name:        name,
		Description: valueString(raw["description"]),
		SourcePath:  abs,
		Data:        raw,
		Expectation: expectation,
		SourceHash:  fileHash(data),
	}, nil
}

func resolveStatuses(scenario *model.Scenario) {
	for i := range scenario.Steps {
		step := &scenario.Steps[i]
		match := statusPattern.FindStringSubmatch(step.Test)
		if len(match) < 2 {
			continue
		}
		if step.Kind == model.StepGRPC {
			step.Status.Protocol = "gRPC"
		} else {
			step.Status.Protocol = "HTTP"
		}
		rhs := strings.Trim(match[1], `"'`)
		if _, err := strconv.Atoi(rhs); err == nil {
			step.Status.Value = rhs
			continue
		}
		step.Status.Variable = rhs
		const prefix = "vars.case."
		if strings.HasPrefix(rhs, prefix) {
			path := strings.TrimPrefix(rhs, prefix)
			var common string
			same := true
			for caseIndex, caseValue := range scenario.Cases {
				value := lookupCase(caseValue, path)
				if caseIndex == 0 {
					common = value
				} else if value != common {
					same = false
				}
			}
			if same && common != "" {
				step.Status.Value = common
			} else if !same {
				step.Status.Value = "ケース別"
			}
		}
	}
}

func lookupCase(caseValue model.Case, path string) string {
	var current any
	if strings.HasPrefix(path, "expect.") {
		current = caseValue.Expectation
		path = strings.TrimPrefix(path, "expect.")
	} else {
		current = caseValue.Data
	}
	for _, part := range strings.Split(path, ".") {
		mapping, ok := current.(map[string]any)
		if !ok {
			return ""
		}
		current = mapping[part]
	}
	return valueString(current)
}

func loadHooks(opts Options) ([]model.Asset, []model.Asset, []string, error) {
	var beforePaths, afterPaths []string
	if opts.ConfigPath != "" {
		configPath := resolveWithBase(opts.BaseDir, opts.ConfigPath)
		data, err := os.ReadFile(configPath)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("config を読めません: %s: %w", configPath, err)
		}
		root, err := parseYAML(data)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("config YAML が不正です: %w", err)
		}
		common := mappingValue(mappingValue(root, "hooks"), "common")
		beforePaths = append(beforePaths, sequenceStrings(mappingValue(common, "before"))...)
		afterPaths = append(afterPaths, sequenceStrings(mappingValue(common, "after"))...)
	}
	beforePaths = append(beforePaths, opts.ProjectBefore...)
	beforePaths = append(beforePaths, opts.BeforeSQL...)
	afterPaths = append(append(append([]string{}, opts.AfterSQL...), opts.ProjectAfter...), afterPaths...)
	before, warnings, err := readAssets(beforePaths, "before", opts.BaseDir)
	if err != nil {
		return nil, nil, warnings, err
	}
	after, moreWarnings, err := readAssets(afterPaths, "after", opts.BaseDir)
	return before, after, append(warnings, moreWarnings...), err
}

func readAssets(paths []string, origin, baseDir string) ([]model.Asset, []string, error) {
	var assets []model.Asset
	for _, path := range paths {
		abs, err := filepath.Abs(resolveWithBase(baseDir, path))
		if err != nil {
			return nil, nil, err
		}
		data, err := os.ReadFile(abs)
		if err != nil {
			return nil, nil, fmt.Errorf("%s SQL を読めません: %s: %w", origin, abs, err)
		}
		assets = append(assets, model.Asset{Path: abs, Content: string(data), Origin: origin, SHA256: fileHash(data)})
	}
	return assets, nil, nil
}

func sequenceStrings(n *yaml.Node) []string {
	if n == nil || n.Kind != yaml.SequenceNode {
		return nil
	}
	result := make([]string, 0, len(n.Content))
	for _, item := range n.Content {
		result = append(result, item.Value)
	}
	return result
}

func resolveReference(baseDir, ref string) string {
	ref = strings.TrimPrefix(strings.TrimPrefix(ref, "json://"), "yaml://")
	if filepath.IsAbs(ref) {
		return filepath.Clean(ref)
	}
	return filepath.Clean(filepath.Join(baseDir, filepath.FromSlash(ref)))
}

func fileHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func slug(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var b strings.Builder
	lastDash := false
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastDash = false
		} else if !lastDash && b.Len() > 0 {
			b.WriteByte('-')
			lastDash = true
		}
	}
	result := strings.Trim(b.String(), "-")
	if result == "" {
		hash := fnv.New32a()
		_, _ = hash.Write([]byte(value))
		return fmt.Sprintf("scenario-%08x", hash.Sum32())
	}
	// These names cannot be used as directory names on Windows.
	if result == "con" || result == "prn" || result == "aux" || result == "nul" ||
		(len(result) == 4 && (strings.HasPrefix(result, "com") || strings.HasPrefix(result, "lpt")) && result[3] >= '1' && result[3] <= '9') {
		return "scenario-" + result
	}
	return result
}

// OutputID applies the same filesystem-safe normalization used for runbook
// filenames. The generator also uses it for runnora.id-based output folders.
func OutputID(value string) string {
	return slug(value)
}

func valueString(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}

func resolveWithBase(baseDir, path string) string {
	if path == "" || filepath.IsAbs(path) || baseDir == "" {
		return path
	}
	return filepath.Join(baseDir, path)
}

func sourceRef(kind, path string) (model.SourceRef, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return model.SourceRef{}, err
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return model.SourceRef{}, err
	}
	return model.SourceRef{Kind: kind, Path: abs, SHA256: fileHash(data)}, nil
}

func addSource(scenario *model.Scenario, ref model.SourceRef) {
	for _, existing := range scenario.Sources {
		if existing.Path == ref.Path {
			return
		}
	}
	scenario.Sources = append(scenario.Sources, ref)
}
