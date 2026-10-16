package render

import (
	"strings"
	"testing"

	"github.com/ramsesyok/runnora-docgen/internal/model"
)

func TestScenarioReviewerReferences(t *testing.T) {
	scenario := &model.Scenario{ID: "review", Steps: []model.Step{{
		ID: "create", Number: 3, Kind: model.StepHTTP,
		HTTP:                 &model.HTTPRequest{Body: map[string]any{}},
		Test:                 "current.res.status == 201",
		Status:               model.StatusExpectation{Protocol: "HTTP", Value: "201"},
		RequestJSONFiles:     []string{"fixtures/create.json", "fixtures/create.json"},
		ExpectationJSONFiles: []string{`fixtures\response.json`},
	}}}
	got := renderScenario(scenario)
	for _, want := range []string{
		"URL：[@" + label("review", "http") + `]-\[3\]`,
		"@" + label("review", "request", "create") + "\\",
		`期待値：\[201\]`, "@" + label("review", "expect", "create") + "\\",
		"create.json", "response.json",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "ステータス：") || strings.Contains(got, "fixtures") || strings.Count(got, "create.json") != 1 {
		t.Fatalf("unexpected legacy status, directory, or duplicate filename:\n%s", got)
	}
}

func TestScenarioExpectationWithoutLiteralStatus(t *testing.T) {
	for _, tt := range []struct{ name, protocol, value, variable, want string }{
		{"no status", "", "", "", "期待値：@"},
		{"variable", "HTTP", "", "vars.status", `期待値：\[vars.status\]`},
		{"cases", "HTTP", "ケース別", "", `期待値：\[ケース別\]`},
		{"grpc", "gRPC", "0", "", `期待値：\[gRPC 0\]`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := renderScenario(&model.Scenario{ID: "review", Steps: []model.Step{{
				ID: "check", Number: 1, Kind: model.StepTest, Test: "check",
				Status: model.StatusExpectation{Protocol: tt.protocol, Value: tt.value, Variable: tt.variable},
			}}})
			if !strings.Contains(got, tt.want) || strings.Contains(got, ".json") || strings.Contains(got, "[200]") {
				t.Fatalf("unexpected expectation:\n%s", got)
			}
		})
	}
}

func TestScenarioWrapsIndexedStatusVariable(t *testing.T) {
	scenario := &model.Scenario{ID: "review", Steps: []model.Step{{
		ID: "check", Number: 1, Kind: model.StepTest,
		Test:   "current.res.status == vars.case13.expect[i].status",
		Status: model.StatusExpectation{Protocol: "HTTP", Variable: "vars.case13.expect[i].status"},
	}}}
	for _, filenames := range []bool{false, true} {
		got := renderScenarioWithDetail(scenario, filenames)
		if !strings.Contains(got, `期待値：\[vars.case13.\`) || !strings.Contains(got, `expect\[i\].status\]`) {
			t.Fatalf("status variable was not wrapped at a path boundary (filenames=%v):\n%s", filenames, got)
		}
		if !strings.Contains(got, fullBorder([]int{8, 54, 24, 30, 8}, '-')) {
			t.Fatalf("status variable widened the scenario table (filenames=%v):\n%s", filenames, got)
		}
	}
}

func TestScenarioWithoutTestUsesDash(t *testing.T) {
	scenario := &model.Scenario{ID: "no-test", Steps: []model.Step{
		{ID: "request", Number: 1, Kind: model.StepHTTP, HTTP: &model.HTTPRequest{}},
		{ID: "bind", Number: 2, Kind: model.StepBind, Bind: map[string]string{"z": "steps.request.res.body.id", "a": "vars.case.name"}},
	}}
	for _, filenames := range []bool{false, true} {
		got := renderScenarioWithDetail(scenario, filenames)
		if strings.Count(got, "| ー ") != 2 {
			t.Fatalf("missing expected dashes (filenames=%v):\n%s", filenames, got)
		}
		if !strings.Contains(got, "変数設定：") || !strings.Contains(got, "a = vars.case.name") || !strings.Contains(got, "z = steps.request.res.body.id") || strings.Contains(got, "`a =") {
			t.Fatalf("bind expressions missing (filenames=%v):\n%s", filenames, got)
		}
		lines := strings.Split(got, "\n")
		for i, line := range lines {
			if strings.Contains(line, "変数設定：") {
				if i+1 >= len(lines) || !strings.Contains(lines[i+1], "a = vars.case.name") {
					t.Fatalf("bind expression is not on the next line (filenames=%v):\n%s", filenames, got)
				}
				break
			}
		}
		if strings.Index(got, "a = vars.case.name") > strings.Index(got, "z = steps.request.res.body.id") {
			t.Fatalf("bind order is unstable:\n%s", got)
		}
	}
}

