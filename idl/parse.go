package idl

import (
	"context"
	_ "embed"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/bufbuild/protocompile"
	"github.com/bufbuild/protocompile/reporter"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/tradalab/scorix/idl/scorixpb"
)

const traitPath = "scorix/v1/annotations.proto"

//go:embed scorix/v1/annotations.proto
var traitSource string

var (
	middlewareRe = regexp.MustCompile(`@middleware\s+([A-Za-z_][A-Za-z0-9_]*)`)
	eventRe      = regexp.MustCompile(`(?m)^[ \t]*//[ \t]*@event\b(?:[ \t]+(in|out))?[ \t]*\r?$`)
	broadcastRe  = regexp.MustCompile(`(?m)^[ \t]*//[ \t]*@broadcast[ \t]*\r?$`)
	mcpRe        = regexp.MustCompile(`(?m)^[ \t]*//[ \t]*@mcp(?:[ \t]+([^\r\n]*?))?[ \t]*\r?$`)
	cliRe        = regexp.MustCompile(`(?m)^[ \t]*//[ \t]*@cli(?:[ \t]+([^\r\n]*?))?[ \t]*\r?$`)
	nativeRe     = regexp.MustCompile(`(?m)^[ \t]*//[ \t]*@native(?:[ \t]+([^\r\n]*?))?[ \t]*\r?$`)
	trailingRe   = regexp.MustCompile(`@(?:event|broadcast|mcp|cli|native)(?:[ \t]+(?:in|out|destructive|headless|exec|timeout=\S+|arg=\S+|rest=\S+|secret=\S+))*[ \t]*\r?\n?$`)
)

// Parse compiles src as the proto file name and reads it into a File. Syntax and type
// errors carry name:line:col.
func Parse(name, src string) (File, error) {
	fd, err := compile(name, src, false)
	if err != nil {
		return File{}, err
	}
	return read(fd)
}

// ParseApp is Parse for a desktop app's proto, which also refuses enum and map: the app's
// generated Go and TS types have no shape for them yet.
func ParseApp(name, src string) (File, error) {
	fd, err := compile(name, src, true)
	if err != nil {
		return File{}, err
	}
	return read(fd)
}

func compile(name, src string, app bool) (protoreflect.FileDescriptor, error) {
	comp := protocompile.Compiler{
		Resolver: protocompile.WithStandardImports(&protocompile.SourceResolver{
			Accessor: protocompile.SourceAccessorFromMap(map[string]string{name: src, traitPath: traitSource}),
		}),
		SourceInfoMode: protocompile.SourceInfoStandard,
		Reporter:       reporter.NewReporter(nil, func(reporter.ErrorWithPos) {}),
	}
	files, err := comp.Compile(context.Background(), name)
	if err != nil {
		return nil, fmt.Errorf("proto: %w", err)
	}
	fd := files[0]
	for i := 0; i < fd.Imports().Len(); i++ {
		if p := fd.Imports().Get(i).Path(); p != traitPath {
			return nil, fmt.Errorf("proto: %s imports %s; an app proto may import only %s", name, p, traitPath)
		}
	}
	if err := rejectUnsupported(fd, app); err != nil {
		return nil, err
	}
	return fd, nil
}

type offence struct {
	line, col int
	word      string
	what      string
}

