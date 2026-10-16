package source

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ramsesyok/runnora-docgen/internal/model"
)

func TestLoadIncludeVarsResolveJSONFromIncludedRunbook(t *testing.T) {
	dir := t.TempDir()
	// json:// in include vars is resolved relative to the included runbook (as runn does).
	writeNestedFile(t, filepath.Join(dir, "suite", "contract.suite.yml"), `desc: Contract suite
steps:
  found:
    desc: Registered user
    include:
      path: ../tpl/get_user.template.yml
      vars:
        case: json://../cases/01_found.json
        expected: json://../responses/user.json
  missing:
    include:
      path: ../tpl/get_user.template.yml
      vars:
        case: json://../cases/02_missing.json
        expected: json://../responses/not_found.json
`)
	writeNestedFile(t, filepath.Join(dir, "tpl", "get_user.template.yml"), `desc: Get user template
runners:
  req:
    endpoint: http://example.test
vars:
  case: {}
  expected: {}
steps:
  call_api:
    req:
      /users/{{ vars.case.pathParams.id }}?mode={{ vars.case.queryParams.mode }}:
        post:
          headers: "{{ vars.case.headers }}"
          body:
            application/json: "{{ vars.case.requestBody }}"
    test: |
      current.res.status == vars.case.expect.status &&
      compare(current.res.body, vars.expected, vars.case.expect.ignorePaths)
`)
	writeNestedFile(t, filepath.Join(dir, "cases", "01_found.json"), `{
  "name": "found",
  "pathParams": {"id": "U1"},
  "queryParams": {"mode": "full"},
  "headers": {},
  "requestBody": {"name": "Alice"},
  "expect": {"status": 200, "mockCase": "getUser_U1", "ignorePaths": [".updatedAt"]}
}`)
	writeNestedFile(t, filepath.Join(dir, "cases", "02_missing.json"), `{
  "name": "missing",
  "pathParams": {"id": "U9"},
  "queryParams": {"mode": "full"},
  "headers": {},
  "requestBody": {},
  "expect": {"status": 404, "ignorePaths": []}
}`)
	writeNestedFile(t, filepath.Join(dir, "responses", "user.json"), `{"id": "U1", "name": "Alice"}`)
	writeNestedFile(t, filepath.Join(dir, "responses", "not_found.json"), `{"code": "NOT_FOUND"}`)

	scenario, warnings, err := Load(filepath.Join(dir, "suite", "contract.suite.yml"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v", warnings)
	}
	if len(scenario.Steps) != 2 {
		t.Fatalf("steps = %d", len(scenario.Steps))
	}
	found, missing := scenario.Steps[0], scenario.Steps[1]

	if found.Description != "Registered user" || missing.Description != "call_api" {
		t.Fatalf("descriptions = %q, %q", found.Description, missing.Description)
	}
	if found.HTTP.Path != "/users/U1" || missing.HTTP.Path != "/users/U9" {
		t.Fatalf("paths = %q, %q", found.HTTP.Path, missing.HTTP.Path)
	}
	if found.HTTP.Query != "mode=full" {
		t.Fatalf("query = %#v", found.HTTP.Query)
	}
	if body, ok := found.HTTP.Body.(map[string]any); !ok || body["name"] != "Alice" {
		t.Fatalf("body = %#v", found.HTTP.Body)
	}
	if found.Status.Value != "200" || missing.Status.Value != "404" {
		t.Fatalf("statuses = %q, %q", found.Status.Value, missing.Status.Value)
	}
	assertBaseNames(t, "request files", found.RequestJSONFiles, "01_found.json")
	assertBaseNames(t, "expectation files", found.ExpectationJSONFiles, "01_found.json", "user.json")

	// The expected response file is shown whole; the case file only with the fields the test reads.
	if len(found.ExpectationJSONData) != 2 {
		t.Fatalf("expectation data = %#v", found.ExpectationJSONData)
	}
	byName := map[string]model.JSONData{}
	for _, data := range found.ExpectationJSONData {
		byName[filepath.Base(data.Path)] = data
	}
	caseData, _ := byName["01_found.json"].Value.(map[string]any)
	expect, _ := caseData["expect"].(map[string]any)
	if expect["status"] != float64(200) || expect["ignorePaths"] == nil || expect["mockCase"] != nil || caseData["requestBody"] != nil {
		t.Fatalf("case data should contain only referenced fields: %#v", caseData)
	}
	if user, _ := byName["user.json"].Value.(map[string]any); user["name"] != "Alice" {
		t.Fatalf("expected file data = %#v", byName["user.json"].Value)
	}

	sources := map[string]bool{}
	for _, ref := range scenario.Sources {
		sources[filepath.Base(ref.Path)] = true
	}
	for _, name := range []string{"get_user.template.yml", "01_found.json", "user.json", "02_missing.json", "not_found.json"} {
		if !sources[name] {
			t.Fatalf("source %s is not recorded: %#v", name, scenario.Sources)
		}
	}
}

func TestLoadIncludeVarsDisplaysNullQueryValues(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "suite.yml"), `steps:
  scenario:
    include:
      path: template.yml
      vars:
        case: json://case.json
`)
	writeTestFile(t, filepath.Join(dir, "template.yml"), `runners:
  req:
    endpoint: http://example.test
steps:
  embedded:
    req:
      /books?name={{ vars.case.queryParams.name }}&page={{ vars.case.queryParams.page }}:
        get: {}
  whole:
    req:
      /books:
        get:
          query: "{{ vars.case.queryParams.name }}"
  mapping:
    req:
      /books:
        get:
          query:
            name: "{{ vars.case.queryParams.name }}"
            inline: "name={{ vars.case.queryParams.name }}"
  missing:
    req:
      /books?name={{ vars.case.queryParams.absent }}:
        get: {}
`)
	writeTestFile(t, filepath.Join(dir, "case.json"), `{"queryParams":{"name":null,"page":2}}`)

	scenario, _, err := Load(filepath.Join(dir, "suite.yml"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got := scenario.Steps[0].HTTP.Query; got != "name=null&page=2" {
		t.Fatalf("embedded query = %#v", got)
	}
	if got := scenario.Steps[1].HTTP.Query; got != "null" {
		t.Fatalf("whole query = %#v", got)
	}
	mapping := scenario.Steps[2].HTTP.Query.(map[string]any)
	if mapping["name"] != nil || mapping["inline"] != "name=null" {
		t.Fatalf("mapping query = %#v", mapping)
	}
	if got := scenario.Steps[3].HTTP.Query; got != "name={{ vars.case.queryParams.absent }}" {
		t.Fatalf("missing variable query = %#v", got)
	}
}

func TestLoadIncludeVarsRejectsMissingJSON(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "suite.yml"), `desc: suite
steps:
  one:
    include:
      path: template.yml
      vars:
        expected: json://missing.json
`)
	writeTestFile(t, filepath.Join(dir, "template.yml"), `desc: template
steps:
  check:
    test: "true"
`)
	if _, _, err := Load(filepath.Join(dir, "suite.yml"), Options{}); err == nil {
		t.Fatal("missing include JSON should be an error")
	}
}

