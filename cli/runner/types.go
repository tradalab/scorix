package runner

type GenerateProtoOptions struct {
	Proto string
	Dir   string
	Force bool
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
	RPCs        []protoRPC
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
}

type protoTemplateData struct {
	Module   string
	Proto    protoFile
	Services []protoService
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