func rejectUnsupported(fd protoreflect.FileDescriptor, app bool) error {
	locs := fd.SourceLocations()
	var found []offence
	add := func(d protoreflect.Descriptor, word, what string) {
		l := locs.ByDescriptor(d)
		found = append(found, offence{l.StartLine, l.StartColumn, word, what})
	}
	for i := 0; app && i < fd.Enums().Len(); i++ {
		add(fd.Enums().Get(i), "enum", "")
	}
	msgs := fd.Messages()
	for i := 0; i < msgs.Len(); i++ {
		m := msgs.Get(i)
		for j := 0; j < m.Enums().Len(); j++ {
			what := ""
			if !app {
				what = fmt.Sprintf("enum %s is declared inside %s; declare it at the top level", m.Enums().Get(j).Name(), m.Name())
			}
			add(m.Enums().Get(j), "enum", what)
		}
		for j := 0; j < m.Messages().Len(); j++ {
			if nested := m.Messages().Get(j); !nested.IsMapEntry() {
				add(nested, "message", fmt.Sprintf("message %s is declared inside %s; declare it at the top level", nested.Name(), m.Name()))
			}
		}
		for j := 0; j < m.Oneofs().Len(); j++ {
			if o := m.Oneofs().Get(j); !o.IsSynthetic() {
				add(o, "oneof", "")
			}
		}
		for j := 0; j < m.Fields().Len(); j++ {
			f := m.Fields().Get(j)
			switch {
			case f.IsMap() && app:
				add(f, "map", "")
			case f.HasOptionalKeyword() && f.Kind() == protoreflect.BytesKind:
				add(f, "optional", fmt.Sprintf("field %s.%s is optional bytes; empty bytes already read as unset, drop the keyword", m.Name(), f.Name()))
			}
		}
	}
	if len(found) == 0 {
		return nil
	}
	sort.Slice(found, func(i, j int) bool {
		if found[i].line != found[j].line {
			return found[i].line < found[j].line
		}
		return found[i].col < found[j].col
	})
	first := found[0]
	if first.what != "" {
		return fmt.Errorf("proto: %s", first.what)
	}
	return fmt.Errorf("proto: %q is not supported by the scorix parser - model enums as string/int32, oneof as optional fields, map<> as a repeated message and stitch in logic", first.word)
}

func commentBlock(text string) string {
	if text == "" {
		return ""
	}
	var b strings.Builder
	for _, line := range strings.Split(strings.TrimSuffix(text, "\n"), "\n") {
		b.WriteString("//" + line + "\n")
	}
	return b.String()
}

func read(fd protoreflect.FileDescriptor) (File, error) {
	pf := File{Package: "app"}
	pf.ProtoPackage = string(fd.Package())
	if pkg := pf.ProtoPackage; pkg != "" {
		parts := strings.Split(pkg, ".")
		pf.Package = parts[len(parts)-1]
	}
	seen := map[string]bool{}
	addMiddleware := func(name string) {
		if !seen[name] {
			seen[name] = true
			pf.Middlewares = append(pf.Middlewares, name)
		}
		pf.HasMiddleware = true
	}

	msgs := fd.Messages()
	for i := 0; i < msgs.Len(); i++ {
		m := msgs.Get(i)
		if m.Name() == "Empty" {
			pf.HasEmpty = true
		}
		msg := Message{Name: string(m.Name()), GoName: ExportedName(string(m.Name()))}
		for j := 0; j < m.Fields().Len(); j++ {
			f, err := field(m.Fields().Get(j))
			if err != nil {
				return File{}, err
			}
			msg.Fields = append(msg.Fields, f)
		}
		pf.Messages = append(pf.Messages, msg)
	}
	sort.Slice(pf.Messages, func(i, j int) bool { return pf.Messages[i].Name < pf.Messages[j].Name })

	locs := fd.SourceLocations()
	routes := map[string]string{}
	paths := map[string][2]string{}
	listed := map[string]string{}
	svcs := fd.Services()
	for i := 0; i < svcs.Len(); i++ {
		s := svcs.Get(i)
		svc := Service{Name: string(s.Name())}
		header := commentBlock(locs.ByDescriptor(s).LeadingComments)
		for _, mm := range middlewareRe.FindAllStringSubmatch(header, -1) {
			svc.Middlewares = append(svc.Middlewares, mm[1])
			addMiddleware(mm[1])
		}
		if m := nativeRe.FindStringSubmatch(header); m != nil {
			svc.Native = true
			for _, mod := range strings.Fields(m[1]) {
				if mod != "expose" {
					return File{}, fmt.Errorf("proto: service %s has `@native %s`; the one modifier is `expose`", svc.Name, mod)
				}
				svc.NativeExpose = true
			}
		}
		methods := s.Methods()
		for j := 0; j < methods.Len(); j++ {
			md := methods.Get(j)
			loc := locs.ByDescriptor(md)
			if trailingRe.MatchString(loc.TrailingComments) {
				return File{}, fmt.Errorf("proto: @event/@broadcast/@mcp/@cli/@native must be on its own comment line above the rpc, not a trailing comment: rpc %s.%s", svc.Name, md.Name())
			}
			rpc, err := method(svc.Name, md, commentBlock(loc.LeadingComments))
			if err != nil {
				return File{}, err
			}
			if rpc.HTTP, err = route(svc.Name, md); err != nil {
				return File{}, err
			}
			if err := contract(svc.Name, md, &rpc); err != nil {
				return File{}, err
			}
			if rpc.List, err = listing(svc.Name, md); err != nil {
				return File{}, err
			}
			if h := rpc.HTTP; h != nil {
				key := routeKey(h)
				if prev, dup := routes[key]; dup {
					return File{}, fmt.Errorf("proto: %s.%s and %s both route %s", svc.Name, rpc.Name, prev, key)
				}
				routes[key] = svc.Name + "." + rpc.Name
				listedPath := strings.TrimSuffix(h.Path, endAnchor)
				shape := pathShape(listedPath)
				if prev, ok := paths[shape]; ok && prev[0] != listedPath {
					return File{}, fmt.Errorf("proto: %s.%s routes %s but %s routes %s; one path names its parameters the same way under every method", svc.Name, rpc.Name, h.Path, prev[1], prev[0])
				}
				paths[shape] = [2]string{listedPath, svc.Name + "." + rpc.Name}
				openapi := h.Method + " " + listedPath
				if prev, dup := listed[openapi]; dup {
					return File{}, fmt.Errorf("proto: %s.%s and %s both list as %s in OpenAPI; an exact {$} path and the subtree under it take two methods or two paths", svc.Name, rpc.Name, prev, openapi)
				}
				listed[openapi] = svc.Name + "." + rpc.Name
			}
			for _, mw := range rpc.Middlewares {
				addMiddleware(mw)
			}
			svc.RPCs = append(svc.RPCs, rpc)
		}
		if len(svc.RPCs) == 0 {
			return pf, fmt.Errorf("service %s has no rpc methods", svc.Name)
		}
		pf.Services = append(pf.Services, svc)
	}
	return pf, nil
}

