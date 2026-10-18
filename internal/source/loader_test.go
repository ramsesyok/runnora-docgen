package source

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ramsesyok/runnora-docgen/internal/model"
)

func TestLoadSuiteResolvesTemplateCaseAndStatus(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "suite.yml"), `desc: Create user suite
vars:
  cases:
    - json://case.json
steps:
  run_case:
    include:
      path: template.yml
`)
	writeTestFile(t, filepath.Join(dir, "template.yml"), `desc: Create user
runners:
  req:
    endpoint: http://example.test
vars:
  case: {}
steps:
  create:
    req:
      /users:
        post:
          body:
            application/json: "{{ vars.case.requestBody }}"
    test: current.res.status == vars.case.expect.status
`)
	writeTestFile(t, filepath.Join(dir, "case.json"), `{
  "name": "valid",
  "requestBody": {"name": "Alice"},
  "expect": {"status": 201}
}`)

	scenario, warnings, err := Load(filepath.Join(dir, "suite.yml"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v", warnings)
	}
	if len(scenario.Steps) != 1 || scenario.Steps[0].Kind != model.StepHTTP {
		t.Fatalf("steps = %#v", scenario.Steps)
	}
	if got, want := scenario.Steps[0].Status.Value, "201"; got != want {
		t.Fatalf("status = %q, want %q", got, want)
	}
	if got, want := scenario.Cases[0].Name, "valid"; got != want {
		t.Fatalf("case name = %q, want %q", got, want)
	}
}

func TestLoadPreservesLoopOwnerForIncludedRows(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "main.yml"), `steps:
  retry:
    loop: {count: 3, until: current.steps.call.res.status == 200}
    include: {path: part.yml}
`)
	writeTestFile(t, filepath.Join(dir, "part.yml"), `steps:
  call:
    test: true
`)
	scenario, _, err := Load(filepath.Join(dir, "main.yml"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(scenario.Steps) != 1 || scenario.Steps[0].Key != "retry.call" || !reflect.DeepEqual(scenario.Steps[0].LoopKeys, []string{"retry"}) {
		t.Fatalf("loop owner: %+v", scenario.Steps)
	}
}

func TestLoadDetectsServerStreamingFromProto(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "service.proto"), `syntax = "proto3";
package example.v1;
service Updates { rpc List(Request) returns (stream Response); }
message Request { string topic = 1; }
message Response { string value = 1; }
`)
	writeTestFile(t, filepath.Join(dir, "runbook.yml"), `desc: updates
runners:
  greq:
    addr: localhost:50051
    protos: [service.proto]
steps:
  list:
    greq:
      example.v1.Updates/List:
        message: {topic: releases}
    test: current.res.status == 0 && len(current.res.messages) == 3
`)

	scenario, warnings, err := Load(filepath.Join(dir, "runbook.yml"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v", warnings)
	}
	if got, want := scenario.Steps[0].GRPC.RPCType, model.RPCServerStreaming; got != want {
		t.Fatalf("RPC type = %q, want %q", got, want)
	}
}

func TestLoadHooksUsesBaseDirAndRunnoraOrder(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "runbook.yml"), `desc: health
runners:
  req: {endpoint: http://example.test}
steps:
  health:
    req:
      /health:
        get: {body: null}
    test: current.res.status == 200
`)
	writeTestFile(t, filepath.Join(dir, "config.yml"), `hooks:
  common:
    before: [common-before.sql]
    after: [common-after.sql]
`)
	for _, name := range []string{"common-before.sql", "extra-before.sql", "common-after.sql", "extra-after.sql"} {
		writeTestFile(t, filepath.Join(dir, name), "-- "+name)
	}

	scenario, _, err := Load(filepath.Join(dir, "runbook.yml"), Options{
		BaseDir:    dir,
		ConfigPath: "config.yml",
		BeforeSQL:  []string{"extra-before.sql"},
		AfterSQL:   []string{"extra-after.sql"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := filepath.Base(scenario.BeforeHooks[0].Path), "common-before.sql"; got != want {
		t.Fatalf("first before = %q, want %q", got, want)
	}
	if got, want := filepath.Base(scenario.BeforeHooks[1].Path), "extra-before.sql"; got != want {
		t.Fatalf("second before = %q, want %q", got, want)
	}
	if got, want := filepath.Base(scenario.AfterHooks[0].Path), "extra-after.sql"; got != want {
		t.Fatalf("first after = %q, want %q", got, want)
	}
	if got, want := filepath.Base(scenario.AfterHooks[1].Path), "common-after.sql"; got != want {
		t.Fatalf("second after = %q, want %q", got, want)
	}
	if len(scenario.Sources) != 6 { // runbook, config, and four SQL files
		t.Fatalf("sources = %d, want 6: %#v", len(scenario.Sources), scenario.Sources)
	}
}

func TestSlugMakesDistinctIDsForJapaneseNames(t *testing.T) {
	first := slug("ユーザー登録")
	second := slug("ユーザー削除")
	if first == second || first == "scenario" || second == "scenario" {
		t.Fatalf("unexpected slugs: %q, %q", first, second)
	}
}

func TestOutputIDIsSafeAsADirectoryName(t *testing.T) {
	for input, want := range map[string]string{
		"ORD-001":    "ord-001",
		"../ORD_001": "ord-001",
		"CON":        "scenario-con",
	} {
		if got := OutputID(input); got != want {
			t.Errorf("OutputID(%q) = %q, want %q", input, got, want)
		}
	}
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadSplitsQueryFromHTTPPath(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "runbook.yml"), `desc: search
runners:
  req:
    endpoint: http://example.test
steps:
  search:
    req:
      /books?genre=NOVEL&availableOnly=true:
        get:
          headers:
            X-Test-Scenario: LIB-000
          body: null
  plain:
    req:
      /books/B0001:
        get:
          body: null
`)
	scenario, _, err := Load(filepath.Join(dir, "runbook.yml"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	search, plain := scenario.Steps[0].HTTP, scenario.Steps[1].HTTP
	if search.Path != "/books" || search.Query != "genre=NOVEL&availableOnly=true" {
		t.Fatalf("search path/query = %q / %#v", search.Path, search.Query)
	}
	if headers, ok := search.Headers.(map[string]any); !ok || headers["X-Test-Scenario"] != "LIB-000" {
		t.Fatalf("headers = %#v", search.Headers)
	}
	if plain.Path != "/books/B0001" || plain.Query != nil {
		t.Fatalf("plain path/query = %q / %#v", plain.Path, plain.Query)
	}
}
