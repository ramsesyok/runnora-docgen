package render

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/ramsesyok/runnora-docgen/internal/model"
)

type Document struct {
	Name    string
	Content []byte
}

func ScenarioDocuments(scenario *model.Scenario, showJSONDetail bool) []Document {
	filenames := !showJSONDetail
	documents := []Document{{Name: "scenario.qmd", Content: []byte(renderScenarioWithDetail(scenario, filenames))}}
	cases := ""
	if filenames {
		cases = renderCasesFilenames(scenario)
	} else {
		cases = renderCases(scenario)
	}
	if content := cases; content != "" {
		documents = append(documents, Document{Name: "cases.qmd", Content: []byte(content)})
	}
	if content := renderHooks(scenario, true); content != "" {
		documents = append(documents, Document{Name: "before.qmd", Content: []byte(content)})
	}
	if content := renderHooks(scenario, false); content != "" {
		documents = append(documents, Document{Name: "after.qmd", Content: []byte(content)})
	}
	if content := renderHTTP(scenario); content != "" {
		documents = append(documents, Document{Name: "http.qmd", Content: []byte(content)})
	}
	if content := renderGRPC(scenario); content != "" {
		documents = append(documents, Document{Name: "grpc.qmd", Content: []byte(content)})
	}
	requestContent := ""
	grpcRequestContent := ""
	expectationContent := ""
	if filenames {
		for _, step := range scenario.Steps {
			if step.HTTP != nil && step.HTTP.Body != nil {
				requestContent = generatedHeader(scenario) + "<!-- リクエスト参照は scenario.qmd に表示 -->\n"
			}
			if step.GRPC != nil && step.GRPC.Message != nil {
				grpcRequestContent = generatedHeader(scenario) + "<!-- gRPC 送信メッセージ参照は scenario.qmd に表示 -->\n"
			}
			if step.Test != "" {
				expectationContent = generatedHeader(scenario) + "<!-- 期待レスポンスボディ参照は scenario.qmd に表示 -->\n"
			}
		}
	} else {
		requestContent = renderRequestData(scenario, false)
		grpcRequestContent = renderRequestData(scenario, true)
		expectationContent = renderExpectations(scenario)
	}
	if content := requestContent; content != "" {
		documents = append(documents, Document{Name: "request-json.qmd", Content: []byte(content)})
	}
	if content := grpcRequestContent; content != "" {
		documents = append(documents, Document{Name: "grpc-request.qmd", Content: []byte(content)})
	}
	if content := expectationContent; content != "" {
		documents = append(documents, Document{Name: "expectations.qmd", Content: []byte(content)})
	}
	return documents
}

func renderScenario(scenario *model.Scenario) string {
	return renderScenarioWithDetail(scenario, false)
}

func renderScenarioWithDetail(scenario *model.Scenario, filenames bool) string {
	rows := make([][]string, 0, len(scenario.Steps))
	for _, step := range scenario.Steps {
		requestReference := ""
		detail := step.Description
		switch step.Kind {
		case model.StepHTTP:
			detail += "\\\nURL：[@" + label(scenario.ID, "http") + "]-\\[" + strconv.Itoa(step.Number) + "\\]"
			if step.HTTP.Body != nil {
				requestReference = "@" + label(scenario.ID, "request", step.ID)
			}
		case model.StepGRPC:
			detail += "\\\n" + string(step.GRPC.RPCType) + " " + step.GRPC.Method
			if step.GRPC.Message != nil {
				requestReference = "@" + label(scenario.ID, "grpc-request", step.ID)
			}
		case model.StepDB:
			detail += "\\\nDB query"
		case model.StepBind:
			detail += "\\\n変数を設定"
		case model.StepTest:
			detail += "\\\n検証のみ"
		}
		if requestReference != "" {
			if filenames {
				requestReference = formatBodyRefs(step.RequestJSONRefs)
				if requestReference == "" {
					requestReference = markdownFile(filepath.Base(step.SourcePath)) + "（インライン）"
				}
			} else {
				requestReference = appendJSONFiles(requestReference, step.RequestJSONFiles)
			}
		}
		status := ""
		if step.Status.Value != "" {
			status = step.Status.Value
		} else if step.Status.Variable != "" {
			status = step.Status.Variable
		}
		if status != "" {
			if step.Status.Protocol == "gRPC" {
				status = "gRPC " + status
			}
			status = "期待値：\\[" + status + "\\]"
		}
		if step.Test != "" && !filenames {
			if status != "" {
				status += " "
			} else {
				status = "期待値："
			}
			status += "@" + label(scenario.ID, "expect", step.ID)
		}
		if filenames {
			body := formatBodyRefs(step.ResponseBodyJSONRefs)
			if body == "" && step.HasResponseBodyCheck {
				body = markdownFile(filepath.Base(step.SourcePath)) + ":" + strconv.Itoa(step.SourceLine) + "（検証式）"
			}
			if body != "" {
				if status != "" {
					status += "\\\n"
				}
				status += body
			}
		} else {
			status = appendJSONFiles(status, step.ExpectationJSONFiles)
		}
		rows = append(rows, []string{strconv.Itoa(step.Number), detail, requestReference, status, ""})
	}
	return generatedHeader(scenario) +
		"::: {.landscape}\n" +
		fmt.Sprintf("::: {.tbl caption=\"テストシナリオ\" label=\"%s\" widths=\"8,46,20,20,6\" breakable-rows=\"true\"}\n", label(scenario.ID, "scenario")) +
		scenarioGrid(scenario.Name, rows, []int{8, 54, 24, 30, 8}, alignCenter, alignLeft, alignLeft, alignLeft, alignCenter) + ":::\n:::\n"
}