func typeName(f protoreflect.FieldDescriptor) string {
	switch f.Kind() {
	case protoreflect.MessageKind, protoreflect.GroupKind:
		return string(f.Message().Name())
	case protoreflect.EnumKind:
		return string(f.Enum().Name())
	}
	return f.Kind().String()
}

func enumValues(f protoreflect.FieldDescriptor) []string {
	if f.Kind() != protoreflect.EnumKind {
		return nil
	}
	vs := f.Enum().Values()
	out := make([]string, vs.Len())
	for i := range out {
		out[i] = string(vs.Get(i).Name())
	}
	return out
}

func option[T proto.Message](opts proto.Message, empty T, xt protoreflect.ExtensionType) (T, error) {
	b, err := proto.Marshal(opts)
	if err != nil {
		return empty, err
	}
	fresh := opts.ProtoReflect().Type().New().Interface()
	if err := proto.Unmarshal(b, fresh); err != nil {
		return empty, err
	}
	return proto.GetExtension(fresh, xt).(T), nil
}

var (
	permissionRe = regexp.MustCompile(`^\S+$`)
	effectNames  = map[scorixpb.Effect]string{
		scorixpb.Effect_READONLY:    "readonly",
		scorixpb.Effect_IDEMPOTENT:  "idempotent",
		scorixpb.Effect_MUTATING:    "mutating",
		scorixpb.Effect_DESTRUCTIVE: "destructive",
	}
)

func verb(name, v string) bool {
	rest, ok := strings.CutPrefix(name, v)
	return ok && (rest == "" || (rest[0] >= 'A' && rest[0] <= 'Z'))
}

func compound(name string) bool {
	start := 0
	for i := 1; i <= len(name); i++ {
		if i == len(name) || (name[i] >= 'A' && name[i] <= 'Z') {
			if w := name[start:i]; w == "Or" || w == "And" {
				return true
			}
			start = i
		}
	}
	return false
}

func declared(md protoreflect.MethodDescriptor) (*scorixpb.Rpc, scorixpb.Effect, error) {
	rule, err := option(md.Options(), (*scorixpb.Rpc)(nil), scorixpb.E_Rpc)
	if err != nil {
		return nil, 0, err
	}
	effect := rule.GetEffect()
	if effect == scorixpb.Effect_EFFECT_UNSPECIFIED {
		switch name := string(md.Name()); {
		case (verb(name, "Get") || verb(name, "List")) && !compound(name):
			effect = scorixpb.Effect_READONLY
		case verb(name, "Delete"):
			effect = scorixpb.Effect_DESTRUCTIVE
		}
	}
	return rule, effect, nil
}