func TestExecScenarioShowsCommandAndTest(t *testing.T) {
	scenario := &model.Scenario{ID: "exec", Steps: []model.Step{{
		ID: "compare", Number: 1, Kind: model.StepExec,
		ExecCommand: "runnora-diff expected.json -",
		Test:        `current.exit_code == 0 && current.stderr == ""`,
	}}}
	got := renderScenarioWithDetail(scenario, true)
	for _, want := range []string{"コマンド：runnora-diff expected.json -", "期待値：", "current.exit\\_code == 0 &&", `current.stderr == ""`} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "`") {
		t.Fatalf("command or expectation is formatted as inline code:\n%s", got)
	}
}

func TestLongVariableReferencesBreakAtPathBoundaries(t *testing.T) {
	value := "steps.create_order.res.body.customer_account_identifier"
	got := wrapExpression("order_id = "+value, 28)
	if !strings.Contains(got, "\\\n") || strings.ReplaceAll(got, "\\\n", "") != "order_id = "+value {
		t.Fatalf("variable reference was not wrapped intact: %q", got)
	}
	for _, line := range strings.Split(got, "\\\n") {
		if displayWidth(line) > 28 {
			t.Fatalf("expression line is too wide: %q", line)
		}
	}
	quoted := `current.stdout == "version_1.2"`
	if got := wrapExpression(quoted, 12); strings.Contains(got, `version_\`) || strings.Contains(got, `1.\`) {
		t.Fatalf("quoted value was split: %q", got)
	}
}

func TestBreakConditionsPreservesQuotedLogicalOperators(t *testing.T) {
	got := breakConditions(`current.stdout == "a||b" || current.exit_code == 0`)
	if got != `current.stdout == "a||b" ||\`+"\n"+`current.exit_code == 0` {
		t.Fatalf("logical OR was not broken correctly: %q", got)
	}
}

func TestHooksListFileNamesWithoutSQLBody(t *testing.T) {
	scenario := &model.Scenario{
		ID: "hooks", Name: "前後処理",
		BeforeHooks: []model.Asset{
			{Path: "sql/common/00_reset.sql", Content: "BEGIN\n  DELETE FROM loans;\nEND;"},
			{Path: "sql/cases/setup.sql", Content: "BEGIN NULL; END;"},
		},
		AfterHooks: []model.Asset{{Path: "sql/common/90_verify.sql", Content: "BEGIN NULL; END;"}},
	}
	before := renderHooks(scenario, true)
	for _, want := range []string{`widths="6,34,60"`, "順序", "ファイル", "出典", "00_reset.sql", "sql/common/00_reset.sql", "setup.sql", "sql/cases/setup.sql"} {
		if !strings.Contains(before, want) {
			t.Fatalf("missing %q:\n%s", want, before)
		}
	}
	if strings.Contains(before, "SQL/PLSQL") || strings.Contains(before, "DELETE FROM") || strings.Contains(before, "BEGIN") {
		t.Fatalf("SQL body must not be rendered:\n%s", before)
	}
	if strings.Index(before, "00_reset.sql") > strings.Index(before, "setup.sql") {
		t.Fatalf("hooks must keep execution order:\n%s", before)
	}
	after := renderHooks(scenario, false)
	if !strings.Contains(after, "90_verify.sql") || strings.Contains(after, "BEGIN") {
		t.Fatalf("unexpected after hooks table:\n%s", after)
	}
	if renderHooks(&model.Scenario{ID: "empty"}, true) != "" {
		t.Fatal("no hooks should render nothing")
	}
}

func TestHTTPTableShowsQueryPerLine(t *testing.T) {
	scenario := &model.Scenario{ID: "query", Steps: []model.Step{{
		ID: "search", Number: 1, Kind: model.StepHTTP,
		HTTP: &model.HTTPRequest{Endpoint: "http://example.test", Method: "GET", Path: "/books",
			Query: "genre=NOVEL&availableOnly=&optional=null", Headers: map[string]any{"X-Test-Case": "search", "X-Values": "one,two"}},
	}}}
	got := renderHTTP(scenario)
	for _, want := range []string{`widths="6,8,40,23,23"`, "http://example.test/books", `genre=NOVEL\`, "availableOnly=", "optional=null", `"X-Test-Case":"search",\`, `"X-Values":"one,two"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "/books?") || strings.Contains(got, `{"X-Test-Case"`) {
		t.Fatalf("query must not stay in the URL column:\n%s", got)
	}
}

func TestExpectationConditionBreaksAndJSONLabel(t *testing.T) {
	scenario := &model.Scenario{ID: "expect", Steps: []model.Step{{
		ID: "check", Number: 1,
		Test:                `current.res.status == 200 && current.res.body.note == "a&&b" && current.res.body.ok`,
		ExpectationJSONData: []model.JSONData{{Path: "fixtures/expect.json", Value: map[string]any{"ok": true}}},
	}}}
	got := renderExpectations(scenario)
	for _, want := range []string{`widths="10,20,62"`, "期待値JSON", "expect.json", "200 &&", `"a&&b" &&`} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "`") {
		t.Fatalf("expectation is formatted as inline code:\n%s", got)
	}
	if strings.Contains(got, `"a&\`) {
		t.Fatalf("a string literal was split:\n%s", got)
	}
}
