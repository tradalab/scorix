package runner

import (
	"context"
	"fmt"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/tradalab/scorix/idl"
	"github.com/tradalab/scorix/internal/cli/template"
)

type GenerateServerOptions struct {
	JSONOut io.Writer
	Proto   string
	PB      string
	Out     string
	Check   bool
}

type serverTemplateData struct {
	Package  string
	PB       string
	Proto    idl.File
	Services []serverService
	HasHTTP  bool
	HasMCP   bool
	HasList  bool
	HasCLI   bool
}

type serverService struct {
	idl.Service
	Calls    []serverCall
	Routes   []serverRoute
	Tools    []serverTool
	Commands []serverCall
}

type serverCall struct {
	idl.RPC
	Rule string
}

type serverTool struct {
	serverCall
	Tool         string
	InputSchema  string
	OutputSchema string
}

func ruleOf(rpc idl.RPC) string {
	var parts []string
	if rpc.Timeout > 0 {
		parts = append(parts, fmt.Sprintf("Timeout: %d", rpc.Timeout.Nanoseconds()))
	}
	if rpc.Permission != "" {
		parts = append(parts, fmt.Sprintf("Permission: %q", rpc.Permission))
	}
	if rpc.Replay {
		parts = append(parts, "Replay: true")
	}
	if rpc.Audit {
		parts = append(parts, "Audit: true")
	}
	return "kernel.Rule{" + strings.Join(parts, ", ") + "}"
}

type serverRoute struct {
	serverCall
	Headers [][2]string
}

func serverTools(pf idl.File, svc idl.Service, taken map[string]string) ([]serverTool, error) {
	var out []serverTool
	for _, rpc := range svc.RPCs {
		if !rpc.MCP {
			continue
		}
		name := idl.LowerCamel(svc.Name) + "_" + idl.KebabName(rpc.Name)
		secret := secretIn(pf, rpc.RequestType, map[string]bool{})
		switch prev, dup := taken[name]; {
		case rpc.Doc == "":
			return nil, fmt.Errorf("proto: rpc %s.%s is @mcp with no comment; an agent picks a tool by its description, so write one above the rpc", svc.Name, rpc.Name)
		case rpc.Effect == "":
			return nil, fmt.Errorf("proto: rpc %s.%s is @mcp with no effect; an agent needs to know whether it changes anything, so set (scorix.v1.rpc).effect", svc.Name, rpc.Name)
		case secret != "":
			return nil, fmt.Errorf("proto: rpc %s.%s is @mcp but its request carries the SECRET field %s; an agent would pass it through the model", svc.Name, rpc.Name, secret)
		case !mcpToolNameRe.MatchString(name):
			return nil, fmt.Errorf("proto: rpc %s.%s would be the MCP tool %q; tool names are 1-64 of [A-Za-z0-9_-]", svc.Name, rpc.Name, name)
		case dup:
			return nil, fmt.Errorf("proto: rpc %s.%s and %s both become the MCP tool %q", svc.Name, rpc.Name, prev, name)
		}
		taken[name] = svc.Name + "." + rpc.Name
		wire := func(f protoField) string { return f.WireName }
		in, err := schemaOf(pf, rpc.RequestType, true, wire)
		if err != nil {
			return nil, err
		}
		res, err := schemaOf(pf, rpc.ResponseType, false, wire)
		if err != nil {
			return nil, err
		}
		out = append(out, serverTool{serverCall: serverCall{RPC: rpc, Rule: ruleOf(rpc)}, Tool: name, InputSchema: in, OutputSchema: res})
	}
	return out, nil
}

func hasList(pf idl.File) bool {
	for _, svc := range pf.Services {
		for _, rpc := range svc.RPCs {
			if rpc.List != nil {
				return true
			}
		}
	}
	return false
}

func secretIn(pf idl.File, message string, seen map[string]bool) string {
	if seen[message] {
		return ""
	}
	seen[message] = true
	for _, m := range pf.Messages {
		if m.Name != message {
			continue
		}
		for _, f := range m.Fields {
			if f.Class == "secret" {
				return m.Name + "." + f.Name
			}
			if found := secretIn(pf, f.Type, seen); found != "" {
				return found
			}
		}
	}
	return ""
}

type goNames map[string]string

func (g goNames) claim(owner string, names ...string) error {
	for _, name := range names {
		if prev, dup := g[name]; dup {
			return fmt.Errorf("proto: %s and %s both generate the Go name %s; rename one", prev, owner, name)
		}
		g[name] = owner
	}
	return nil
}