func contract(svc string, md protoreflect.MethodDescriptor, rpc *RPC) error {
	rule, effect, err := declared(md)
	if err != nil {
		return err
	}
	where := fmt.Sprintf("proto: rpc %s.%s", svc, md.Name())
	if rpc.MCPDestructive {
		switch effect {
		case scorixpb.Effect_EFFECT_UNSPECIFIED:
			effect = scorixpb.Effect_DESTRUCTIVE
		case scorixpb.Effect_DESTRUCTIVE:
		default:
			return fmt.Errorf("%s is @mcp destructive but its effect is %s; say one thing", where, effect)
		}
	}
	rpc.Effect = effectNames[effect]
	if err := traits(where, md, rpc, effect == scorixpb.Effect_DESTRUCTIVE); err != nil {
		return err
	}
	if t := rule.GetTimeout(); t != "" {
		d, err := time.ParseDuration(t)
		if err != nil || d <= 0 {
			return fmt.Errorf("%s (scorix.v1.rpc) timeout %q is not a positive Go duration such as 3s", where, t)
		}
		rpc.Timeout = d
	}
	if rule.GetReplay() {
		switch {
		case effect == scorixpb.Effect_READONLY:
			return fmt.Errorf("%s replays but is READONLY; a read can simply be retried", where)
		case md.IsStreamingServer() || md.IsStreamingClient():
			return fmt.Errorf("%s replays but streams its reply; only a single reply can be kept", where)
		}
		rpc.Replay = true
	}
	rpc.Audit = effect != scorixpb.Effect_READONLY
	if rule != nil && rule.Audit != nil {
		rpc.Audit = rule.GetAudit()
	}
	if p := rule.GetPermission(); p != "" {
		if !permissionRe.MatchString(p) {
			return fmt.Errorf("%s (scorix.v1.rpc) permission %q holds whitespace", where, p)
		}
		rpc.Permission = p
	}
	return nil
}

func traitTimeout(where, trait, text string) (time.Duration, error) {
	if text == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(text)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("%s (scorix.v1.%s) timeout %q is not a positive Go duration such as 10m", where, trait, text)
	}
	return d, nil
}

func traits(where string, md protoreflect.MethodDescriptor, rpc *RPC, destructive bool) error {
	twice := func(trait string) error {
		return fmt.Errorf("%s says @%s twice, as a comment and as (scorix.v1.%s); keep the option", where, trait, trait)
	}
	mcp, err := option(md.Options(), (*scorixpb.Mcp)(nil), scorixpb.E_Mcp)
	if err != nil {
		return err
	}
	if mcp != nil {
		if rpc.MCP {
			return twice("mcp")
		}
		rpc.MCP, rpc.MCPDestructive = true, destructive
		if rpc.MCPTimeout, err = traitTimeout(where, "mcp", mcp.GetTimeout()); err != nil {
			return err
		}
	}
	cli, err := option(md.Options(), (*scorixpb.Cli)(nil), scorixpb.E_Cli)
	if err != nil {
		return err
	}
	if cli != nil {
		if rpc.CLI {
			return twice("cli")
		}
		rpc.CLI, rpc.CLIDestructive = true, destructive
		rpc.CLIHeadless, rpc.CLIExec = cli.GetHeadless(), cli.GetExec()
		rpc.CLIArg, rpc.CLIRest, rpc.CLISecret = cli.GetArg(), cli.GetRest(), cli.GetSecret()
		if rpc.CLITimeout, err = traitTimeout(where, "cli", cli.GetTimeout()); err != nil {
			return err
		}
	}
	event, err := option(md.Options(), (*scorixpb.Event)(nil), scorixpb.E_Event)
	if err != nil {
		return err
	}
	if event != nil {
		if rpc.IsEvent {
			return twice("event")
		}
		rpc.IsEvent, rpc.EventDir = true, "out"
		if event.GetDirection() == scorixpb.Direction_IN {
			rpc.EventDir = "in"
		}
	}
	return nil
}

var columnRe = regexp.MustCompile(`^[a-z_][a-z0-9_.]*$`)

func hasField(m protoreflect.MessageDescriptor, name string, kind protoreflect.Kind) bool {
	f := m.Fields().ByName(protoreflect.Name(name))
	return f != nil && f.Kind() == kind && f.Cardinality() != protoreflect.Repeated
}