func TestTraceHTTPStepReportsReferenceOriginsWithoutValues(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "runbook.yml"), `steps:
  scenario:
    include:
      path: scenario.yml
      vars:
        case2: json://case2.json
`)
	writeTestFile(t, filepath.Join(dir, "scenario.yml"), `runners:
  req:
    endpoint: http://example.test
steps:
  setup:
    bind:
      upload: multipart()
  case2:
    req:
      /books/{{ vars.case2.pathParams.id }}?id={{ vars.case2.queryParams.id }}:
        get:
          headers: "{{ vars.case2.headers }}"
`)
	writeTestFile(t, filepath.Join(dir, "case2.json"), `{"pathParams":{"id":"B2"},"queryParams":{"id":"{{ vars.case1.queryParams.id }}"},"headers":"{{ vars.case1.headers }}"}`)
	_, warnings, err := Load(filepath.Join(dir, "runbook.yml"), Options{TraceHTTPStep: 2})
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(warnings, "\n")
	for _, want := range []string{
		"HTTP 手順 2 (scenario.case2",
		"Query: 読込時=[vars.case2.queryParams.id]、生成時に残る参照=[vars.case1.queryParams.id]",
		"Headers: 読込時=[vars.case2.headers]、生成時に残る参照=[vars.case1.headers]",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("trace missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "B2") {
		t.Fatalf("trace contains a resolved value:\n%s", got)
	}
}

func assertBaseNames(t *testing.T, what string, paths []string, want ...string) {
	t.Helper()
	got := map[string]bool{}
	for _, path := range paths {
		got[filepath.Base(path)] = true
	}
	if len(got) != len(want) {
		t.Fatalf("%s = %v, want %v", what, paths, want)
	}
	for _, name := range want {
		if !got[name] {
			t.Fatalf("%s = %v, want %v", what, paths, want)
		}
	}
}

func writeNestedFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, path, content)
}

func TestLoadIncludeVarsKeepsDeclaredOrderInSources(t *testing.T) {
	dir := t.TempDir()
	// Declared order differs from alphabetical order on purpose.
	writeTestFile(t, filepath.Join(dir, "suite.yml"), `desc: suite
steps:
  one:
    include:
      path: template.yml
      vars:
        zeta: json://z.json
        alpha: json://a.json
        mid: json://m.json
`)
	writeTestFile(t, filepath.Join(dir, "template.yml"), `desc: template
steps:
  check:
    test: vars.zeta.ok && vars.alpha.ok && vars.mid.ok
`)
	for _, name := range []string{"z.json", "a.json", "m.json"} {
		writeTestFile(t, filepath.Join(dir, name), `{"ok": true}`)
	}
	want := []string{"z.json", "a.json", "m.json"}
	// Map iteration order is random, so repeat to catch unstable output.
	for i := 0; i < 20; i++ {
		scenario, _, err := Load(filepath.Join(dir, "suite.yml"), Options{})
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, ref := range scenario.Sources {
			if ref.Kind == "json" {
				got = append(got, filepath.Base(ref.Path))
			}
		}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("run %d: json sources = %v, want %v", i, got, want)
		}
		var files []string
		for _, path := range scenario.Steps[0].ExpectationJSONFiles {
			files = append(files, filepath.Base(path))
		}
		if strings.Join(files, ",") != strings.Join(want, ",") {
			t.Fatalf("run %d: expectation files = %v, want %v", i, files, want)
		}
	}
}
