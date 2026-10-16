package source

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/ramsesyok/runnora-docgen/internal/model"
	"gopkg.in/yaml.v3"
)

// includeVars holds the values an include step passes to its child runbook.
// Static json:// values are read the same way runn does: relative to the
// included runbook, not to the runbook that contains the include step.
type includeVars struct {
	values map[string]any    // variable name -> value (JSON files already decoded)
	files  map[string]string // variable name -> absolute JSON path
}

var (
	wholeVarExpression = regexp.MustCompile(`^\s*\{\{\s*(vars(?:\.[A-Za-z_][A-Za-z0-9_]*)+)\s*\}\}\s*$`)
	embeddedVarPattern = regexp.MustCompile(`\{\{\s*(vars(?:\.[A-Za-z_][A-Za-z0-9_]*)+)\s*\}\}`)
)

// vars は YAML に書かれた順に処理する。map で回すと出典 (manifest) の並びが実行ごとに変わるため。
func loadIncludeVars(varsNode *yaml.Node, childDir string) (includeVars, []model.SourceRef, error) {
	result := includeVars{values: map[string]any{}, files: map[string]string{}}
	var sources []model.SourceRef
	for _, entry := range mappingEntries(varsNode) {
		name, value := entry[0].Value, decodeAny(entry[1])
		ref, ok := value.(string)
		if !ok || !strings.HasPrefix(ref, "json://") || strings.ContainsAny(ref, "{}$") {
			result.values[name] = value
			continue
		}
		path := strings.TrimPrefix(ref, "json://")
		if !strings.EqualFold(filepath.Ext(path), ".json") {
			result.values[name] = value
			continue
		}
		abs := resolveReference(childDir, path)
		data, err := os.ReadFile(abs)
		if err != nil {
			return result, nil, fmt.Errorf("include の vars.%s が参照する JSON を読めません: %s: %w", name, abs, err)
		}
		var decoded any
		if err := json.Unmarshal(data, &decoded); err != nil {
			return result, nil, fmt.Errorf("include の vars.%s が参照する JSON が不正です: %s: %w", name, abs, err)
		}
		result.values[name] = decoded
		result.files[name] = abs
		sources = append(sources, model.SourceRef{Kind: "json", Path: abs, SHA256: fileHash(data)})
	}
	return result, sources, nil
}

// apply fills in what the child runbook left as templates: request values,
// the expected status and the JSON files the step reads through its vars.
func (v includeVars) apply(step *model.Step) {
	if len(v.values) == 0 {
		return
	}
	if step.HTTP != nil {
		step.HTTP.Path = fmt.Sprint(v.substitute(step.HTTP.Path))
		step.HTTP.Query = v.substituteQuery(step.HTTP.Query)
		step.HTTP.Headers = v.substitute(step.HTTP.Headers)
		step.RequestJSONFiles = append(step.RequestJSONFiles, v.referencedFiles(step.HTTP.Body)...)
		step.RequestJSONRefs = appendRefs(step.RequestJSONRefs, v.refsForVars(varPaths(step.HTTP.Body)))
		step.HTTP.Body = v.substitute(step.HTTP.Body)
	}
	if step.GRPC != nil {
		step.GRPC.Headers = v.substitute(step.GRPC.Headers)
		step.RequestJSONFiles = append(step.RequestJSONFiles, v.referencedFiles(step.GRPC.Message)...)
		step.RequestJSONRefs = appendRefs(step.RequestJSONRefs, v.refsForVars(varPaths(step.GRPC.Message)))
		step.GRPC.Message = v.substitute(step.GRPC.Message)
	}
	if strings.HasPrefix(step.Status.Variable, "vars.") {
		if value, ok := v.lookup(step.Status.Variable); ok && isScalar(value) {
			step.Status.Value = valueString(value)
		}
	}
	step.ExpectationJSONFiles = append(step.ExpectationJSONFiles, v.referencedFiles("{{"+step.Test+"}}")...)
	step.ResponseBodyJSONRefs = appendRefs(step.ResponseBodyJSONRefs, v.refsForVars(responseBodyVarPaths(step.Test)))
	step.ExpectationJSONData = append(step.ExpectationJSONData, v.expectationData(step.Test)...)
}

// substituteQuery displays an explicit null value in the HTTP Query column.
// Other request fields keep the usual substitution behavior.
func (v includeVars) substituteQuery(value any) any {
	return v.substituteQueryValue(value, true)
}