func serverServices(pf idl.File) ([]serverService, bool, error) {
	headers := map[string][][2]string{}
	for _, m := range pf.Messages {
		for _, f := range m.Fields {
			if f.Header != "" {
				headers[m.Name] = append(headers[m.Name], [2]string{f.Name, f.Header})
			}
		}
	}
	var out []serverService
	hasHTTP := false
	taken := goNames{}
	for _, svc := range pf.Services {
		s := serverService{Service: svc}
		names := []string{svc.Name + "Path", svc.Name + "Handler", "Register" + svc.Name, strings.ToLower(svc.Name[:1]) + svc.Name[1:] + "Client"}
		for _, rpc := range svc.RPCs {
			if rpc.List != nil {
				if err := taken.claim("rpc "+svc.Name+"."+rpc.Name, svc.Name+rpc.Name+"Page"); err != nil {
					return nil, false, err
				}
			}
			call := serverCall{RPC: rpc, Rule: ruleOf(rpc)}
			s.Calls = append(s.Calls, call)
			if rpc.HTTP != nil {
				s.Routes = append(s.Routes, serverRoute{serverCall: call, Headers: headers[rpc.RequestType]})
				hasHTTP = true
			}
			if rpc.CLI {
				s.Commands = append(s.Commands, call)
			}
		}
		if len(s.Routes) > 0 {
			names = append(names, "Register"+svc.Name+"HTTP")
		}
		if len(s.Commands) > 0 {
			names = append(names, "Register"+svc.Name+"CLI")
		}
		if slices.ContainsFunc(svc.RPCs, func(r idl.RPC) bool { return r.MCP }) {
			names = append(names, "Register"+svc.Name+"MCP")
		}
		if err := taken.claim("service "+svc.Name, names...); err != nil {
			return nil, false, err
		}
		out = append(out, s)
	}
	return out, hasHTTP, nil
}

func GenerateServer(ctx context.Context, opt GenerateServerOptions) error {
	res := &GenerateResult{Check: opt.Check, Regen: "scorix generate server"}
	err := generateServer(ctx, opt, res)
	if opt.JSONOut != nil {
		return EmitJSON(opt.JSONOut, "generate server", res, err)
	}
	return err
}

func serviceProto(path, command string) (idl.File, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return idl.File{}, fmt.Errorf("read proto: %w", err)
	}
	pf, err := idl.Parse(filepath.Base(path), string(src))
	if err != nil {
		return idl.File{}, err
	}
	if pf.ProtoPackage == "" {
		return idl.File{}, fmt.Errorf("proto: %s has no package; a network procedure is /<package>.<Service>/<Method>", path)
	}
	for _, svc := range pf.Services {
		for _, rpc := range svc.RPCs {
			switch {
			case token.IsKeyword(rpc.Name):
				return idl.File{}, fmt.Errorf("proto: rpc %s.%s is named with the Go keyword %s, and %s names a Go method after each rpc; rename it", svc.Name, rpc.Name, rpc.Name, command)
			case rpc.IsEvent:
				return idl.File{}, fmt.Errorf("proto: %s.%s is an event; %s serves rpcs only", svc.Name, rpc.Name, command)
			case rpc.Arity != "unary" && rpc.HTTP != nil:
				return idl.File{}, fmt.Errorf("proto: %s.%s is %s and has (scorix.v1.http); REST takes one request and answers once, so drop the route or the stream", svc.Name, rpc.Name, rpc.Arity)
			case rpc.Arity != "unary" && rpc.MCP:
				return idl.File{}, fmt.Errorf("proto: %s.%s is %s and an MCP tool; a tool call takes one input and answers once, so drop the option or the stream", svc.Name, rpc.Name, rpc.Arity)
			case rpc.Arity != "unary" && rpc.List != nil:
				return idl.File{}, fmt.Errorf("proto: %s.%s is %s and pages; a page is one request and one reply, so drop (scorix.v1.list) or the stream", svc.Name, rpc.Name, rpc.Arity)
			case rpc.CLI && (rpc.Arity == "client-stream" || rpc.Arity == "bidi"):
				return idl.File{}, fmt.Errorf("proto: %s.%s is %s and a command; a command reads its flags once, so drop (scorix.v1.cli) or the stream", svc.Name, rpc.Name, rpc.Arity)
			case rpc.CLI && rpc.CLIExec:
				return idl.File{}, fmt.Errorf("proto: %s.%s is a command with exec; exec hands a desktop terminal to a program, which a service command cannot do", svc.Name, rpc.Name)
			}
			if rpc.CLI {
				if err := checkCLI(pf, svc.Name, rpc); err != nil {
					return idl.File{}, err
				}
			}
		}
	}
	return pf, nil
}

