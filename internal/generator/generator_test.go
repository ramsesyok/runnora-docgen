package generator

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateKeepsCountOutOfScenarioTable(t *testing.T) {
	dir := t.TempDir()
	runbook := filepath.Join(dir, "scenario.yml")
	content := `desc: 一覧取得
runners:
  req:
    endpoint: http://example.test
steps:
  list:
    desc: ユーザー一覧を取得する
    req:
      /users:
        get:
          body: null
    test: current.res.status == 200 && len(current.res.body) == 3
`
	if err := os.WriteFile(runbook, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(dir, "generated")
	result, err := Generate(context.Background(), Options{RunbookPaths: []string{runbook}, OutputDir: output, ShowJSONDetail: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Files) == 0 {
		t.Fatal("no files generated")
	}
	manifestData, err := os.ReadFile(filepath.Join(output, "scenario", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var recorded map[string]any
	if err := json.Unmarshal(manifestData, &recorded); err != nil {
		t.Fatal(err)
	}
	inputs, ok := recorded["inputs"].([]any)
	if !ok || len(inputs) == 0 {
		t.Fatalf("manifest inputs = %#v", recorded["inputs"])
	}
	if filepath.IsAbs(recorded["source"].(string)) {
		t.Fatalf("manifest source is absolute: %q", recorded["source"])
	}
	for _, input := range inputs {
		path := input.(map[string]any)["path"].(string)
		if filepath.IsAbs(path) {
			t.Fatalf("manifest input path is absolute: %q", path)
		}
		if strings.Contains(path, `\`) {
			t.Fatalf("manifest input path is not slash-normalized: %q", path)
		}
	}
	scenarioData, err := os.ReadFile(filepath.Join(output, "scenario", "scenario.qmd"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(scenarioData), `期待値：\[200\]`) {
		t.Fatalf("scenario does not contain status:\n%s", scenarioData)
	}
	// 出典は相対パスで書く (一時ディレクトリの相対パスは OS によって "../tmp/..." になるので、行の先頭で判定する)
	for _, line := range strings.Split(string(scenarioData), "\n") {
		if source, ok := strings.CutPrefix(line, "source: "); ok && (filepath.IsAbs(source) || strings.HasPrefix(source, "/")) {
			t.Fatalf("scenario header contains an absolute source path:\n%s", scenarioData)
		}
	}
	if !strings.Contains(string(scenarioData), "::: {.landscape}\n::: {.tbl") {
		t.Fatalf("scenario table is not landscape:\n%s", scenarioData)
	}
	if !strings.Contains(string(scenarioData), `widths="8,46,20,20,6"`) {
		t.Fatalf("scenario widths are incorrect:\n%s", scenarioData)
	}
	if strings.Contains(string(scenarioData), "len(current.res.body)") {
		t.Fatalf("scenario contains non-status assertion:\n%s", scenarioData)
	}
	if strings.Contains(string(scenarioData), "GET http://example.test/users") {
		t.Fatalf("scenario duplicates HTTP method and URL:\n%s", scenarioData)
	}
	if !strings.Contains(string(scenarioData), "URL：[@tbl-http-") || !strings.Contains(string(scenarioData), `]-\[1\]`) {
		t.Fatalf("scenario does not reference HTTP call details:\n%s", scenarioData)
	}
	httpData, err := os.ReadFile(filepath.Join(output, "scenario", "http.qmd"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(httpData), `widths="6,8,40,23,23"`) {
		t.Fatalf("HTTP widths are incorrect:\n%s", httpData)
	}
	if strings.Contains(string(httpData), "Runner") {
		t.Fatalf("HTTP table still contains Runner column:\n%s", httpData)
	}
	expectationData, err := os.ReadFile(filepath.Join(output, "scenario", "expectations.qmd"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(expectationData), "len(current.res.body) == 3") {
		t.Fatalf("expectation details lost assertion:\n%s", expectationData)
	}
	if !strings.Contains(string(expectationData), "::: {.landscape}\n::: {.tbl") {
		t.Fatalf("expectation table is not landscape:\n%s", expectationData)
	}
}

func TestGenerateShowsJSONDetailOnlyWhenRequested(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"suite.yml": `steps:
  created:
    include:
      path: template.yml
      vars:
        case: json://case.json
        request: json://request.json
        expected: json://response.json
`,
		"template.yml": `runners:
  req: {endpoint: http://example.test}
steps:
  call:
    req:
      /books:
        post:
          body:
            application/json: "{{ vars.request }}"
    test: current.res.status == vars.case.expect.status && compare(current.res.body, vars.expected)
`,
		"case.json":     `{"expect":{"status":201}}`,
		"request.json":  `{"title":"Example"}`,
		"response.json": `{"id":"B1"}`,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, tt := range []struct {
		name       string
		showDetail bool
		wantTable  bool
	}{
		{name: "default filenames"},
		{name: "requested detail", showDetail: true, wantTable: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			output := filepath.Join(dir, strings.ReplaceAll(tt.name, " ", "-"))
			_, err := Generate(context.Background(), Options{
				RunbookPaths: []string{filepath.Join(dir, "suite.yml")},
				OutputDir:    output, ShowJSONDetail: tt.showDetail,
			})
			if err != nil {
				t.Fatal(err)
			}
			read := func(name string) string {
				data, err := os.ReadFile(filepath.Join(output, "suite", name))
				if err != nil {
					t.Fatal(err)
				}
				return string(data)
			}
			scenario := read("scenario.qmd")
			request := read("request-json.qmd")
			expected := read("expectations.qmd")
			if tt.wantTable {
				if !strings.Contains(scenario, "@tbl-req-") || !strings.Contains(scenario, "@tbl-exp-") ||
					!strings.Contains(request, "::: {.tbl") || !strings.Contains(expected, "::: {.tbl") {
					t.Fatalf("JSON detail tables missing: %s\n%s\n%s", scenario, request, expected)
				}
			} else {
				for _, want := range []string{"request.json", "response.json", `期待値：\[201\]`} {
					if !strings.Contains(scenario, want) {
						t.Fatalf("missing %q in scenario: %s", want, scenario)
					}
				}
				if strings.Contains(scenario, "@tbl-req-") || strings.Contains(scenario, "@tbl-exp-") ||
					strings.Contains(request, "::: {.tbl") || strings.Contains(expected, "::: {.tbl") {
					t.Fatalf("unexpected JSON detail table: %s\n%s\n%s", scenario, request, expected)
				}
			}
		})
	}
}

func TestGenerateLoopSummarizesOnlyComparedResponseBody(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"suite.yml": `vars:
  cases: [json://first.json, json://second.json]
steps:
  run:
    loop: {count: len(vars.cases)}
    include: {path: template.yml}
`,
		"first.json":  `{"name":"first","requestBody":{"id":"A"},"expect":{"status":201,"body":{"id":"A"}}}`,
		"second.json": `{"name":"second","requestBody":{"id":"B"},"expect":{"status":400,"body":{"error":"invalid"}}}`,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, tt := range []struct {
		name      string
		bodyCheck string
		wantBody  bool
	}{
		{name: "status only", bodyCheck: "", wantBody: false},
		{name: "status and body", bodyCheck: " && compare(current.res.body, vars.case.expect.body)", wantBody: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			template := `runners:
  req: {endpoint: http://example.test}
steps:
  call:
    req:
      /books:
        post:
          body:
            application/json: "{{ vars.case.requestBody }}"
    test: current.res.status == vars.case.expect.status` + tt.bodyCheck + "\n"
			if err := os.WriteFile(filepath.Join(dir, "template.yml"), []byte(template), 0o644); err != nil {
				t.Fatal(err)
			}
			output := filepath.Join(dir, strings.ReplaceAll(tt.name, " ", "-"))
			if _, err := Generate(context.Background(), Options{
				RunbookPaths: []string{filepath.Join(dir, "suite.yml")}, OutputDir: output,
			}); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(filepath.Join(output, "suite", "cases.qmd"))
			if err != nil {
				t.Fatal(err)
			}
			cases := string(data)
			for _, want := range []string{"first.json（requestBody）", "second.json（requestBody）"} {
				if !strings.Contains(cases, want) {
					t.Fatalf("cases missing request %q:\n%s", want, cases)
				}
			}
			wantCount := 0
			if tt.wantBody {
				wantCount = 2
			}
			if got := strings.Count(cases, "expect.body"); got != wantCount {
				t.Fatalf("cases body reference count = %d, want %d:\n%s", got, wantCount, cases)
			}
			scenarioData, err := os.ReadFile(filepath.Join(output, "suite", "scenario.qmd"))
			if err != nil {
				t.Fatal(err)
			}
			scenario := string(scenarioData)
			if !strings.Contains(scenario, `期待値：\[ケース別\]`) || strings.Contains(scenario, "@tbl-exp-") {
				t.Fatalf("scenario status/detail mismatch:\n%s", scenario)
			}
			if got := strings.Count(scenario, "expect.body"); got != wantCount {
				t.Fatalf("scenario body reference count = %d, want %d:\n%s", got, wantCount, scenario)
			}
		})
	}
}

func TestGenerateRequiresForceForExistingOutput(t *testing.T) {
	dir := t.TempDir()
	runbook := filepath.Join(dir, "scenario.yml")
	content := `desc: health
runnora:
  id: HEALTH-001
runners:
  req: {endpoint: http://example.test}
steps:
  health:
    req:
      /health:
        get: {body: null}
    test: current.res.status == 200
`
	if err := os.WriteFile(runbook, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	opts := Options{RunbookPaths: []string{runbook}, OutputDir: filepath.Join(dir, "generated")}
	if _, err := Generate(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(opts.OutputDir, "health-001", "scenario.qmd")); err != nil {
		t.Fatalf("ID-based output is missing: %v", err)
	}
	if _, err := Generate(context.Background(), opts); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("second generation error = %v, want --force guidance", err)
	}
	opts.Force = true
	if _, err := Generate(context.Background(), opts); err != nil {
		t.Fatalf("force generation failed: %v", err)
	}
}

func TestGenerateRejectsCollidingNormalizedRunnoraIDs(t *testing.T) {
	dir := t.TempDir()
	for name, id := range map[string]string{"first.yml": "A_B", "second.yml": "A-B"} {
		content := "runnora:\n  id: " + id + "\nsteps:\n  check:\n    test: true\n"
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out := filepath.Join(dir, "generated")
	_, err := Generate(context.Background(), Options{
		RunbookPaths: []string{filepath.Join(dir, "first.yml"), filepath.Join(dir, "second.yml")},
		OutputDir:    out,
	})
	if err == nil || !strings.Contains(err.Error(), `同じ生成コマンド内で出力フォルダ名 "a-b" が重複します`) {
		t.Fatalf("expected normalized ID collision, got %v", err)
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatalf("output was written before collision check: %v", err)
	}
}

func TestGenerateSameFilenameWithDistinctRunnoraIDs(t *testing.T) {
	dir := t.TempDir()
	var paths []string
	for _, id := range []string{"ORD-001", "USR-001"} {
		folder := filepath.Join(dir, id)
		if err := os.MkdirAll(folder, 0o755); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(folder, "runbook.yml")
		content := "runnora:\n  id: " + id + "\nrunners:\n  req: {endpoint: http://example.test}\nsteps:\n  create:\n    req:\n      /items:\n        post:\n          body:\n            application/json: {name: item}\n    test: current.res.status == 200\n"
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, path)
	}
	out := filepath.Join(dir, "generated")
	opts := Options{RunbookPaths: paths, OutputDir: out, ShowJSONDetail: true}
	if _, err := Generate(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"ORD-001", "USR-001"} {
		for file, labels := range map[string][]string{
			"scenario.qmd":     {`label="tbl-scn-` + id + `"`, "@tbl-http-" + id, "@tbl-req-" + id + ":create", "@tbl-exp-" + id + ":create"},
			"http.qmd":         {`label="tbl-http-` + id + `"`},
			"request-json.qmd": {`label="tbl-req-` + id + `:create"`},
			"expectations.qmd": {`label="tbl-exp-` + id + `:create"`},
		} {
			data, err := os.ReadFile(filepath.Join(out, strings.ToLower(id), file))
			if err != nil {
				t.Fatal(err)
			}
			for _, label := range labels {
				if !strings.Contains(string(data), label) {
					t.Errorf("%s/%s missing %s", id, file, label)
				}
			}
		}
	}
	// Duplicate explicit IDs must fail before any output is written.
	data, err := os.ReadFile(paths[1])
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths[1], []byte(strings.ReplaceAll(string(data), "USR-001", "ORD-001")), 0o644); err != nil {
		t.Fatal(err)
	}
	opts.OutputDir = filepath.Join(dir, "duplicate")
	if _, err := Generate(context.Background(), opts); err == nil || !strings.Contains(err.Error(), "重複") {
		t.Fatalf("duplicate ID error = %v", err)
	}
	if _, err := os.Stat(opts.OutputDir); !os.IsNotExist(err) {
		t.Fatalf("output written on duplicate: %v", err)
	}
}