func (v includeVars) substituteQueryValue(value any, topLevel bool) any {
	switch query := value.(type) {
	case string:
		if match := wholeVarExpression.FindStringSubmatch(query); match != nil {
			if resolved, found := v.lookup(match[1]); found {
				if resolved == nil && topLevel {
					return "null"
				}
				return resolved
			}
			return query
		}
		return embeddedVarPattern.ReplaceAllStringFunc(query, func(expression string) string {
			path := embeddedVarPattern.FindStringSubmatch(expression)[1]
			if resolved, found := v.lookup(path); found {
				if resolved == nil {
					return "null"
				}
				if isScalar(resolved) {
					return valueString(resolved)
				}
			}
			return expression
		})
	case map[string]any:
		result := make(map[string]any, len(query))
		for key, child := range query {
			result[key] = v.substituteQueryValue(child, false)
		}
		return result
	case []any:
		result := make([]any, len(query))
		for index, child := range query {
			result[index] = v.substituteQueryValue(child, false)
		}
		return result
	}
	return value
}

// substitute replaces {{ vars.x.y }} with the included value. A string that is
// only one expression takes the value as is (object, number, ...); expressions
// inside a longer string are replaced only with scalar values.
func (v includeVars) substitute(value any) any {
	switch t := value.(type) {
	case string:
		if m := wholeVarExpression.FindStringSubmatch(t); m != nil {
			if resolved, ok := v.lookup(m[1]); ok {
				return resolved
			}
			return t
		}
		return embeddedVarPattern.ReplaceAllStringFunc(t, func(expression string) string {
			path := embeddedVarPattern.FindStringSubmatch(expression)[1]
			if resolved, ok := v.lookup(path); ok && isScalar(resolved) {
				return valueString(resolved)
			}
			return expression
		})
	case map[string]any:
		result := make(map[string]any, len(t))
		for key, child := range t {
			result[key] = v.substitute(child)
		}
		return result
	case []any:
		result := make([]any, len(t))
		for i, child := range t {
			result[i] = v.substitute(child)
		}
		return result
	}
	return value
}

func (v includeVars) lookup(expression string) (any, bool) {
	parts := strings.Split(strings.TrimPrefix(expression, "vars."), ".")
	current, ok := v.values[parts[0]]
	if !ok {
		return nil, false
	}
	for _, part := range parts[1:] {
		mapping, isMap := current.(map[string]any)
		if !isMap {
			return nil, false
		}
		if current, ok = mapping[part]; !ok {
			return nil, false
		}
	}
	return current, true
}

// referencedFiles lists the JSON files whose variables appear in value.
func (v includeVars) referencedFiles(value any) []string {
	seen := map[string]bool{}
	var files []string
	for _, path := range varPaths(value) {
		name := strings.Split(strings.TrimPrefix(path, "vars."), ".")[0]
		if file, ok := v.files[name]; ok && !seen[file] {
			seen[file] = true
			files = append(files, file)
		}
	}
	return files
}

// expectationData shows what the test expression reads from each JSON file:
// the whole file when the variable itself is used (e.g. compare with
// vars.expected), otherwise only the referenced fields (e.g. vars.case.expect.status).
func (v includeVars) expectationData(test string) []model.JSONData {
	whole := map[string]bool{}
	fields := map[string]map[string]any{}
	var order []string
	for _, path := range varPaths("{{" + test + "}}") {
		parts := strings.Split(strings.TrimPrefix(path, "vars."), ".")
		if _, ok := v.files[parts[0]]; !ok {
			continue
		}
		if _, known := fields[parts[0]]; !known && !whole[parts[0]] {
			order = append(order, parts[0])
			fields[parts[0]] = map[string]any{}
		}
		if len(parts) == 1 {
			whole[parts[0]] = true
			continue
		}
		if value, ok := v.lookup(path); ok {
			setPath(fields[parts[0]], parts[1:], value)
		}
	}
	var result []model.JSONData
	for _, name := range order {
		data := model.JSONData{Path: v.files[name], Value: v.values[name]}
		if !whole[name] {
			if len(fields[name]) == 0 {
				continue
			}
			data.Value = fields[name]
		}
		result = append(result, data)
	}
	return result
}

func varPaths(value any) []string {
	var paths []string
	var visit func(any)
	visit = func(value any) {
		switch t := value.(type) {
		case string:
			for _, expression := range templateExpressionPattern.FindAllString(t, -1) {
				paths = append(paths, variablePathPattern.FindAllString(expression, -1)...)
			}
		case map[string]any:
			keys := make([]string, 0, len(t))
			for key := range t {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				visit(t[key])
			}
		case []any:
			for _, child := range t {
				visit(child)
			}
		}
	}
	visit(value)
	return paths
}

func setPath(target map[string]any, parts []string, value any) {
	for _, part := range parts[:len(parts)-1] {
		next, ok := target[part].(map[string]any)
		if !ok {
			next = map[string]any{}
			target[part] = next
		}
		target = next
	}
	target[parts[len(parts)-1]] = value
}

func isScalar(value any) bool {
	switch value.(type) {
	case string, float64, int, int64, bool:
		return true
	}
	return false
}