// Keep filenames literal (including underscores and brackets) in Markdown cells.
func appendJSONFiles(reference string, paths []string) string {
	seen := map[string]bool{}
	for _, path := range paths {
		name := filepath.Base(strings.ReplaceAll(path, `\`, "/"))
		if seen[name] {
			continue
		}
		seen[name] = true
		name = strings.NewReplacer(`\`, `\\`, "_", `\_`, "*", `\*`, "[", `\[`, "]", `\]`, "`", "\\`").Replace(name)
		reference += "\\\n" + name
	}
	return reference
}

func markdownFile(name string) string {
	return strings.NewReplacer(`\`, `\\`, "_", `\_`, "*", `\*`, "[", `\[`, "]", `\]`, "`", "\\`").Replace(name)
}

func formatBodyRefs(refs []model.JSONRef) string {
	type entry struct {
		path   string
		fields []string
		whole  bool
	}
	var entries []*entry
	byPath := map[string]*entry{}
	basePaths := map[string]map[string]bool{}
	for _, ref := range refs {
		if ref.Path == "" {
			continue
		}
		path := filepath.ToSlash(ref.Path)
		item := byPath[path]
		if item == nil {
			item = &entry{path: path}
			byPath[path] = item
			entries = append(entries, item)
		}
		base := filepath.Base(path)
		if basePaths[base] == nil {
			basePaths[base] = map[string]bool{}
		}
		basePaths[base][path] = true
		if ref.FieldPath == "" {
			item.whole = true
		} else {
			found := false
			for _, field := range item.fields {
				if field == ref.FieldPath {
					found = true
					break
				}
			}
			if !found {
				item.fields = append(item.fields, ref.FieldPath)
			}
		}
	}
	var lines []string
	for _, item := range entries {
		name := filepath.Base(item.path)
		if len(basePaths[name]) > 1 {
			name = item.path
		}
		line := markdownFile(name)
		if !item.whole && len(item.fields) > 0 {
			line += "（" + strings.Join(item.fields, ", ") + "）"
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\\\n")
}

func renderCasesFilenames(scenario *model.Scenario) string {
	if len(scenario.Cases) == 0 {
		return ""
	}
	if len(scenario.Cases) == 1 && len(scenario.Cases[0].Data) == 0 && len(scenario.Cases[0].Expectation) == 0 {
		return ""
	}
	rows := make([][]string, 0, len(scenario.Cases))
	for _, caseValue := range scenario.Cases {
		var requests, responses []model.JSONRef
		for _, step := range scenario.Steps {
			for _, ref := range step.RequestJSONRefs {
				if ref.Path == caseValue.SourcePath {
					requests = append(requests, ref)
				}
			}
			for _, ref := range step.ResponseBodyJSONRefs {
				if ref.Path == caseValue.SourcePath {
					responses = append(responses, ref)
				}
			}
		}
		expect := ""
		if status, ok := caseValue.Expectation["status"]; ok {
			expect = "ステータス: " + compactJSON(status)
		}
		if body := formatBodyRefs(responses); body != "" {
			if expect != "" {
				expect += "\\\n"
			}
			expect += body
		}
		rows = append(rows, []string{caseValue.Name, caseValue.Description, formatBodyRefs(requests), expect, markdownFile(filepath.Base(caseValue.SourcePath))})
	}
	return generatedHeader(scenario) + tableBlock(
		captionWithScenario("ケースデータ一覧", scenario.Name), label(scenario.ID, "cases"), "16,33,25,10,16",
		[]string{"ケース", "説明", "入力", "期待値", "出典"}, rows, []int{16, 36, 34, 20, 24},
		alignLeft, alignLeft, alignLeft, alignLeft, alignLeft)
}

func renderCases(scenario *model.Scenario) string {
	if len(scenario.Cases) == 0 {
		return ""
	}
	if len(scenario.Cases) == 1 && len(scenario.Cases[0].Data) == 0 && len(scenario.Cases[0].Expectation) == 0 {
		return ""
	}
	requestRef := firstRequestReference(scenario)
	expectRef := firstExpectationReference(scenario)
	rows := make([][]string, 0, len(scenario.Cases))
	for _, caseValue := range scenario.Cases {
		input := strings.Join(caseInputSummary(caseValue, requestRef), "\\\n")
		expect := expectationSummary(caseValue.Expectation, expectRef)
		rows = append(rows, []string{caseValue.Name, caseValue.Description, input, expect, filepath.Base(caseValue.SourcePath)})
	}
	return generatedHeader(scenario) + tableBlock(
		captionWithScenario("ケースデータ一覧", scenario.Name), label(scenario.ID, "cases"), "16,33,25,10,16",
		[]string{"ケース", "説明", "入力", "期待値", "出典"}, rows, []int{16, 36, 34, 20, 24},
		alignLeft, alignLeft, alignLeft, alignLeft, alignLeft)
}

func renderHTTP(scenario *model.Scenario) string {
	var rows [][]string
	for _, step := range scenario.Steps {
		if step.HTTP == nil {
			continue
		}
		rows = append(rows, []string{
			strconv.Itoa(step.Number), step.HTTP.Method,
			joinURL(step.HTTP.Endpoint, step.HTTP.Path), headerCell(step.HTTP.Headers), queryCell(step.HTTP.Query),
		})
	}
	if len(rows) == 0 {
		return ""
	}
	return generatedHeader(scenario) + tableBlock(
		captionWithScenario("HTTP 呼び出し情報", scenario.Name), label(scenario.ID, "http"), "6,8,40,23,23",
		[]string{"手順", "Method", "URL", "Headers", "Query"}, rows, []int{6, 8, 54, 32, 32},
		alignCenter, alignLeft, alignLeft, alignLeft, alignLeft)
}

func renderGRPC(scenario *model.Scenario) string {
	var rows [][]string
	for _, step := range scenario.Steps {
		if step.GRPC == nil {
			continue
		}
		rows = append(rows, []string{
			strconv.Itoa(step.Number), step.GRPC.Runner, step.GRPC.Address, step.GRPC.Method,
			string(step.GRPC.RPCType), compactJSON(step.GRPC.Headers), step.GRPC.Timeout,
		})
	}
	if len(rows) == 0 {
		return ""
	}
	return generatedHeader(scenario) + tableBlock(
		captionWithScenario("gRPC 呼び出し情報", scenario.Name), label(scenario.ID, "grpc"), "7,10,18,25,15,15,10",
		[]string{"手順", "Runner", "Address", "Service/Method", "RPC type", "Metadata", "Timeout"}, rows, []int{6, 10, 20, 38, 18, 22, 12})
}

func renderRequestData(scenario *model.Scenario, grpc bool) string {
	var blocks strings.Builder
	for _, step := range scenario.Steps {
		var value any
		prefix := "request"
		caption := "リクエストボディ"
		if grpc {
			if step.GRPC == nil || step.GRPC.Message == nil {
				continue
			}
			value = step.GRPC.Message
			prefix = "grpc-request"
			caption = "gRPC 送信メッセージ"
		} else {
			if step.HTTP == nil || step.HTTP.Body == nil {
				continue
			}
			value = step.HTTP.Body
		}
		rows := flattenRows(value)
		headers := []string{"JSON path", "値"}
		widthsAttr := "50,50"
		widths := []int{34, 70}
		if len(step.RequestJSONData) > 0 {
			rows = [][]string{{"参照式", "$", compactJSON(value)}}
			for _, asset := range step.RequestJSONData {
				for _, row := range flattenRows(asset.Value) {
					rows = append(rows, []string{filepath.Base(asset.Path), row[0], row[1]})
				}
			}
			headers = []string{"出典", "JSON path", "値"}
			widthsAttr = "25,30,45"
			widths = []int{30, 34, 58}
		}
		if templateCaseReference(value) && len(scenario.Cases) > 0 {
			rows = nil
			for _, caseValue := range scenario.Cases {
				caseRequest := caseValue.Data["requestBody"]
				if grpc {
					caseRequest = caseValue.Data["message"]
				}
				if caseRequest == nil {
					continue
				}
				for _, row := range flattenRows(caseRequest) {
					rows = append(rows, []string{caseValue.Name, row[0], row[1]})
				}
			}
			headers = []string{"ケース", "JSON path", "値"}
			widthsAttr = "18,32,50"
			widths = []int{18, 34, 58}
		}
		blocks.WriteString(tableBlockWithMerge(
			fmt.Sprintf("%s（%s／手順 %d）", caption, scenario.Name, step.Number), label(scenario.ID, prefix, step.ID), widthsAttr, !grpc,
			headers, rows, widths))
		blocks.WriteByte('\n')
	}
	if blocks.Len() == 0 {
		return ""
	}
	return generatedHeader(scenario) + blocks.String()
}

func renderExpectations(scenario *model.Scenario) string {
	var blocks strings.Builder
	for _, step := range scenario.Steps {
		if step.Test == "" {
			continue
		}
		rows := [][]string{{"検証式", sourceLocation(step.SourcePath, step.SourceLine), breakConditions(step.Test)}}
		if step.Status.Value != "" {
			rows = append(rows, []string{"ステータス", "検証式から抽出", step.Status.Protocol + " " + step.Status.Value})
		}
		for _, asset := range step.ExpectationJSONData {
			for _, row := range flattenRows(asset.Value) {
				rows = append(rows, []string{"期待値JSON", filepath.Base(asset.Path), row[0] + ": " + row[1]})
			}
		}
		if strings.Contains(step.Test, "vars.case.") {
			for _, caseValue := range scenario.Cases {
				for _, expected := range flattenExpectationRows(caseValue) {
					pathExpression := "vars.case.expect" + strings.TrimPrefix(expected[0], "$")
					usage := "ケース定義（検証式参照未確認）"
					if strings.Contains(step.Test, pathExpression) {
						usage = "ケース定義（検証式参照）"
					}
					rows = append(rows, []string{"ケース " + caseValue.Name, filepath.Base(caseValue.SourcePath) + "\\\n" + usage, expected[0] + ": " + expected[1]})
				}
			}
		}
		blocks.WriteString(tableBlockWithMerge(
			fmt.Sprintf("期待値・検証条件（%s／手順 %d）", scenario.Name, step.Number), label(scenario.ID, "expect", step.ID), "10,20,62", true,
			[]string{"区分", "出典", "内容"}, rows, []int{18, 26, 72}))
		blocks.WriteByte('\n')
	}
	if blocks.Len() == 0 {
		return ""
	}
	return generatedHeader(scenario) + blocks.String()
}

func renderHooks(scenario *model.Scenario, before bool) string {
	assets := scenario.AfterHooks
	caption := "後処理"
	labelPart := "after"
	widthsAttr := "10,30,60"
	if before {
		assets = scenario.BeforeHooks
		caption = "前処理"
		labelPart = "before"
		widthsAttr = "6,34,60"
	}
	if len(assets) == 0 {
		return ""
	}
	// SQL 本文は載せない。表が大きくなり読みにくいため、実行順とファイルだけを示し、本文は出典のファイルを参照させる。
	rows := make([][]string, 0, len(assets))
	for i, asset := range assets {
		rows = append(rows, []string{strconv.Itoa(i + 1), filepath.Base(asset.Path), asset.Path})
	}
	return generatedHeader(scenario) + tableBlock(
		captionWithScenario(caption, scenario.Name), label(scenario.ID, labelPart), widthsAttr,
		[]string{"順序", "ファイル", "出典"}, rows, []int{6, 30, 60})
}

func captionWithScenario(caption, scenarioName string) string {
	if strings.TrimSpace(scenarioName) == "" {
		return caption
	}
	return caption + "（" + scenarioName + "）"
}

func tableBlock(caption, tableLabel, widthsAttr string, headers []string, rows [][]string, widths []int, alignments ...columnAlignment) string {
	return tableBlockWithMerge(caption, tableLabel, widthsAttr, false, headers, rows, widths, alignments...)
}

func tableBlockWithMerge(caption, tableLabel, widthsAttr string, mergeAll bool, headers []string, rows [][]string, widths []int, alignments ...columnAlignment) string {
	mergeAttr := ""
	if mergeAll {
		mergeAttr = ` merge-cols="all"`
	}
	return fmt.Sprintf("::: {.landscape}\n::: {.tbl caption=\"%s\" label=\"%s\" widths=\"%s\"%s breakable-rows=\"true\"}\n%s:::\n:::\n",
		caption, tableLabel, widthsAttr, mergeAttr, gridTable(headers, rows, widths, alignments...))
}

func generatedHeader(scenario *model.Scenario) string {
	return fmt.Sprintf("<!-- Code generated by runnora-docgen; DO NOT EDIT.\nsource: %s\nsha256: %s\n-->\n\n", scenario.SourcePath, scenario.SourceHash)
}

func sourceLocation(path string, line int) string {
	name := filepath.Base(path)
	location := ":" + strconv.Itoa(line)
	if displayWidth(name+location) <= 24 {
		return name + location
	}
	if index := strings.LastIndex(name, ".template"); index > 0 {
		stem := name[:index]
		if separator := strings.Index(stem, "_"); separator > 0 && displayWidth(stem) > 18 {
			stem = stem[:separator+1] + "\\\n" + breakCamelIdentifier(stem[separator+1:], 12)
		}
		return stem + "\\\n" + name[index:] + location
	}
	if index := strings.LastIndex(name, "."); index > 0 {
		return name[:index] + "\\\n" + name[index:] + location
	}
	return name + "\\\n" + strconv.Itoa(line)
}

func breakCamelIdentifier(value string, maxWidth int) string {
	if displayWidth(value) <= maxWidth {
		return value
	}
	var words []string
	start := 0
	runes := []rune(value)
	for index := 1; index < len(runes); index++ {
		if runes[index] >= 'A' && runes[index] <= 'Z' {
			words = append(words, string(runes[start:index]))
			start = index
		}
	}
	words = append(words, string(runes[start:]))

	var lines []string
	line := ""
	for _, word := range words {
		if line != "" && displayWidth(line+word) > maxWidth {
			lines = append(lines, line)
			line = ""
		}
		line += word
	}
	if line != "" {
		lines = append(lines, line)
	}
	return strings.Join(lines, "\\\n")
}

func joinURL(endpoint, path string) string {
	if endpoint == "" {
		return path
	}
	return strings.TrimRight(endpoint, "/") + "/" + strings.TrimLeft(path, "/")
}

// queryCell は URL から取り出したクエリ文字列を「名前=値」の 1 行ずつに分けて示す。
// 文字列以外 (旧来の query 指定) は JSON で示す。
func queryCell(value any) string {
	query, ok := value.(string)
	if !ok {
		return compactJSON(value)
	}
	var lines []string
	for _, pair := range strings.Split(query, "&") {
		if pair != "" {
			lines = append(lines, pair)
		}
	}
	return strings.Join(lines, "\\\n")
}

// headerCell shows each top-level HTTP header on its own line without the
// enclosing JSON object braces. Commas in quoted values or nested JSON stay put.
func headerCell(value any) string {
	encoded := compactJSON(value)
	if len(encoded) < 2 || encoded[0] != '{' || encoded[len(encoded)-1] != '}' {
		return encoded
	}
	inner := encoded[1 : len(encoded)-1]
	if inner == "" {
		return ""
	}
	var members []string
	start, depth := 0, 0
	quoted, escaped := false, false
	for i := 0; i < len(inner); i++ {
		switch inner[i] {
		case '\\':
			if quoted && !escaped {
				escaped = true
				continue
			}
		case '"':
			if !escaped {
				quoted = !quoted
			}
		case '{', '[':
			if !quoted {
				depth++
			}
		case '}', ']':
			if !quoted {
				depth--
			}
		case ',':
			if !quoted && depth == 0 {
				members = append(members, inner[start:i+1])
				start = i + 1
			}
		}
		escaped = false
	}
	members = append(members, inner[start:])
	return strings.Join(members, "\\\n")
}

// breakConditions adds a visible line break after each logical AND, while
// leaving && inside string literals untouched.
func breakConditions(expression string) string {
	var parts []string
	start := 0
	quote := byte(0)
	escaped := false
	for i := 0; i < len(expression); i++ {
		character := expression[i]
		if quote != 0 {
			if character == '\\' && !escaped {
				escaped = true
				continue
			}
			if character == quote && !escaped {
				quote = 0
			}
			escaped = false
			continue
		}
		if character == '"' || character == '\'' || character == '`' {
			quote = character
			continue
		}
		if character == '&' && i+1 < len(expression) && expression[i+1] == '&' {
			parts = append(parts, strings.TrimSpace(expression[start:i])+" &&")
			i++
			start = i + 1
		}
	}
	if len(parts) == 0 {
		return expression
	}
	parts = append(parts, strings.TrimSpace(expression[start:]))
	return strings.Join(parts, "\\\n")
}

func compactJSON(value any) string {
	if value == nil {
		return ""
	}
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprint(value)
	}
	return string(data)
}