type GenerateDispatchOptions struct {
	JSONOut io.Writer
	Proto   string
	PB      string
	Logic   string
	Svc     string
	Out     string
	Check   bool
}

type dispatchTemplateData struct {
	Package string
	PB      string
	Logic   string
	Svc     string
	Proto   idl.File
}

// GenerateDispatch writes the <Service>Server that hands each rpc to logic.New<Rpc>Logic,
// the shape goctl's server scaffold has, so a go-zero service drops goctl without touching logic.
func GenerateDispatch(ctx context.Context, opt GenerateDispatchOptions) error {
	res := &GenerateResult{Check: opt.Check, Regen: "scorix generate dispatch"}
	err := generateDispatch(ctx, opt, res)
	if opt.JSONOut != nil {
		return EmitJSON(opt.JSONOut, "generate dispatch", res, err)
	}
	return err
}

func generateDispatch(ctx context.Context, opt GenerateDispatchOptions, res *GenerateResult) error {
	if opt.Proto == "" || opt.PB == "" || opt.Logic == "" || opt.Svc == "" || opt.Out == "" {
		return fmt.Errorf("generate dispatch needs --proto, --pb, --logic, --svc and --out")
	}
	pf, err := serviceProto(opt.Proto, "generate dispatch")
	if err != nil {
		return err
	}
	if err := dispatchNames(pf); err != nil {
		return err
	}
	out, err := filepath.Abs(opt.Out)
	if err != nil {
		return err
	}
	return emitGenerated(ctx, filepath.Dir(out), opt.Check, res, []generatedFile{{
		Path:     out,
		Template: mustRead(template.GoDispatch),
		Data:     dispatchTemplateData{Package: goPackage(out), PB: opt.PB, Logic: opt.Logic, Svc: opt.Svc, Proto: pf},
		Go:       true,
		Force:    true,
	}})
}

func goPackage(file string) string {
	return strings.ReplaceAll(filepath.Base(filepath.Dir(file)), "-", "")
}

func dispatchNames(pf idl.File) error {
	taken, logic := goNames{}, map[string]string{}
	for _, svc := range pf.Services {
		if err := taken.claim("service "+svc.Name, svc.Name+"Server", "New"+svc.Name+"Server"); err != nil {
			return err
		}
		for _, rpc := range svc.RPCs {
			if prev, dup := logic[rpc.Name]; dup {
				return fmt.Errorf("proto: %s and rpc %s.%s both dispatch to logic.New%sLogic, and the logic package holds one %sLogic; rename one", prev, svc.Name, rpc.Name, rpc.Name, rpc.Name)
			}
			logic[rpc.Name] = "rpc " + svc.Name + "." + rpc.Name
		}
	}
	return nil
}

func generateServer(ctx context.Context, opt GenerateServerOptions, res *GenerateResult) error {
	if opt.Proto == "" || opt.PB == "" || opt.Out == "" {
		return fmt.Errorf("generate server needs --proto, --pb and --out")
	}
	pf, err := serviceProto(opt.Proto, "generate server")
	if err != nil {
		return err
	}
	out, err := filepath.Abs(opt.Out)
	if err != nil {
		return err
	}
	services, hasHTTP, err := serverServices(pf)
	if err != nil {
		return err
	}
	hasMCP, hasCLI := false, false
	taken := map[string]string{}
	for i := range services {
		hasCLI = hasCLI || len(services[i].Commands) > 0
		if services[i].Tools, err = serverTools(pf, services[i].Service, taken); err != nil {
			return err
		}
		hasMCP = hasMCP || len(services[i].Tools) > 0
	}
	return emitGenerated(ctx, filepath.Dir(out), opt.Check, res, []generatedFile{{
		Path:     out,
		Template: mustRead(template.GoServer),
		Data:     serverTemplateData{Package: goPackage(out), PB: opt.PB, Proto: pf, Services: services, HasHTTP: hasHTTP, HasMCP: hasMCP, HasList: hasList(pf), HasCLI: hasCLI},
		Go:       true,
		Force:    true,
	}})
}
