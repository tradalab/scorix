package runner

type GenerateProtoOptions struct {
	Proto string
	Dir   string
	Force bool
	// Check renders in memory and diffs against disk instead of writing, erroring
	// on drift — CI guard against editing proto (or generated files) without regen.
	Check bool
}

type protoFile struct {
	Package       string
	HasEmpty      bool
	HasMiddleware bool
	Middlewares   []string
	Messages      []protoMessage
	Services      []protoService
}

type protoMessage struct {
	Name   string
	GoName string
	Fields []protoField
}

type protoField struct {
	Name     string
	JSONName string
	GoName   string
	Type     string
	GoType   string
	TSType   string
	Repeated bool
}

type protoService struct {
	Name        string
	Package     string
	Middlewares []string
	RPCs        []protoRPC // request/reply commands only (events are split out)
	Events      []protoRPC // rpcs annotated @event / @event in
}

type protoRPC struct {
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

	// Event fields — set when the rpc carries an @event annotation. The request
	// message is the event payload; the response type is ignored.
	IsEvent     bool
	EventDir    string // "out" (Go -> JS push, default) | "in" (JS -> Go one-way)
	EventName   string // wire topic, e.g. "monitor:message"
	EventGoName string // service-prefixed identifier, e.g. "MonitorMessage"
}

type protoTemplateData struct {
	Module    string
	Proto     protoFile
	Services  []protoService
	OutEvents []protoRPC
	InEvents  []protoRPC
	HasEvents bool
}

type logicTemplateData struct {
	Module  string
	Service protoService
	RPC     protoRPC
}

type middlewareTemplateData struct {
	Module       string
	Name         string
	ExportedName string
}

type generatedFile struct {
	Path     string
	Template string
	Data     any
	Go       bool
	Force    bool
}