func listing(svc string, md protoreflect.MethodDescriptor) (*List, error) {
	rule, err := option(md.Options(), (*scorixpb.List)(nil), scorixpb.E_List)
	if err != nil || rule == nil {
		return nil, err
	}
	where := fmt.Sprintf("proto: rpc %s.%s (scorix.v1.list)", svc, md.Name())
	l := &List{Default: rule.GetDefaultPageSize(), Max: rule.GetMaxPageSize()}
	switch {
	case l.Default <= 0:
		return nil, fmt.Errorf("%s default_page_size must be positive", where)
	case l.Max < l.Default:
		return nil, fmt.Errorf("%s max_page_size %d is below default_page_size %d", where, l.Max, l.Default)
	case !hasField(md.Input(), "page_size", protoreflect.Int32Kind) || !hasField(md.Input(), "page_token", protoreflect.StringKind):
		return nil, fmt.Errorf("%s: %s needs int32 page_size and string page_token", where, md.Input().Name())
	case !hasField(md.Output(), "next_page_token", protoreflect.StringKind):
		return nil, fmt.Errorf("%s: %s needs string next_page_token", where, md.Output().Name())
	}
	wants, total := md.Input().Fields().ByName("want_total"), md.Output().Fields().ByName("total_size")
	switch {
	case wants != nil && !hasField(md.Input(), "want_total", protoreflect.BoolKind):
		return nil, fmt.Errorf("%s: %s.want_total must be a bool", where, md.Input().Name())
	case wants != nil && !hasField(md.Output(), "total_size", protoreflect.Int32Kind):
		return nil, fmt.Errorf("%s: %s asks for want_total but %s has no int32 total_size", where, md.Input().Name(), md.Output().Name())
	case wants == nil && total != nil:
		return nil, fmt.Errorf("%s: %s has total_size but %s has no want_total; counting is opt-in, so a caller must ask for it", where, md.Output().Name(), md.Input().Name())
	}
	l.Total = wants != nil
	items := 0
	for i := 0; i < md.Output().Fields().Len(); i++ {
		if f := md.Output().Fields().Get(i); f.IsList() && f.Kind() == protoreflect.MessageKind {
			items++
		}
	}
	if items != 1 {
		return nil, fmt.Errorf("%s: %s has %d repeated message fields; a page holds one", where, md.Output().Name(), items)
	}
	for _, part := range strings.Split(rule.GetOrder(), ",") {
		words := strings.Fields(part)
		if len(words) == 0 || len(words) > 2 || !columnRe.MatchString(words[0]) || (len(words) == 2 && words[1] != "asc" && words[1] != "desc") {
			return nil, fmt.Errorf("%s order %q: each part is a column, then asc or desc", where, rule.GetOrder())
		}
		l.Order = append(l.Order, Order{Column: words[0], Desc: len(words) == 2 && words[1] == "desc"})
	}
	unique := rule.GetUnique()
	if unique == "" {
		unique = "id"
	}
	if l.Order[len(l.Order)-1].Column != unique {
		return nil, fmt.Errorf("%s order %q has no unique tiebreaker; end it with %s", where, rule.GetOrder(), unique)
	}
	return l, nil
}

var paramRe = regexp.MustCompile(`\{([^{}]*)\}`)

const endAnchor = "{$}"

func pathShape(path string) string {
	return paramRe.ReplaceAllStringFunc(path, func(p string) string {
		if p == endAnchor {
			return p
		}
		return "{}"
	})
}