func flattenRows(value any) [][]string {
	var rows [][]string
	flatten("$", value, &rows)
	if len(rows) == 0 {
		return [][]string{{"$", "null"}}
	}
	return rows
}

func flatten(path string, value any, rows *[][]string) {
	switch typed := value.(type) {
	case map[string]any:
		if len(typed) == 0 {
			*rows = append(*rows, []string{path, "{}"})
			return
		}
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			flatten(path+"."+key, typed[key], rows)
		}
	case []any:
		if len(typed) == 0 {
			*rows = append(*rows, []string{path, "[]"})
			return
		}
		for i, item := range typed {
			flatten(fmt.Sprintf("%s[%d]", path, i), item, rows)
		}
	default:
		*rows = append(*rows, []string{path, fmt.Sprint(typed)})
	}
}

func caseInputSummary(caseValue model.Case, requestRef string) []string {
	keys := make([]string, 0, len(caseValue.Data))
	for key := range caseValue.Data {
		if key != "name" && key != "description" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		if isEmptyValue(caseValue.Data[key]) {
			continue
		}
		if key == "requestBody" && requestRef != "" {
			result = append(result, key+": @"+requestRef)
			continue
		}
		result = append(result, key+": "+compactJSON(caseValue.Data[key]))
	}
	return result
}

