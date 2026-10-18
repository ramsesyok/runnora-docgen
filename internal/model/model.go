package model

// Scenario is the format-neutral document model. Parsing runn syntax and
// rendering a particular table layout are deliberately kept separate.
type Scenario struct {
	ID          string
	Name        string
	SourcePath  string
	SourceHash  string
	Steps       []Step
	Cases       []Case
	BeforeHooks []Asset
	AfterHooks  []Asset
	Sources     []SourceRef
}

type Step struct {
	Number int
	ID     string
	// Key は runnora の report.json と証跡のファイル名で使うステップのキー。
	// include 先は呼び出したステップのキーと . でつなぐ (inc.call)。steps が配列なら 0 始まりの添字。
	Key string
	// Loop は runnora の実行で回ごとに結果が分かれるステップ (自身か呼び出したステップに loop がある)。
	Loop bool
	// LoopKeys はこの行を囲む loop ステップのキー。include 先の行でも loop.until の失敗を追えるようにする。
	LoopKeys             []string
	Description          string
	Kind                 StepKind
	HTTP                 *HTTPRequest
	GRPC                 *GRPCRequest
	DBQuery              string
	ExecCommand          string
	Bind                 map[string]string
	Test                 string
	Status               StatusExpectation
	RequestJSONFiles     []string
	ExpectationJSONFiles []string
	RequestJSONRefs      []JSONRef
	ResponseBodyJSONRefs []JSONRef
	HasResponseBodyCheck bool
	RequestJSONData      []JSONData
	ExpectationJSONData  []JSONData
	SourcePath           string
	SourceLine           int
}

type JSONData struct {
	Path  string
	Value any
}

// JSONRef identifies the part of a source file used as a request or an
// expected response body. An empty FieldPath means the whole file is used.
type JSONRef struct {
	Path      string
	FieldPath string
}

type StepKind string

const (
	StepHTTP    StepKind = "http"
	StepGRPC    StepKind = "grpc"
	StepDB      StepKind = "db"
	StepExec    StepKind = "exec"
	StepBind    StepKind = "bind"
	StepInclude StepKind = "include"
	StepTest    StepKind = "test"
	StepUnknown StepKind = "unknown"
)

type HTTPRequest struct {
	Runner      string
	Endpoint    string
	Method      string
	Path        string
	Headers     any
	Query       any
	Body        any
	ContentType string
}

type GRPCRequest struct {
	Runner  string
	Address string
	Method  string
	RPCType RPCType
	Headers any
	Message any
	Timeout string
}

type RPCType string

const (
	RPCUnary           RPCType = "Unary"
	RPCServerStreaming RPCType = "Server streaming"
	RPCUnsupported     RPCType = "Unsupported streaming"
	RPCUnknown         RPCType = "Unknown"
)

type StatusExpectation struct {
	Protocol string
	Value    string
	Variable string
}

type Case struct {
	ID          string
	Name        string
	Description string
	SourcePath  string
	Data        map[string]any
	Expectation map[string]any
	SourceHash  string
}

type Asset struct {
	Path    string
	Content string
	Origin  string
	SHA256  string
}

type SourceRef struct {
	Kind   string `json:"kind"`
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}