func route(svc string, md protoreflect.MethodDescriptor) (*HTTP, error) {
	rule, err := option(md.Options(), (*scorixpb.Http)(nil), scorixpb.E_Http)
	if err != nil || rule == nil {
		return nil, err
	}
	where := fmt.Sprintf("proto: rpc %s.%s (scorix.v1.http)", svc, md.Name())
	var h HTTP
	for method, path := range map[string]string{"GET": rule.GetGet(), "POST": rule.GetPost(), "PUT": rule.GetPut(), "DELETE": rule.GetDelete(), "PATCH": rule.GetPatch()} {
		if path == "" {
			continue
		}
		if h.Method != "" {
			return nil, fmt.Errorf("%s names both %s and %s; a route has one method", where, h.Method, method)
		}
		h.Method, h.Path = method, path
	}
	switch {
	case h.Method == "":
		return nil, fmt.Errorf("%s names no method; set one of get, post, put, delete or patch", where)
	case !strings.HasPrefix(h.Path, "/"):
		return nil, fmt.Errorf("%s path %q must start with /", where, h.Path)
	case rule.GetBody() != "" && rule.GetBody() != "*":
		return nil, fmt.Errorf("%s body %q; it is \"*\" or empty", where, rule.GetBody())
	case rule.GetBody() == "*" && (h.Method == "GET" || h.Method == "DELETE"):
		return nil, fmt.Errorf("%s is %s with body \"*\"; a %s has no body", where, h.Method, h.Method)
	}
	h.Body, h.Anonymous = rule.GetBody(), rule.GetAnonymous()
	if i := strings.Index(h.Path, endAnchor); i >= 0 && (i != len(h.Path)-len(endAnchor) || h.Path[i-1] != '/') {
		return nil, fmt.Errorf("%s path %q: {$} only ends a path, after a /, where it matches that path exactly", where, h.Path)
	}
	for _, m := range paramRe.FindAllStringSubmatch(strings.TrimSuffix(h.Path, endAnchor), -1) {
		f := md.Input().Fields().ByName(protoreflect.Name(m[1]))
		if f == nil || f.Cardinality() == protoreflect.Repeated || f.Kind() == protoreflect.MessageKind {
			return nil, fmt.Errorf("%s path segment {%s} is not a scalar field of %s", where, m[1], md.Input().Name())
		}
		h.Params = append(h.Params, m[1])
	}
	return &h, nil
}

func field(f protoreflect.FieldDescriptor) (Field, error) {
	elem, mapKey := f, ""
	if f.IsMap() {
		elem, mapKey = f.MapValue(), f.MapKey().Kind().String()
	}
	t := typeName(elem)
	repeated := f.IsList()
	goType, tsType := scalarToGo(t), scalarToTS(t)
	optional := f.HasOptionalKeyword()
	switch {
	case mapKey != "":
		goType = "map[" + scalarToGo(mapKey) + "]" + goType
		tsType = "Record<string, " + tsType + ">"
	case repeated:
		goType = "[]" + goType
		tsType += "[]"
	case optional:
		goType = "*" + goType
		tsType += " | null"
	}
	name := string(f.Name())
	binding, err := option(f.Options(), (*scorixpb.Field)(nil), scorixpb.E_Field)
	if err != nil {
		return Field{}, err
	}
	if binding.GetHeader() != "" && (f.Kind() != protoreflect.StringKind || repeated) {
		return Field{}, fmt.Errorf("proto: field %s.%s takes header %q but is not a single string", f.Parent().Name(), name, binding.GetHeader())
	}
	return Field{
		Name:     name,
		JSONName: SnakeName(name),
		GoName:   ExportedName(name),
		Type:     t,
		GoType:   goType,
		TSType:   tsType,
		Repeated: repeated,
		Map:      f.IsMap(),
		MapKey:   mapKey,
		Enum:     enumValues(elem),
		Optional: optional,
		WireName: f.JSONName(),
		Header:   binding.GetHeader(),
		Class:    map[scorixpb.Class]string{scorixpb.Class_PII: "pii", scorixpb.Class_SECRET: "secret"}[binding.GetClass()],
	}, nil
}

func method(svcName string, md protoreflect.MethodDescriptor, block string) (RPC, error) {
	name := string(md.Name())
	arity := "unary"
	switch {
	case !md.IsStreamingClient() && md.IsStreamingServer():
		arity = "server-stream"
	case md.IsStreamingClient() && !md.IsStreamingServer():
		arity = "client-stream"
	case md.IsStreamingClient() && md.IsStreamingServer():
		arity = "bidi"
	}
	var mws []string
	for _, mm := range middlewareRe.FindAllStringSubmatch(block, -1) {
		mws = append(mws, mm[1])
	}
	rpc := RPC{
		Name:           name,
		RequestType:    string(md.Input().Name()),
		ResponseType:   string(md.Output().Name()),
		Middlewares:    mws,
		Arity:          arity,
		IsServerStream: arity == "server-stream",
	}
	if m := eventRe.FindStringSubmatch(block); m != nil {
		rpc.IsEvent = true
		rpc.EventDir = "out"
		if m[1] == "in" {
			rpc.EventDir = "in"
		}
		rpc.Legacy = append(rpc.Legacy, "@event")
	} else if broadcastRe.MatchString(block) {
		rpc.IsEvent = true
		rpc.EventDir = "out"
		rpc.Legacy = append(rpc.Legacy, "@broadcast")
	}
	if m := mcpRe.FindStringSubmatch(block); m != nil {
		if err := mcpModifiers(&rpc, svcName, strings.Fields(m[1])); err != nil {
			return RPC{}, err
		}
		rpc.MCP = true
		rpc.Legacy = append(rpc.Legacy, "@mcp")
	}
	if m := cliRe.FindStringSubmatch(block); m != nil {
		if err := cliModifiers(&rpc, svcName, strings.Fields(m[1])); err != nil {
			return RPC{}, err
		}
		rpc.CLI = true
		rpc.Legacy = append(rpc.Legacy, "@cli")
	}
	if nativeRe.MatchString(block) {
		return RPC{}, fmt.Errorf("proto: rpc %s.%s has @native; it goes above the service, which becomes the plugin", svcName, name)
	}
	rpc.Doc = doc(block)
	return rpc, nil
}

