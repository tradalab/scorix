package idl

import "time"

// File is one app.proto read into the model every generator works from.
type File struct {
	// Package is the last segment of the proto package; ProtoPackage is all of it.
	Package       string
	ProtoPackage  string
	HasEmpty      bool
	HasMiddleware bool
	Middlewares   []string
	Messages      []Message
	Services      []Service
	// Natives is empty after Parse; a generator that splits out the @native services puts them
	// here, leaving those marked `@native expose` in Services too.
	Natives []Service
}

type Message struct {
	Name   string
	GoName string
	Fields []Field
}

type Field struct {
	Name     string
	JSONName string
	GoName   string
	Type     string
	GoType   string
	TSType   string
	Repeated bool
	// WireName is the field's protobuf JSON name, the one a network caller sends.
	WireName string
	// Header is the request header a REST route fills this field from.
	Header string
	// Class is "pii", "secret" or empty, from the field's (scorix.v1.field) option.
	Class string
}

// HTTP is an rpc's REST route, from its (scorix.v1.http) option.
type HTTP struct {
	Method string
	Path   string
	// Body is "*" when the request body is the message, empty when the query is.
	Body      string
	Anonymous bool
	// Params are the {name} segments of Path, in order.
	Params []string
}

type Service struct {
	Name        string
	Package     string
	Middlewares []string
	// Parse keeps every rpc in RPCs, events flagged IsEvent; Events is for a caller that splits them out.
	RPCs         []RPC
	Events       []RPC
	Native       bool
	NativeExpose bool
}

type RPC struct {
	Name          string
	LogicName     string
	MethodName    string
	FileName      string
	CommandName   string
	Middlewares   []string
	RequestType   string
	ResponseType  string
	RequestGoType string
	ResultGoType  string
	RequestTSType string
	ResultTSType  string

	// Arity is "unary", "server-stream", "client-stream" or "bidi".
	Arity          string
	IsServerStream bool

	IsEvent     bool
	EventDir    string
	EventName   string
	EventGoName string

	MCP             bool
	MCPDestructive  bool
	MCPToolName     string
	MCPSchema       string
	MCPOutputSchema string
	// MCPTimeout is zero when the rpc keeps mcpcore's default.
	MCPTimeout time.Duration

	CLI            bool
	CLIDestructive bool
	CLIHeadless    bool
	CLIExec        bool
	CLITimeout     time.Duration
	CLIArg         string
	CLIRest        string
	CLISecret      string
	CLISchema      string
	// Doc is the rpc's leading comment without its annotation lines.
	Doc string

	NativeMethod string

	// HTTP is nil for an rpc that has no REST route.
	HTTP *HTTP

	// Effect is "readonly", "idempotent", "mutating", "destructive", or empty when neither
	// the (scorix.v1.rpc) option nor the rpc's name says.
	Effect     string
	Timeout    time.Duration
	Permission string
	// Replay is true when a retry carrying an Idempotency-Key gets the first outcome.
	Replay bool
	// Audit is true when every call is recorded: unless READONLY, or as (scorix.v1.rpc) audit says.
	Audit bool

	// List is nil for an rpc that does not page.
	List *List

	// Legacy names the comment annotations, such as "@mcp", the rpc still uses in place of a trait.
	Legacy []string
}

// List is an rpc's (scorix.v1.list) option.
type List struct {
	Default int32
	Max     int32
	Order   []Order
	// Total is true when the request has want_total and the response total_size.
	Total bool
}

type Order struct {
	Column string
	Desc   bool
}