func isEmptyValue(value any) bool {
	if value == nil {
		return true
	}
	switch typed := value.(type) {
	case string:
		return typed == ""
	case map[string]any:
		return len(typed) == 0
	case []any:
		return len(typed) == 0
	default:
		return false
	}
}

func expectationSummary(expectation map[string]any, expectationRef string) string {
	if len(expectation) == 0 {
		return ""
	}
	if expectationRef != "" {
		return "@" + expectationRef
	}
	keys := make([]string, 0, len(expectation))
	for key := range expectation {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var result []string
	for _, key := range keys {
		result = append(result, key+": "+compactJSON(expectation[key]))
	}
	return strings.Join(result, "\\\n")
}

func firstRequestReference(scenario *model.Scenario) string {
	for _, step := range scenario.Steps {
		if step.HTTP != nil && step.HTTP.Body != nil {
			return label(scenario.ID, "request", step.ID)
		}
		if step.GRPC != nil && step.GRPC.Message != nil {
			return label(scenario.ID, "grpc-request", step.ID)
		}
	}
	return ""
}

func firstExpectationReference(scenario *model.Scenario) string {
	for _, step := range scenario.Steps {
		if step.Test != "" {
			return label(scenario.ID, "expect", step.ID)
		}
	}
	return ""
}

func templateCaseReference(value any) bool {
	text, ok := value.(string)
	return ok && strings.Contains(text, "vars.case")
}

func flattenExpectationRows(caseValue model.Case) [][]string {
	if len(caseValue.Expectation) == 0 {
		return nil
	}
	return flattenRows(caseValue.Expectation)
}

func label(parts ...string) string {
	kind := "doc"
	if len(parts) > 1 {
		kind = parts[1]
	}
	abbreviations := map[string]string{
		"scenario": "scn", "cases": "case", "request": "req", "grpc-request": "greq",
		"expect": "exp", "http": "http", "grpc": "grpc", "before": "pre", "after": "post",
	}
	if short, ok := abbreviations[kind]; ok {
		kind = short
	}
	var components []string
	for i, part := range parts {
		if i != 1 {
			components = append(components, labelComponent(part))
		}
	}
	return "tbl-" + kind + "-" + strings.Join(components, ":")
}

// Escape unsafe bytes and the escape marker to preserve distinct IDs.
func labelComponent(value string) string {
	var b strings.Builder
	for i := 0; i < len(value); i++ {
		c := value[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "_%02x_", c)
		}
	}
	return b.String()
}