func duration(svc, rpc, anno, mod string) (time.Duration, error) {
	d, err := time.ParseDuration(strings.TrimPrefix(mod, "timeout="))
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("proto: rpc %s.%s has `@%s %s`; timeout takes a positive Go duration such as 10m", svc, rpc, anno, mod)
	}
	return d, nil
}

func mcpModifiers(rpc *RPC, svc string, mods []string) error {
	for _, mod := range mods {
		switch {
		case mod == "destructive":
			rpc.MCPDestructive = true
		case strings.HasPrefix(mod, "timeout="):
			d, err := duration(svc, rpc.Name, "mcp", mod)
			if err != nil {
				return err
			}
			rpc.MCPTimeout = d
		default:
			return fmt.Errorf("proto: rpc %s.%s has `@mcp %s`; the modifiers are `destructive` and `timeout=<duration>`", svc, rpc.Name, mod)
		}
	}
	return nil
}

func cliModifiers(rpc *RPC, svc string, mods []string) error {
	named := func(mod, prefix, hint string) (string, error) {
		v := strings.TrimPrefix(mod, prefix)
		if v == "" {
			return "", fmt.Errorf("proto: rpc %s.%s has `@cli %s`; %s", svc, rpc.Name, prefix, hint)
		}
		return v, nil
	}
	for _, mod := range mods {
		var err error
		switch {
		case mod == "destructive":
			rpc.CLIDestructive = true
		case mod == "headless":
			rpc.CLIHeadless = true
		case mod == "exec":
			rpc.CLIExec = true
		case strings.HasPrefix(mod, "timeout="):
			rpc.CLITimeout, err = duration(svc, rpc.Name, "cli", mod)
		case strings.HasPrefix(mod, "secret="):
			rpc.CLISecret, err = named(mod, "secret=", "name the string field to read from stdin")
		case strings.HasPrefix(mod, "rest="):
			rpc.CLIRest, err = named(mod, "rest=", "name the repeated string field that takes the words after --")
		case strings.HasPrefix(mod, "arg="):
			rpc.CLIArg, err = named(mod, "arg=", "name the request field to take positionally")
		default:
			err = fmt.Errorf("proto: rpc %s.%s has `@cli %s`; the modifiers are `destructive`, `headless`, `exec`, `timeout=<duration>`, `arg=<field>`, `rest=<field>` and `secret=<field>`", svc, rpc.Name, mod)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func doc(block string) string {
	var words []string
	for _, line := range strings.Split(block, "\n") {
		text := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "//"))
		if text == "" || strings.HasPrefix(text, "@") {
			continue
		}
		words = append(words, text)
	}
	return strings.Join(words, " ")
}

func scalarToGo(name string) string {
	switch name {
	case "double":
		return "float64"
	case "float":
		return "float32"
	case "int32", "sint32", "sfixed32":
		return "int32"
	case "int64", "sint64", "sfixed64":
		return "int64"
	case "uint32", "fixed32":
		return "uint32"
	case "uint64", "fixed64":
		return "uint64"
	case "bool", "string":
		return name
	case "bytes":
		return "[]byte"
	}
	return ExportedName(name)
}

func scalarToTS(name string) string {
	switch name {
	case "double", "float", "int32", "sint32", "sfixed32", "int64", "sint64", "sfixed64", "uint32", "fixed32", "uint64", "fixed64":
		return "number"
	case "bool":
		return "boolean"
	case "string":
		return "string"
	case "bytes":
		return "Uint8Array"
	case "Empty":
		return "Record<string, never>"
	}
	return ExportedName(name)
}
